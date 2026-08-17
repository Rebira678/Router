// Package proxy implements Router's core passthrough: take an incoming
// *http.Request, forward it to an upstream LLM provider, and stream the
// response back — unmodified in substance, but correct at the transport
// level. That correctness is the entire point of Day 1.
//
// A naive proxy is ~10 lines: read the body, http.Post it somewhere, copy
// the response back. That version breaks in ways that only show up under
// load or with a real client: it leaks connection-management headers to
// the upstream, it silently drops the client's cancellation signal, it
// buffers entire bodies in memory (fatal once streaming/SSE responses show
// up on Day 4), and it forges no trace of where the request actually came
// from. This file exists to avoid all four.
package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// hopByHopHeaders are headers that describe a *single transport hop*, not
// the request/response itself — RFC 7230 §6.1 (and the older RFC 2616
// §13.5.1) name these explicitly. A proxy MUST strip them before forwarding
// in either direction, because they belong to the client<->proxy
// connection, not the proxy<->upstream one. Forwarding "Connection: close"
// from the client, for example, would tell your upstream to tear down a
// connection you (the proxy) still control and may want to keep pooled.
//
// "Connection" itself is special: its *value* lists additional header
// names that are also hop-by-hop for this specific request and must be
// stripped too (see removeHopByHopHeaders below).
//
// SIMPLE EXPLANATION: Think of these as "temporary delivery labels" used only
// between the user and the proxy. They must be removed before sending the message
// to the backend server so connection pools and transport links don't break.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true, // non-standard but widely sent
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// Target describes the single upstream this Day-1 proxy forwards to.
// (Multiple targets, health checks, and failover are Week 3 problems —
// today there is exactly one upstream and no decision to make about it.)
//
// SIMPLE EXPLANATION: Target stores the backend server's nickname (for logs)
// and its destination URL (e.g., http://localhost:9090).
type Target struct {
	Name    string // human-readable label, used in logs
	BaseURL *url.URL
}

// Handler is the reverse proxy itself. It satisfies http.Handler directly
// rather than wrapping httputil.NewSingleHostReverseProxy, on purpose: the
// stdlib version is what you should reach for in production, but writing
// the forwarding logic by hand once is how "request forwarding at the
// transport level" stops being an abstraction you've read about.
//
// SIMPLE EXPLANATION: This struct is the proxy itself. It holds the target info
// and a custom HTTP client used to send outbound HTTP calls to the target.
type Handler struct {
	target Target
	client *http.Client
}

// New builds a Handler that forwards every request it receives to target.
func New(target Target) *Handler {
	return &Handler{
		target: target,
		// A dedicated client (not http.DefaultClient) with its own
		// Transport means Router controls connection pooling and
		// timeouts independently of anything else in the process.
		// Per-request timeouts arrive properly via context on Day 3;
		// this is a coarse backstop so a hung upstream can't wedge
		// the proxy's connection pool forever.
		//
		// SIMPLE EXPLANATION: Creating a dedicated client prevents infinite waiting if
		// the upstream hangs (30s safety timeout) and reuses open TCP connections
		// (MaxIdleConns) to make proxying high-speed and efficient.
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

// ServeHTTP implements http.Handler.
//
// SIMPLE EXPLANATION: This is the main entry point for every incoming HTTP request.
// It builds an upstream request, executes it, copies the response back to the client,
// and logs how long the operation took.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Step A: Convert the incoming client request into an outbound upstream request.
	outReq, err := h.buildUpstreamRequest(r)
	if err != nil {
		slog.Error("proxy: failed to build upstream request", "error", err)
		http.Error(w, "bad gateway: could not construct upstream request", http.StatusBadGateway)
		return
	}

	// Step B: Send the request to the upstream target server.
	upstreamResp, err := h.client.Do(outReq)
	if err != nil {
		// This is the branch that fires for: DNS failure, connection
		// refused, TLS handshake failure, or — once Day 3 wires
		// context timeouts through — a client that gave up waiting.
		slog.Error("proxy: upstream request failed",
			"target", h.target.Name,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds(),
		)
		http.Error(w, "bad gateway: upstream request failed", http.StatusBadGateway)
		return
	}
	defer upstreamResp.Body.Close()

	// Step C: Mirror the upstream response headers, status code, and body back to the original client.
	h.writeDownstreamResponse(w, upstreamResp)

	// Step D: Log a successful request summary.
	slog.Info("proxy: request forwarded",
		"target", h.target.Name,
		"method", r.Method,
		"path", r.URL.Path,
		"status", upstreamResp.StatusCode,
		"elapsed_ms", time.Since(start).Milliseconds(),
	)
}

// buildUpstreamRequest turns the inbound client request into the request
// Router will actually send upstream. This is the part that is easy to get
// subtly wrong, so it is worth walking through in order:
func (h *Handler) buildUpstreamRequest(r *http.Request) (*http.Request, error) {
	// 1. Rewrite the URL onto the target host, keeping the client's
	//    original path and query string. We resolve against BaseURL
	//    rather than string-concatenating so path joining (trailing
	//    slashes, etc.) follows normal URL semantics.
	//
	// SIMPLE EXPLANATION: Combines target base URL (e.g. http://localhost:9090) with
	// the client's path (e.g. /v1/chat/completions) and query parameters cleanly.
	upstreamURL := *h.target.BaseURL
	upstreamURL.Path = singleJoiningSlash(h.target.BaseURL.Path, r.URL.Path)
	upstreamURL.RawQuery = r.URL.RawQuery

	// 2. Build the new request with r.Context() as its context — NOT
	//    context.Background(). This is what makes the upstream call
	//    cancelable at all: if the client's TCP connection drops, or a
	//    context deadline set upstream of us fires, http.Client.Do
	//    will abort the in-flight request instead of running it to
	//    completion for nothing. (We don't set a deadline *on* this
	//    context yet — that's Day 3 — but plumbing it through now is
	//    what makes Day 3 a small diff instead of a rewrite.)
	//
	// 3. Pass r.Body straight through as an io.Reader rather than
	//    reading it into a []byte first. That keeps the request body
	//    streaming — a multi-megabyte upload is forwarded incrementally,
	//    not buffered twice in RAM. This matters even more once
	//    responses stream on Day 4, but the discipline starts here.
	//
	// SIMPLE EXPLANATION: Passes the client context (cancellation signal) and streams
	// the request body directly without loading all data into memory first.
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), r.Body)
	if err != nil {
		return nil, err
	}

	// 4. Copy headers, then strip hop-by-hop ones in both directions.
	//
	// SIMPLE EXPLANATION: Clones client headers (keeping Auth, Content-Type, etc.) and
	// deletes single-hop connection headers that shouldn't cross the proxy boundary.
	outReq.Header = r.Header.Clone()
	removeHopByHopHeaders(outReq.Header)

	// 5. Append this hop to X-Forwarded-For rather than overwriting it,
	//    so a chain of proxies still preserves the original client IP.
	//
	// SIMPLE EXPLANATION: Preserves client IP history in X-Forwarded-For so the upstream
	// backend knows who originally sent the request.
	if clientIP := clientIPFromRemoteAddr(r.RemoteAddr); clientIP != "" {
		if prior := outReq.Header.Get("X-Forwarded-For"); prior != "" {
			outReq.Header.Set("X-Forwarded-For", prior+", "+clientIP)
		} else {
			outReq.Header.Set("X-Forwarded-For", clientIP)
		}
	}
	outReq.Header.Set("X-Forwarded-Host", r.Host)
	outReq.Header.Set("X-Forwarded-Proto", schemeOf(r))

	// 6. Host header: Go's transport uses outReq.Host (falling back to
	//    outReq.URL.Host) when writing the request line, so this must
	//    reflect the *upstream* host, not the client's original Host
	//    header — otherwise SNI/virtual-hosting on the upstream side
	//    breaks.
	//
	// SIMPLE EXPLANATION: Sets the Host header to match the backend destination host.
	outReq.Host = upstreamURL.Host

	return outReq, nil
}

// writeDownstreamResponse mirrors the upstream response back to the client:
// status line, headers (minus hop-by-hop), then body.
//
// The body copy uses io.Copy, which streams via a fixed-size internal
// buffer rather than reading the whole response into memory first. For a
// small JSON completion this distinction is invisible; for a streamed
// SSE response (Day 4) or a large document (Retriever, Week 5) it is the
// difference between O(1) and O(response size) memory per in-flight
// request.
//
// SIMPLE EXPLANATION: This function takes the upstream response, strips out hop-by-hop
// response headers, sends the HTTP status code, and streams the body bytes back to the client.
func (h *Handler) writeDownstreamResponse(w http.ResponseWriter, upstreamResp *http.Response) {
	destHeader := w.Header()
	for k, values := range upstreamResp.Header {
		for _, v := range values {
			destHeader.Add(k, v)
		}
	}
	removeHopByHopHeaders(destHeader)

	w.WriteHeader(upstreamResp.StatusCode)

	// Streams the payload in chunks using constant, minimal memory usage.
	if _, err := io.Copy(w, upstreamResp.Body); err != nil {
		// The client almost certainly disconnected mid-response.
		// Nothing to send an error page for at this point — the
		// headers are already written — so just log it.
		slog.Warn("proxy: error streaming response body to client", "error", err)
	}
}

// removeHopByHopHeaders strips the standard hop-by-hop set, plus any
// header names listed inside an existing Connection header value — per
// RFC 7230 §6.1, "Connection: X-Custom-Header" means X-Custom-Header is
// also hop-by-hop for this particular message.
//
// SIMPLE EXPLANATION: Looks for dynamically listed hop-by-hop headers inside the Connection header
// and removes them alongside the standard hop-by-hop headers.
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

// singleJoiningSlash joins a base path and a request path with exactly one
// slash between them, regardless of whether either side already has one.
// Lifted conceptually from net/http/httputil — it is small enough to be
// worth understanding rather than importing.
//
// SIMPLE EXPLANATION: Safely merges base path and target path so you don't get double slashes ("//")
// or missing slashes.
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

// clientIPFromRemoteAddr strips the port from r.RemoteAddr ("1.2.3.4:5678"
// -> "1.2.3.4"). RemoteAddr is always host:port for TCP connections, so a
// bare colon-split is sufficient here — no need for net.SplitHostPort's
// extra error handling in this context, but we guard against an empty
// result anyway rather than assume the format.
//
// SIMPLE EXPLANATION: Helper function to strip port numbers off an IP address string.
func clientIPFromRemoteAddr(remoteAddr string) string {
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		return remoteAddr[:idx]
	}
	return remoteAddr
}

// schemeOf checks whether the incoming client request is HTTP or HTTPS.
func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
