package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github/rebik/internal/middleware"
	"github.com/golang-jwt/jwt/v5"
)

type contextKey string

const identityKey = contextKey("identity")

// Middleware validates a JWT from the Authorization header and extracts
// the tenant's identity (the "sub" claim).
//
// DESIGN DECISION: JWT vs Opaque Tokens
// We explicitly chose JWTs (stateless tokens) over opaque tokens for the API gateway.
// If we used opaque tokens, this middleware would have to perform a network call
// (to Postgres or Redis) on *every single request* just to figure out who the caller is.
// By using cryptographically signed JWTs, the router can independently verify identity,
// tampering, and expiration purely in CPU. This eliminates a massive latency bottleneck
// and a point of failure for an infrastructure component where latency is critical.
func Middleware(secretKey string) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "missing authorization header", http.StatusUnauthorized)
				return
			}

			// Expect "Bearer <token>"
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				http.Error(w, "invalid authorization header format", http.StatusUnauthorized)
				return
			}

			tokenString := parts[1]

			// Parse and validate the JWT.
			token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
				// Critical security check: validate the algorithm matches what we expect (HMAC).
				// This prevents the "alg: none" bypass vulnerability.
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
				}
				return []byte(secretKey), nil
			})

			if err != nil || !token.Valid {
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}

			// Extract the tenant identity (stored in the "sub" or subject claim)
			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				http.Error(w, "invalid token claims", http.StatusUnauthorized)
				return
			}

			subject, err := claims.GetSubject()
			if err != nil || subject == "" {
				http.Error(w, "token missing subject claim", http.StatusUnauthorized)
				return
			}

			// Inject the validated identity into the request context (the "sticky name tag")
			// so the Rate-Limiter and Usage-Tracker can simply read it without re-parsing.
			ctx := context.WithValue(r.Context(), identityKey, subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// FromContext retrieves the validated raw identity from the context.
// Because the Middleware runs first, downstream handlers can trust this value.
func FromContext(ctx context.Context) string {
	if id, ok := ctx.Value(identityKey).(string); ok {
		return id
	}
	return "anonymous"
}
