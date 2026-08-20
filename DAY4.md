# Day 4 — Server-Sent Events (SSE) Passthrough

**Concept mastered:** how streaming HTTP responses work — SSE framing,
chunked transfer encoding, and why explicit flushing is required.
**Built:** a real SSE endpoint in `mockllm`, and a flush-per-chunk relay
path in `proxy.go` so Router forwards streamed tokens as they arrive
instead of buffering them.

---

## 1. What SSE actually is

Strip away the branding and SSE is: an ordinary HTTP response, with
`Content-Type: text/event-stream`, whose body is a sequence of small
text blocks in this exact shape:

```
data: {"choices":[{"delta":{"content":"This "}}]}

data: {"choices":[{"delta":{"content":"is "}}]}

data: [DONE]

```

Each event is `data: <payload>` followed by a **blank line** (`\n\n`).
That blank line is the entire framing mechanism — it's how a client tells
one event ends and the next begins. No length prefix, no binary framing,
nothing WebSocket-like. This simplicity is exactly why LLM providers chose
it: it's just HTTP, so every proxy, load balancer, and HTTP client that
already exists can carry it without being taught anything new about it —
**except** for the one thing most of them get wrong by default: buffering.

## 2. Why buffering breaks this, specifically

`net/http`'s `ResponseWriter`, on the server side, doesn't guarantee that
calling `Write()` puts bytes on the wire immediately. Internally, writes
can sit until either enough has accumulated or the handler returns. For a
JSON response written once and done, this is unobservable — by the time
you'd notice, the handler has already returned and everything flushed
out together anyway.

For SSE it's fatal to the entire feature: mockllm produces 11 tokens over
~1.65 seconds (150ms apart). Without forcing each one onto the wire as it's
written, a client would see *nothing* for 1.65 seconds, then all 11 tokens
appear simultaneously — technically "it worked," but it's indistinguishable
from not streaming at all from the user's point of view.

**`http.Flusher`** is the fix: `w.(http.Flusher)` type-asserts the
`ResponseWriter` to expose a `Flush()` method that forces buffered bytes
out immediately. Go's HTTP server implementation supports this — you just
have to call it, deliberately, after every write you want the client to
see right away.

## 3. Chunked transfer encoding, briefly

HTTP/1.1 responses normally declare their size up front via
`Content-Length`. That's impossible for SSE — Router has no idea how many
tokens the upstream will produce when the response starts. The answer is
**chunked transfer encoding**: instead of one `Content-Length`, the body is
sent as a series of `<size>\r\n<data>\r\n` chunks, ending in a zero-length
chunk. You don't write this framing yourself — Go's `net/http` does it
automatically the moment you write to a `ResponseWriter` without having
set `Content-Length`. This is *why* Day 4's code explicitly deletes any
`Content-Length` header the upstream might have sent (`proxy.go`,
`writeDownstreamResponse`): its presence would tell Go "use this exact
length," defeating the automatic switch to chunked encoding and producing
a response that's malformed relative to what's actually sent.

## 4. Reading the code, in order

1. **`mockllm/server.go` — the new `/v1/chat/completions/stream` handler.**
   Read top to bottom: it asserts `http.Flusher` support up front (fail
   fast if the connection can't stream at all), sets
   `Content-Type: text/event-stream`, writes headers, and — notice —
   calls `flusher.Flush()` **immediately after `WriteHeader`**, before any
   token exists. That pushes the response headers to the client right
   away, so a real client's SSE parser knows the stream has started
   instead of waiting to see the first byte of body.

2. **The token loop.** Each iteration: check `r.Context().Done()` (same
   cancellation discipline as Day 3 — don't keep producing tokens for a
   client that's gone), marshal one JSON chunk, write it in `data: ...\n\n`
   format, `Flush()`, then `time.Sleep(150ms)` to simulate real generation
   latency. The `[DONE]` sentinel at the end is a convention borrowed from
   OpenAI's API — not part of the SSE spec itself, but common enough that
   it's worth modeling.

3. **`proxy.go` — `writeDownstreamResponse`.** Now branches on
   `Content-Type`. Read the comment block above it — it explains *why*
   `io.Copy` (Day 1's original approach) was already fine for ordinary
   responses and specifically wrong for streaming ones, rather than
   assuming one approach should always win.

4. **`proxy.go` — `streamSSE`.** This is today's core logic. Read the
   doc comment on `io.Reader.Read` twice — the fact that `Read` returns as
   soon as *any* data is available (not only once its buffer is full) is
   what makes a 512-byte buffer safe to use here without artificially
   batching tokens together. The granularity you see on the client side is
   set by how the upstream flushed its writes, not by our buffer size.

## 5. Running & proving it streams — for real, not "trust me"

```bash
go run ./cmd/router
```

`curl`'s default behavior buffers output for pretty-printing, which would
hide exactly the thing you're trying to observe — use `-N` (`--no-buffer`)
to see it arrive live:

```bash
curl -N http://localhost:8080/v1/chat/completions/stream
```

Watch the terminal: `data: {...}` lines should print one at a time, with a
visible ~150ms pause between them — not all 11 appearing at once after a
1.65-second wait. **That visible pacing is your proof the flush is
working.** If you see one long pause followed by everything dumped
together, something upstream of `streamSSE` (a proxy layer, `curl` without
`-N`, or a misconfigured reverse proxy in front of all of this in a real
deployment) is buffering again.

**This is also today's LinkedIn/X asset** — screen-record this exact
terminal output. A GIF of real tokens landing one at a time, with visible
timing, is a far stronger post than a static screenshot, and it's honest:
it's really streaming, not simulated for the demo.

## 6. A limitation worth knowing about, not hiding

Day 3's `context.WithTimeout(r.Context(), h.upstreamTimeout)` wraps the
*entire* HTTP round trip — including reading a streaming body. Right now
`upstreamTimeout` is 5 seconds, and mockllm's stream takes ~1.65 seconds,
so it fits comfortably. But a real LLM completion can legitimately stream
for 30+ seconds. As written today, a single timeout value can't correctly
mean both "give up if the upstream never starts responding" (should be
short — a few seconds) and "give up if a legitimately-long generation is
still streaming" (should be much longer, or absent entirely). Conflating
those two into one deadline is a real gap, not a hypothetical one — it's
flagged here rather than fixed today because the correct fix (separate
"time to first byte" and "total stream duration" limits) deserves its own
deliberate design, not a rushed addition on top of an already-full day.
Keep this in mind for when Router talks to a real provider.

## 7. What to say out loud in 60 seconds

*"SSE is plain HTTP with Content-Type: text/event-stream and a simple
data: <payload> followed by a blank line as event framing — no upgrade,
no new protocol, which is exactly why it survives ordinary HTTP
infrastructure. The part that actually requires care is that Go's
ResponseWriter buffers writes by default, so io.Copy alone streams bytes
correctly at the network level but doesn't guarantee the client sees them
promptly. The fix is the http.Flusher interface — calling Flush() after
every write forces it onto the wire immediately, which is what turns
'technically streaming' into 'the user actually sees tokens appear one at
a time.' I also strip any Content-Length the upstream sends for a
streaming response, since Router can't know the total size in advance
either — deleting it lets Go correctly fall back to chunked transfer
encoding instead of producing a response whose framing lies about its own
length."*

## 8. What's deliberately not here yet

- No separate time-to-first-byte vs. total-stream-duration timeout — see
  §6 above.
- No handling of multi-line `data:` fields, `id:`, `retry:`, or other SSE
  spec features beyond the single-line `data:` case — sufficient for
  today's mock, not yet a fully spec-compliant SSE relay.
- No backpressure if the *client* reads slowly while tokens arrive
  quickly — Retriever's ingestion pipeline tackles backpressure properly
  in Week 6; Router doesn't need it as urgently since LLM generation is
  already the rate-limiting step in almost every real scenario.
- No metrics on active concurrent streams yet — Week 4 (Prometheus).