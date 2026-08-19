# Day 2 — Goroutines, Channels & the Bounded Worker Pool

**Concept mastered:** unbuffered vs buffered channels, and why an unbounded
number of concurrent upstream calls is a real production hazard, not
theoretical.
**Built:** `internal/workerpool` — bounded-concurrency middleware, wired in
front of Day 1's proxy in `main.go`.

---

## 1. The problem, precisely

Day 1's proxy is *correct* — every request forwards properly. It is not
*safe under load*. Here's why: `net/http.Server` spins up a new goroutine
per accepted connection automatically. That's a language/runtime feature,
not something in your code. So if 5,000 clients hit Router in the same
second, you already have ~5,000 goroutines, and — with nothing in front of
`proxyHandler` — every single one calls `h.client.Do()` against your
upstream LLM provider at the same moment.

That's a self-inflicted DDoS on your own dependency. Three concrete
failure modes follow from it:

1. **Upstream overload** — the LLM provider starts rate-limiting or
   erroring you back, at the worst possible time (peak load).
2. **File descriptor / connection exhaustion** — each concurrent outbound
   call holds a socket. OS limits are finite.
3. **No latency ceiling** — with no cap, p99 latency degrades
   unpredictably as everything contends for the same upstream capacity,
   instead of degrading predictably (some requests wait in a queue, some
   get a fast, clean 503).

Goroutines themselves aren't the expensive part — they're genuinely cheap
(~2KB starting stack). The upstream call each one triggers is what's
expensive, and unmetered.

## 2. Channels: the two shapes, and why the difference matters here

```go
unbuffered := make(chan int)      // capacity 0
buffered   := make(chan int, 50)  // capacity 50
```

**Unbuffered** — a send (`ch <- v`) and a receive (`<-ch`) are a single
atomic rendezvous. The sender blocks until a receiver is *actively
waiting, right now*. There is no queue — capacity is zero by definition.

**Buffered** — a send succeeds immediately as long as there's a free slot
in the buffer, whether or not a receiver is ready. It only blocks once the
buffer is full.

For a worker pool, this choice directly *is* your backpressure policy:

| Choice | Behavior under burst | When it's right |
|---|---|---|
| `queueSize = 0` (unbuffered) | Reject the instant no worker is free — zero tolerance for bursts | Extremely strict SLAs, or upstream that truly cannot absorb any queueing |
| `queueSize = N` (small buffer) | Absorb short bursts up to N requests, then reject | Most real services — smooths jitter without hiding sustained overload |
| unbounded queue (`make(chan job, 1_000_000)` or similar) | Never rejects, just gets slower and slower | Almost never correct — this just delays the failure and turns it into an OOM instead of a clean 503 |

We chose `workers=20, queueSize=50` in `main.go` — a small deliberate
buffer, not zero and not unbounded.

## 3. Reading `workerpool/pool.go`

Read it in this order:

1. **`job` struct** — what gets handed from the accepting goroutine to a
   worker goroutine. Note the `done chan struct{}`: an unbuffered channel
   used purely as a signal, never to carry a value. Closing it (rather
   than sending on it) is the idiomatic Go way to broadcast "finished" —
   a closed channel can be received from by any number of goroutines
   without panicking, whereas a second send on an already-signaled
   channel would.

2. **`New()`** — starts exactly `workers` goroutines, once, at startup.
   This count never changes at runtime in this version — it's a fixed
   pool, not an auto-scaling one. That's intentional simplicity for Day 2.

3. **`runWorker()`** — `for j := range p.jobs` is a worker pulling jobs
   off the shared channel forever. Multiple workers ranging over the same
   channel is safe and is in fact the whole mechanism — Go's channel
   implementation guarantees each value sent is delivered to exactly one
   receiver, so 20 workers ranging over one channel naturally load-balance
   without any extra coordination code.

4. **`ServeHTTP()`** — this is the part worth re-reading twice. The
   `select { case p.jobs <- j: ... default: ... }` is a **non-blocking
   send**. Without the `default` case, `p.jobs <- j` would block the
   calling goroutine until there's room — meaning a full queue would just
   cause new request-goroutines to pile up blocked on the channel send
   instead of being bounded. The `default` branch is what turns "wait
   forever" into "fail fast, right now, with a clean 503."

## 4. Why `http.ResponseWriter` can safely cross goroutines here

This is the subtle correctness point in the whole file. Normally, sharing
a `ResponseWriter` across goroutines is a race condition waiting to
happen. It's safe here specifically because of the handoff discipline:

- The accepting goroutine (`ServeHTTP`) does **nothing** to `w` after
  submitting the job — it only blocks on `<-j.done`.
- The worker goroutine is the **only** one that touches `w`, and it does
  so for the full duration of `p.next.ServeHTTP(j.w, j.r)`.
- `close(j.done)` happens *after* the worker is finished with `w`, and
  the accepting goroutine only proceeds (returns from `ServeHTTP`, which
  lets `net/http` close out the connection) *after* that close is
  observed.

That ordering — worker finishes with `w`, *then* signals — is what
prevents the race. If the signal happened before the worker was done
writing, you'd have two goroutines touching `w` in an unsynchronized
window.

## 5. Running & proving it works

```bash
go run ./cmd/router
```

Normal request — should behave exactly like Day 1, just routed through
the pool now:

```bash
curl -i http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-test-123" \
  -d '{"model":"mock-llm-v1"}'
```

**Prove the bound exists** — fire more concurrent requests than
`workers + queueSize` (20 + 50 = 70) at once and confirm you get some
`503 Service Unavailable` responses instead of everything just queueing
forever:

```bash
# requires `hey` (go install github.com/rakyll/hey@latest) — or swap for
# any concurrent load tool you have; ab / vegeta work too
hey -n 500 -c 200 \
  -H "Authorization: Bearer sk-test-123" \
  -d '{"model":"mock-llm-v1"}' \
  http://localhost:8080/v1/chat/completions
```

Check the status code distribution in `hey`'s output — you should see a
mix of `200` and `503`, and Router's own logs will show `workerpool:
queue full, rejecting request` entries. That log line appearing at all is
your proof the bound is real, not just a comment in the code.

## 6. What to say out loud in 60 seconds

*"net/http already gives every connection its own goroutine — that's not
something I add. What's unbounded without a worker pool is how many of
those goroutines can call the upstream LLM provider at the same instant.
I fixed that with a fixed number of worker goroutines pulling jobs off a
buffered channel. The buffer size is a deliberate backpressure choice: too
small and you reject bursts you could have absorbed, too large — or
unbounded — and you just delay an overload into an out-of-memory crash
instead of failing fast with a 503. The non-blocking select with a default
case is what makes 'queue full' an immediate, cheap decision instead of
every excess request's goroutine piling up blocked on a channel send."*

## 7. What's deliberately not here yet

- Pool size (20) and queue size (50) are hardcoded guesses — informed by
  nothing yet. Real numbers come from Week 7's load testing.
- No `context` deadline wired through the pool itself yet — a request
  queued behind a full pool will wait indefinitely for a worker even if
  the client gave up. That's tomorrow: Day 3 wires context cancellation
  through both the queue wait and the upstream call.
- No per-tenant fairness — one noisy client can fill the whole queue.
  Per-tenant rate limiting is Week 2.