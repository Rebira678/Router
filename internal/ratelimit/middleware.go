package ratelimit

import (
	"log/slog"
	"net/http"

	"github/rebik/internal/middleware"
)

// KeyFunc extracts the identity a rate limit should be applied per — for
// Router, that's meant to be "one bucket per API key." Making this a
// function type (rather than hardcoding "always use the Authorization
// header") is what lets Day 12's real JWT-based auth swap in a proper
// tenant ID later without this middleware needing to change at all.
type KeyFunc func(r *http.Request) string

// KeyFromAuthHeader is today's stand-in KeyFunc: it uses the raw
// Authorization header value as the rate-limit identity. This is
// deliberately naive — there's no real API key validation yet (that's
// Day 12) — but it's enough to prove the per-tenant isolation genuinely
// works: two different Authorization headers get two independent buckets,
// today, with real code, not just in theory.
func KeyFromAuthHeader(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		return auth
	}
	return "anonymous" // requests with no Authorization header share one bucket
}

// Middleware wraps next with rate limiting: every request must get an
// Allow() from the Limiter before it's allowed to reach next at all.
// limiter is now the Limiter INTERFACE (not *Limiter — that name is gone,
// it's an interface today), so this same middleware works unchanged
// whether it's handed a MemoryLimiter or today's RedisLimiter.
//
// Where this sits in the handler chain is a real design decision, not an
// afterthought: it belongs OUTSIDE the Day 2 worker pool, not inside it.
// A request that's over its rate limit should be rejected before it ever
// takes up a worker-pool queue slot — otherwise a client hammering past
// its own limit could still crowd out other tenants' legitimate traffic
// by filling the shared queue, which defeats half the point of having
// per-tenant limits in the first place.
func Middleware(limiter Limiter, keyFn KeyFunc) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)

			allowed, err := limiter.Allow(r.Context(), key)
			if err != nil {
				// NEW today, and a real design decision worth being able to
				// defend out loud: what should happen if Redis itself is
				// unreachable? Two honest options exist —
				//   FAIL CLOSED: reject every request until Redis recovers.
				//     Safer for the rate limit's integrity, but now a Redis
				//     outage takes down the entire gateway, not just rate
				//     limiting — a dependency that was meant to protect
				//     availability becomes the thing that removes it.
				//   FAIL OPEN: let the request through anyway, log loudly.
				//     Rate limiting is temporarily not enforced, but Router
				//     itself stays up. Chosen here, because losing rate
				//     limiting for a few minutes during a Redis blip is a
				//     recoverable, boring problem — losing the whole gateway
				//     because a side concern went down is not.
				// This is genuinely debatable and depends on what you're
				// protecting; naming the trade-off out loud is the point.
				slog.Error("ratelimit: backend error, failing open", "error", err, "key_hash", hashIdentity(key))
				next.ServeHTTP(w, r)
				return
			}

			if !allowed {
				// Day 7 fix: log the hashed fingerprint, not the raw key —
				// the raw key is a real Authorization header value, and it
				// has no business sitting in plain text in application logs.
				slog.Warn("ratelimit: request rejected, bucket empty", "key_hash", hashIdentity(key), "path", r.URL.Path)
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded for this API key", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
