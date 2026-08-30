package usage

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github/rebik/internal/identity"
	"github/rebik/internal/middleware"
)

// Middleware records a usage event after the request completes.
func Middleware(store *Store, model string) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Proceed with the request
			next.ServeHTTP(w, r)

			// The request has completed. Now record usage.
			rawIdentity := identity.FromContext(r.Context())

			// For Day 11, we hardcode token count and cost, since we aren't parsing
			// the actual LLM responses yet.
			tokensUsed := 10
			costMicros := int64(100) // $0.0001
			occurredAt := time.Now()

			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			err := store.Record(ctx, rawIdentity, model, tokensUsed, costMicros, occurredAt)
			if err != nil {
				slog.Error("usage: failed to record event", "error", err, "tenant_hash", identity.Hash(rawIdentity))
			}
		})
	}
}


