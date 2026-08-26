package identity

import (
	"context"
	"net/http"

	"github/rebik/internal/middleware"
)

type contextKey string

const identityKey = contextKey("identity")

// Middleware extracts the raw identity (e.g., Authorization header)
// and places it in the request context.
func Middleware() middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				auth = "anonymous"
			}
			ctx := context.WithValue(r.Context(), identityKey, auth)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// FromContext retrieves the raw identity from the context.
func FromContext(ctx context.Context) string {
	if id, ok := ctx.Value(identityKey).(string); ok {
		return id
	}
	return "anonymous"
}
