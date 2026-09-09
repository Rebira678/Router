package cors

import "net/http"

// Middleware allows Cross-Origin Resource Sharing (CORS) so that our React
// frontend can securely interact with the API Gateway from a different local port.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// In a real production environment, you would lock this down to specific domains.
		// For our local visualizer, "*" is perfectly fine.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Authorization, X-Request-ID, Idempotency-Key, X-Mock-Delay-Ms")
		
		// This is critical: We must explicitly expose our custom tracking headers
		// so the React frontend can read them from the HTTP Response.
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id, X-Mock-Upstream, Retry-After")

		// Handle preflight OPTIONS requests immediately
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
