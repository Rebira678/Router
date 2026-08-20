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
	"context"
	"errors"
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
type Target struct {
	Name    string // human-readable label, used in logs
	BaseURL *url.URL
}

// Handler is the reverse proxy itself. It satisfies http.Handler directly
// rather than wrapping httputil.NewSingleHostReverseProxy, on purpose: the
// stdlib version is what you should reach for in production, but writing
// the forwarding logic by hand once is how "request forwarding at the
// transport level" stops being an abstraction you've read about.
type Handler struct {
	target Target
	client *http.Client

	// upstreamTimeout is Day 3's addition: a hard per-request ceiling on
	// how long Router will wait for the upstream to respond, enforced
	// via context — not via http.Client.Timeout (that one, set below,
	// stays as a coarse global backstop). This is the number you'd
	// actually tune per-provider in production; the client Timeout is
	// just "something is deeply wrong, abort no matter what."
	upstreamTimeout time.Duration
}

// New builds a Handler that forwards every request it receives to target,
// aborting the upstream call if it takes longer than upstreamTimeout.
func New(target Target, upstreamTimeout time.Duration) *Handler {
	return &Handler{
		target:          target,
		upstreamTimeout: upstreamTimeout,
		// A dedicated client (not http.DefaultClient) with its own
		// Transport means Router controls connection pooling and
		// timeouts independently of anything else in the process.
		// This 30s Timeout is deliberately looser than
		// upstreamTimeout — it exists only as a last-resort backstop
		// in case something bypasses the context deadline entirely
		// (e.g. a bug in a future retry wrapper). The context
		// deadline below is what actually governs normal operation.
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
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Day 3: derive a child context from r.Context() with our own
	// deadline layered on top. Per the WithTimeout contract, this
	// context's Done() fires at whichever comes first: our
	// upstreamTimeout from now, OR the parent's own deadline/
	// cancellation (e.g. the client disconnecting, or — from Day 2 —
	// nothing currently sets a parent deadline, but nothing has to for
	// this to be correct: WithTimeout still works fine against a parent
	// with no deadline at all, it just means ours is the binding one).
	//
	// defer cancel() is not optional. WithTimeout starts an internal
	// timer; if we never call cancel, that timer (and the small amount
	// of state behind this context) leaks until it fires on its own.
	// `go vet` will flag a WithTimeout/WithCancel whose cancel func is
	// provably never called — that lint exists because this leak is a
	// real, common bug, not a style nitpick.
	ctx, cancel := context.WithTimeout(r.Context(), h.upstreamTimeout)
	defer cancel()

	outReq, err := h.buildUpstreamRequest(ctx, r)
	if err != nil {
		slog.Error("proxy: failed to build upstream request", "error", err)
		http.Error(w, "bad gateway: could not construct upstream request", http.StatusBadGateway)
		return
	}

	upstreamResp, err := h.client.Do(outReq)
	if err != nil {
		elapsed := time.Since(start)
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			// Our own upstreamTimeout fired. This is the case Day
			// 3 exists to produce: a clean, fast, correctly-coded
			// failure instead of an indefinite hang. 504 Gateway
			// Timeout is the semantically correct status — it
			// means "I'm a proxy/gateway, and the thing I was
			// waiting on didn't respond in time," which is
			// exactly what happened.
			slog.Warn("proxy: upstream timed out",
				"target", h.target.Name,
				"timeout", h.upstreamTimeout,
				"elapsed_ms", elapsed.Milliseconds(),
			)
			http.Error(w, "gateway timeout: upstream did not respond in time", http.StatusGatewayTimeout)

		case errors.Is(err, context.Canceled):
			// The *client* disconnected before we got a response —
			// this fires when r.Context() (the parent) was
			// canceled, which happens automatically when the
			// client's TCP connection closes. There is no one left
			// to send a response to, so we don't try. This is also
			// why the log level is Info, not Error: a client
			// hanging up is normal internet behavior, not a bug in
			// Router.
			slog.Info("proxy: client disconnected before upstream responded",
				"target", h.target.Name,
				"elapsed_ms", elapsed.Milliseconds(),
			)

		default:
			// DNS failure, connection refused, TLS handshake
			// failure — a genuine upstream/network problem, not a
			// timeout or a cancellation.
			slog.Error("proxy: upstream request failed",
				"target", h.target.Name,
				"error", err,
				"elapsed_ms", elapsed.Milliseconds(),
			)
			http.Error(w, "bad gateway: upstream request failed", http.StatusBadGateway)
		}
		return
	}
	defer upstreamResp.Body.Close()

	h.writeDownstreamResponse(w, upstreamResp)

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
func (h *Handler) buildUpstreamRequest(ctx context.Context, r *http.Request) (*http.Request, error) {
	// 1. Rewrite the URL onto the target host, keeping the client's
	//    original path and query string. We resolve against BaseURL
	//    rather than string-concatenating so path joining (trailing
	//    slashes, etc.) follows normal URL semantics.
	upstreamURL := *h.target.BaseURL
	upstreamURL.Path = singleJoiningSlash(h.target.BaseURL.Path, r.URL.Path)
	upstreamURL.RawQuery = r.URL.RawQuery

	// 2. Build the new request with the caller-supplied ctx — the
	//    timeout-bound child context from ServeHTTP, not r.Context()
	//    directly. This is the one-line change that makes Day 3 real:
	//    on Day 1/2 this was r.Context(), which propagates
	//    cancellation but has no deadline of its own. Now it's a
	//    context that fires Done() at our chosen upstreamTimeout (or
	//    sooner, if the client disconnects first) — and because
	//    http.Client.Do watches ctx.Done() internally, that's all it
	//    takes for the timeout to actually abort the in-flight HTTP
	//    call, not just get ignored.
	//
	// 3. Pass r.Body straight through as an io.Reader rather than
	//    reading it into a []byte first. That keeps the request body
	//    streaming — a multi-megabyte upload is forwarded incrementally,
	//    not buffered twice in RAM. This matters even more once
	//    responses stream on Day 4, but the discipline starts here.
	outReq, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL.String(), r.Body)
	if err != nil {
		return nil, err
	}

	// 4. Copy headers, then strip hop-by-hop ones in both directions.
	outReq.Header = r.Header.Clone()
	removeHopByHopHeaders(outReq.Header)

	// 5. Append this hop to X-Forwarded-For rather than overwriting it,
	//    so a chain of proxies still preserves the original client IP.
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
	outReq.Host = upstreamURL.Host

	return outReq, nil
}

// writeDownstreamResponse mirrors the upstream response back to the client:
// status line, headers (minus hop-by-hop), then body.
//
// Day 4 splits the body-copy strategy in two, based on the upstream's
// Content-Type:
//
//   - Ordinary responses: io.Copy, unchanged since Day 1. It streams via
//     a fixed-size internal buffer rather than reading the whole body
//     into memory, which is already correct — the data reaches the
//     client progressively at the TCP level. What io.Copy does NOT do is
//     call Flush between reads, and for a fast, small JSON response
//     that's invisible: the whole thing is written and the handler
//     returns before buffering would even matter.
//
//   - text/event-stream responses: a hand-written loop that calls
//     Flush() after every single write. For SSE this distinction is the
//     entire feature — without an explicit Flush, Go's ResponseWriter is
//     free to hold written-but-unflushed bytes until either its internal
//     buffer fills or the handler returns, which for a slow, token-by-
//     token upstream would mean the client sees nothing for seconds,
//     then everything at once. That defeats the reason SSE exists.
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
		// A streaming body's true length is unknowable in advance —
		// if the upstream nonetheless sent a Content-Length (it
		// shouldn't, but "shouldn't" isn't a guarantee), forwarding
		// it verbatim would tell the client to expect exactly that
		// many bytes. Since we're about to stream an indeterminate
		// number of chunks instead, a stale Content-Length would
		// make well-behaved HTTP clients either truncate the stream
		// early or hang waiting for bytes that are never coming.
		// Deleting it lets Go's server fall back to chunked transfer
		// encoding, which is what a length-unknown-in-advance stream
		// is supposed to use.
		destHeader.Del("Content-Length")
	}

	w.WriteHeader(upstreamResp.StatusCode)

	if isEventStream {
		h.streamSSE(w, upstreamResp.Body)
		return
	}

	if _, err := io.Copy(w, upstreamResp.Body); err != nil {
		// The client almost certainly disconnected mid-response.
		// Nothing to send an error page for at this point — the
		// headers are already written — so just log it.
		slog.Warn("proxy: error streaming response body to client", "error", err)
	}
}

// streamSSE relays an SSE body to the client one read at a time, flushing
// after every write so each event reaches the client as soon as Router
// receives it — not batched, not delayed.
//
// A quiet but important detail: io.Reader.Read does NOT block until its
// buffer is full. Per the io.Reader contract, Read returns as soon as
// *some* data is available, even if that's fewer bytes than len(buf). So
// this loop's 512-byte buffer is a ceiling on how much we read per
// iteration, not a batching delay we're imposing — the real granularity
// of "how much arrives per iteration" is set by how the upstream flushed
// its own writes onto the TCP connection. Since mockllm flushes after
// every single token, each Read call here will typically return exactly
// one token's worth of JSON, not eleven of them merged together.
func (h *Handler) streamSSE(w http.ResponseWriter, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// No Flusher support on this connection for some reason —
		// fall back to a plain copy. The response will still
		// eventually arrive complete and correct, just not
		// incrementally. Better than failing the request outright.
		slog.Warn("proxy: ResponseWriter does not support Flusher; SSE response will not stream incrementally")
		_, _ = io.Copy(w, body)
		return
	}

	buf := make([]byte, 512)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				// Client disconnected mid-stream — completely
				// normal for SSE (a user closing a chat tab
				// mid-response, for instance). Nothing more to
				// do; the deferred upstreamResp.Body.Close() in
				// ServeHTTP will release the upstream connection.
				slog.Info("proxy: client disconnected mid-stream", "error", writeErr)
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				slog.Warn("proxy: error reading SSE body from upstream", "error", readErr)
			}
			return // io.EOF is the normal, successful end of stream
		}
	}
}

// removeHopByHopHeaders strips the standard hop-by-hop set, plus any
// header names listed inside an existing Connection header value — per
// RFC 7230 §6.1, "Connection: X-Custom-Header" means X-Custom-Header is
// also hop-by-hop for this particular message.
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
