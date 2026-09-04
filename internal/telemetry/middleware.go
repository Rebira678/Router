package telemetry

import (
	"net/http"
	"strconv"
	"time"
)

// Middleware instruments the HTTP handler with Prometheus RED metrics.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ActiveRequests.Inc()
		defer ActiveRequests.Dec()

		start := time.Now()

		rw := &responseWriterInterceptor{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		statusStr := strconv.Itoa(rw.statusCode)

		RequestDuration.WithLabelValues(statusStr).Observe(duration)
	})
}

// responseWriterInterceptor captures the HTTP status code for metrics since standard
// http.ResponseWriter does not expose it after writing.
type responseWriterInterceptor struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriterInterceptor) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher to ensure streaming SSE responses continue to work properly.
func (rw *responseWriterInterceptor) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
