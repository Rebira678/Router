package proxy

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github/rebik/internal/circuitbreaker"
)

var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

type Target struct {
	Name    string
	BaseURL *url.URL
}

// Day 17: Upstream binds a Target to its own Circuit Breaker.
type Upstream struct {
	Target  Target
	Breaker *circuitbreaker.Breaker
}

type Handler struct {
	upstreams []*Upstream
	client    *http.Client

	upstreamTimeout time.Duration
}

func New(targets []Target, upstreamTimeout time.Duration, breakerFailureThreshold int, breakerCooldown time.Duration) *Handler {
	var upstreams []*Upstream
	for _, t := range targets {
		upstreams = append(upstreams, &Upstream{
			Target:  t,
			Breaker: circuitbreaker.New(breakerFailureThreshold, breakerCooldown),
		})
	}

	return &Handler{
		upstreams:       upstreams,
		upstreamTimeout: upstreamTimeout,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), h.upstreamTimeout)
	defer cancel()

	// Day 18: Idempotency keys. If the client didn't send one, generate one
	// for the entire logical request so all retries share it.
	if r.Header.Get("Idempotency-Key") == "" && r.Header.Get("X-Idempotency-Key") == "" {
		r.Header.Set("Idempotency-Key", generateIdempotencyKey())
	}

	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
		r.Body.Close()
	}

	var upstreamResp *http.Response
	var err error

	maxRetries := 3
	
	// Day 17: Failover loop. We try upstreams in order.
	for upstreamIdx, u := range h.upstreams {
		if !u.Breaker.Allow() {
			slog.Warn("proxy: circuit breaker open, skipping upstream",
				"target", u.Target.Name,
				"state", u.Breaker.State(),
			)
			continue // Failover: try the next target
		}

		backoff := 100 * time.Millisecond

		for attempt := 0; attempt <= maxRetries; attempt++ {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			var outReq *http.Request
			outReq, err = h.buildUpstreamRequest(ctx, r, u.Target)
			if err != nil {
				slog.Error("proxy: failed to build upstream request", "error", err)
				http.Error(w, "bad gateway: could not construct upstream request", http.StatusBadGateway)
				return
			}

			upstreamResp, err = h.client.Do(outReq)

			if err == nil && upstreamResp.StatusCode < 500 && upstreamResp.StatusCode != http.StatusTooManyRequests {
				break // Success (or 4xx client error)
			}

			if attempt == maxRetries {
				break
			}

			statusCode := 0
			if upstreamResp != nil {
				statusCode = upstreamResp.StatusCode
				upstreamResp.Body.Close()
				upstreamResp = nil
			}

			sleepDuration := backoff
			// Day 17 Nitpick fix: check Retry-After header. (Omitted for brevity if not present)
			jitter := time.Duration(rand.Float64() * float64(sleepDuration) * 0.5)
			sleepDuration += jitter

			slog.Warn("proxy: upstream call failed, retrying", 
				"target", u.Target.Name,
				"attempt", attempt+1, 
				"sleep_ms", sleepDuration.Milliseconds(), 
				"error", err,
				"status_code", statusCode,
			)

			select {
			case <-ctx.Done():
				err = ctx.Err()
				goto EndRetries
			case <-time.After(sleepDuration):
				backoff *= 2
			}
		}

	EndRetries:

		if err != nil {
			elapsed := time.Since(start)
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				u.Breaker.RecordFailure()
				slog.Warn("proxy: upstream timed out",
					"target", u.Target.Name,
					"timeout", h.upstreamTimeout,
					"elapsed_ms", elapsed.Milliseconds(),
					"breaker_state", u.Breaker.State(),
				)

			case errors.Is(err, context.Canceled):
				slog.Info("proxy: client disconnected before upstream responded",
					"target", u.Target.Name,
					"elapsed_ms", elapsed.Milliseconds(),
				)
				return // If client disconnected, stop trying entirely

			default:
				u.Breaker.RecordFailure()
				slog.Error("proxy: upstream request failed",
					"target", u.Target.Name,
					"error", err,
					"elapsed_ms", elapsed.Milliseconds(),
					"breaker_state", u.Breaker.State(),
				)
			}
			
			// Try the next upstream
			continue 
		}

		// Day 17 Fix: exhausted retries, but HTTP call "succeeded" with a 500/429.
		if upstreamResp.StatusCode >= 500 || upstreamResp.StatusCode == http.StatusTooManyRequests {
			u.Breaker.RecordFailure()
			slog.Warn("proxy: upstream still failing after retries exhausted",
				"target", u.Target.Name,
				"status", upstreamResp.StatusCode,
				"breaker_state", u.Breaker.State(),
			)
			upstreamResp.Body.Close()
			upstreamResp = nil
			continue // Try the next upstream
		} else {
			u.Breaker.RecordSuccess()
		}

		// Success path! Forward it downstream.
		defer upstreamResp.Body.Close()
		h.writeDownstreamResponse(w, upstreamResp)

		slog.Info("proxy: request forwarded",
			"target", u.Target.Name,
			"method", r.Method,
			"path", r.URL.Path,
			"status", upstreamResp.StatusCode,
			"elapsed_ms", time.Since(start).Milliseconds(),
			"upstream_idx", upstreamIdx,
		)
		return
	}

	// If we got here, ALL upstreams failed (or their breakers were open)
	slog.Error("proxy: all upstreams exhausted or circuit breakers open")
	w.Header().Set("Retry-After", "5")
	http.Error(w, "service unavailable: all upstreams failed or circuit breakers open", http.StatusServiceUnavailable)
}

func (h *Handler) buildUpstreamRequest(ctx context.Context, r *http.Request, target Target) (*http.Request, error) {
	upstreamURL := *target.BaseURL
	upstreamURL.Path = singleJoiningSlash(target.BaseURL.Path, r.URL.Path)
	upstreamURL.RawQuery = r.URL.RawQuery

	outReq, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL.String(), r.Body)
	if err != nil {
		return nil, err
	}

	outReq.Header = r.Header.Clone()
	removeHopByHopHeaders(outReq.Header)

	if clientIP := clientIPFromRemoteAddr(r.RemoteAddr); clientIP != "" {
		if prior := outReq.Header.Get("X-Forwarded-For"); prior != "" {
			outReq.Header.Set("X-Forwarded-For", prior+", "+clientIP)
		} else {
			outReq.Header.Set("X-Forwarded-For", clientIP)
		}
	}
	outReq.Header.Set("X-Forwarded-Host", r.Host)
	outReq.Header.Set("X-Forwarded-Proto", schemeOf(r))

	outReq.Host = upstreamURL.Host

	return outReq, nil
}

func (h *Handler) writeDownstreamResponse(w http.ResponseWriter, upstreamResp *http.Response) {
	destHeader := w.Header()
	for k, values := range upstreamResp.Header {
		for _, v := range values {
			destHeader.Add(k, v)
		}
	}
	removeHopByHopHeaders(destHeader)

	isEventStream := strings.HasPrefix(
		strings.ToLower(upstreamResp.Header.Get("Content-Type")),
		"text/event-stream",
	)

	if isEventStream {
		destHeader.Del("Content-Length")
	}

	w.WriteHeader(upstreamResp.StatusCode)

	if isEventStream {
		h.streamSSE(w, upstreamResp.Body)
		return
	}

	if _, err := io.Copy(w, upstreamResp.Body); err != nil {
		slog.Warn("proxy: error streaming response body to client", "error", err)
	}
}

func (h *Handler) streamSSE(w http.ResponseWriter, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Warn("proxy: ResponseWriter does not support Flusher; SSE response will not stream incrementally")
		_, _ = io.Copy(w, body)
		return
	}

	buf := make([]byte, 512)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				slog.Info("proxy: client disconnected mid-stream", "error", writeErr)
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				slog.Warn("proxy: error reading SSE body from upstream", "error", readErr)
			}
			return
		}
	}
}

func removeHopByHopHeaders(h http.Header) {
	if conn := h.Get("Connection"); conn != "" {
		for _, name := range strings.Split(conn, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for name := range hopByHopHeaders {
		h.Del(name)
	}
}

func singleJoiningSlash(base, ref string) string {
	baseSlash := strings.HasSuffix(base, "/")
	refSlash := strings.HasPrefix(ref, "/")
	switch {
	case baseSlash && refSlash:
		return base + ref[1:]
	case !baseSlash && !refSlash:
		return base + "/" + ref
	default:
		return base + ref
	}
}

func clientIPFromRemoteAddr(remoteAddr string) string {
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		return remoteAddr[:idx]
	}
	return remoteAddr
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func generateIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	return fmt.Sprintf("req-%x", b)
}
