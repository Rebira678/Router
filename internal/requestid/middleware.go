package requestid

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
)

type contextKey string

const reqIDKey contextKey = "request_id"

// Middleware injects a unique correlation ID into the request context.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 16)
			rand.Read(b)
			id = fmt.Sprintf("req-%x", b)
		}

		ctx := context.WithValue(r.Context(), reqIDKey, id)
		
		// Set headers for both the response to the client and the proxied request upstream
		w.Header().Set("X-Request-ID", id)
		r.Header.Set("X-Request-ID", id)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// FromContext extracts the request ID from the context.
func FromContext(ctx context.Context) string {
	if id, ok := ctx.Value(reqIDKey).(string); ok {
		return id
	}
	return ""
}
