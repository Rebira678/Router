# Day 3 — Context Propagation & Cancellation

**Concept mastered:** `context.Context` — deadlines, cancellation, and how
they propagate (or fail to) through a call chain.
**Built:** a real per-request upstream timeout in `proxy.go`, status-code
differentiation between timeout/cancel/failure, a testable delay knob in
`mockllm`, and a matching cancellation-aware wait in `workerpool`.

---

## 1. What `context.Context` actually is

Three things, bundled into one interface, passed explicitly as the first
argument of any function that does I/O:

1. **A deadline** — "abort whatever you're doing after this instant."
2. **A cancellation signal** — a channel (`ctx.Done()`) that closes when
   the context is canceled, either by a deadline firing or by someone
   calling a `cancel()` function directly.
3. **Request-scoped values** (`ctx.Value`) — we don't use this today; it's
   for things like trace IDs, not covered until Week 8.

The critical mental model: **contexts form a tree.** `context.WithTimeout(parent, d)`
returns a *child* of `parent`. Canceling the parent cancels every child
transitively. A child's effective deadline is `min(parent's deadline, its own)`
— it can only make the deadline *stricter*, never looser. That's why layering
`h.upstreamTimeout` on top of `r.Context()` in `proxy.go` is safe: if some
future middleware sets an even tighter deadline upstream of us, ours never
overrides it to be more permissive.

## 2. Before vs. after, concretely

**Day 1/2:** `http.NewRequestWithContext(r.Context(), ...)`. This propagates
cancellation (if the client disconnects, the upstream call aborts) but sets
no deadline of its own. A slow-but-still-connected upstream can hang Router
for up to the 30s `http.Client.Timeout` backstop — a blunt global number,
not something you'd want to actually rely on as your real timeout policy.

**Day 3:** `context.WithTimeout(r.Context(), h.upstreamTimeout)` — now
there's an explicit, per-handler, tunable ceiling (5s in `main.go`), and
`http.Client.Do` will abort the call the instant it's exceeded, returning
an error that wraps `context.DeadlineExceeded`.

## 3. Reading the changes, in order

1. **`proxy.go` — `Handler.upstreamTimeout` field + `New()` signature.**
   The timeout is now a constructor parameter, not a hardcoded constant
   buried in the client — so it's configurable per-Handler instance
   (relevant once Week 3 gives you multiple upstream targets that might
   reasonably need different timeouts).

2. **`proxy.go` — `ServeHTTP`, the `context.WithTimeout` line.** Read the
   comment on `defer cancel()` carefully — this is the single most common
   context-related bug in real Go codebases: calling `WithTimeout` or
   `WithCancel` and never calling the returned `cancel` function. It leaks
   a timer and a small amount of internal state per call. `go vet` catches
   the *provably*-never-called case; it can't catch every case, so this is
   a discipline, not just a lint you can rely on blindly.

3. **`proxy.go` — the three-way `switch` in the error branch.** This is
   the part worth understanding cold:
   - `errors.Is(err, context.DeadlineExceeded)` → **our** timeout fired →
     `504 Gateway Timeout`.
   - `errors.Is(err, context.Canceled)` → the **client** hung up (their
     cancellation propagated down through `r.Context()`) → no response to
     send, log at Info, don't treat it as an error.
   - anything else → a real network/upstream failure → `502 Bad Gateway`,
     same as Day 1.

   Notice the pattern: `errors.Is`, not `err == context.DeadlineExceeded`.
   `http.Client.Do` wraps the underlying context error inside a `*url.Error`,
   so a direct equality check would silently never match. `errors.Is`
   unwraps the error chain to find it. This is *the* idiomatic way to check
   "is this error (or something it wraps) a specific sentinel error" in Go
   — worth internalizing now, you'll use it constantly.

4. **`proxy.go` — `buildUpstreamRequest`'s new `ctx context.Context`
   parameter.** One-line change in substance (`r.Context()` →
   caller-supplied `ctx`), but it's *the* change that makes the timeout
   real instead of decorative. Contexts are inert data structures — a
   `context.WithTimeout` that's never actually passed into the function
   doing the I/O does nothing at all. This is a common way people
   "add a timeout" and have it silently not work: they create the
   context, then keep using the old one by mistake three lines later.

5. **`mockllm/server.go` — the `X-Mock-Delay-Ms` header + `select`.**
   Lets you trigger an artificial slow response per-request via curl,
   without restarting anything. Notice it also selects on
   `r.Context().Done()` instead of an unconditional `time.Sleep` — a
   deliberate mirror of good upstream behavior: stop working the instant
   the caller has given up, don't burn CPU/time producing a response
   nobody will receive.

6. **`workerpool/pool.go` — the nested `select` while waiting on `j.done`.**
   This closes a gap Day 2 didn't yet handle: a request could be sitting
   in the queue, waiting for a free worker, while the client has already
   disconnected. Now the accepting goroutine stops waiting the instant
   `r.Context()` is canceled — instead of only reacting once a worker
   eventually gets to it. Read the comment there twice: **this does not
   stop the worker itself** once the job is dequeued — cancellation in Go
   is cooperative, not forceful. It only stops *this* goroutine from
   waiting pointlessly for a result nobody can receive. The worker still
   calls `proxy.ServeHTTP`, and it's `proxy.go`'s own context check (via
   `http.Client.Do`) that actually aborts the upstream call. Understanding
   that distinction — "cancellation propagates by convention, at every
   layer that chooses to check `ctx.Done()`, not automatically" — is the
   single most important idea in this entire day.

## 4. Proving hang vs. timeout, with real commands

```bash
go run ./cmd/router
```

**Baseline — fast, normal request:**

```bash
curl -i http://localhost:8080/v1/chat/completions -d '{"model":"mock-llm-v1"}'
```

Should return `200` almost instantly.

**Trigger the timeout on purpose** — ask the mock upstream to sleep 8
seconds, longer than Router's 5-second `upstreamTimeout`:

```bash
time curl -i http://localhost:8080/v1/chat/completions \
  -H "X-Mock-Delay-Ms: 8000" \
  -d '{"model":"mock-llm-v1"}'
```

Expected: the `time` output shows **~5 seconds**, not 8 — proof Router
aborted the wait rather than passing the hang straight through. The
response is `504 Gateway Timeout`, and Router's own log shows a `proxy:
upstream timed out` warning with `elapsed_ms` around 5000.

**Compare against a delay under the threshold** — should succeed slowly
but not time out:

```bash
time curl -i http://localhost:8080/v1/chat/completions \
  -H "X-Mock-Delay-Ms: 2000" \
  -d '{"model":"mock-llm-v1"}'
```

Expected: `200` after ~2 seconds.

**Prove client-cancellation is handled distinctly** — start a slow request
and kill curl before it finishes (Ctrl+C within ~2s of a `X-Mock-Delay-Ms:
8000` request). Router's log should show `proxy: client disconnected before
upstream responded` at Info level — not the timeout warning, not an error.

## 5. What to say out loud in 60 seconds

*"Day 1 and 2 passed r.Context() through to the upstream call, which
propagates cancellation but sets no deadline. Day 3 wraps it in
context.WithTimeout to add a real per-request ceiling — the child context
fires Done() at whichever comes first, our timeout or the parent's own
cancellation, and http.Client.Do watches that channel internally to abort
the in-flight call. The three outcomes — our timeout, client
disconnection, and genuine upstream failure — all surface as errors from
Do(), so I distinguish them with errors.Is against context.DeadlineExceeded
and context.Canceled, and map them to 504, no response, and 502
respectively. The one thing to always remember is that cancellation is
cooperative: closing a Done() channel doesn't kill a goroutine, it just
signals — every layer that wants to respect it has to explicitly check it,
which is why I had to fix the worker pool's queue-wait separately from the
proxy's upstream call, even though both are 'part of the same request'."*

## 6. What's deliberately not here yet

- No retry on timeout — a timed-out request just fails right now. Retry +
  exponential backoff is Day 15; doing it before understanding timeouts
  cold would just compound confusion.
- `upstreamTimeout` (5s) is a hardcoded guess in `main.go`, same caveat as
  Day 2's pool size — real numbers come from load testing later.
- No circuit breaker — a permanently-slow upstream will just keep timing
  out, one request at a time, forever. That's Day 16.