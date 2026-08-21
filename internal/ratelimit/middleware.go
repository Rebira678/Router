package ratelimit

import (
	"log/slog"
	"net/http"
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
//
// Where this sits in the handler chain is a real design decision, not an
// afterthought: it belongs OUTSIDE the Day 2 worker pool, not inside it.
// A request that's over its rate limit should be rejected before it ever
// takes up a worker-pool queue slot — otherwise a client hammering past
// its own limit could still crowd out other tenants' legitimate traffic
// by filling the shared queue, which defeats half the point of having
// per-tenant limits in the first place.
func Middleware(next http.Handler, limiter *Limiter, keyFn KeyFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := keyFn(r)

		if !limiter.Allow(key) {
			slog.Warn("ratelimit: request rejected, bucket empty", "key", key, "path", r.URL.Path)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded for this API key", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}
