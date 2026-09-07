# Day 24 — Goroutine Leak Hunting with pprof

**Concept mastered:** how to find a real, running goroutine leak using
Go's built-in pprof profiler, and the specific coding discipline (defer
right next to resource creation) that prevents this whole class of bug.
**Built:** a deliberate leak in `streamSSE`, pprof wired onto the
existing telemetry port, then the fix.

---

## 1. The scenario

A background employee checking in on a customer every few minutes, with
no instructions for what to do if the customer leaves early — they just
keep checking forever. A goroutine leak is exactly this: a background
goroutine whose only exit condition depends on something that doesn't
always happen.

## 2. The bug, precisely

```go
stop := make(chan struct{})
go func() {
    for {
        select {
        case <-ticker.C: /* heartbeat */
        case <-stop:      return
        }
    }
}()
...
if writeErr != nil {
    return          // stop never closed on THIS path
}
...
close(stop)          // only closed HERE
```

Two exit paths exist. Only one closes `stop`. The other — a client
disconnecting mid-stream, a completely normal, frequent SSE event —
leaks the heartbeat goroutine permanently. It costs nothing per-tick
(parked on a select), but it never gets collected either. Multiply by
every client disconnect over the process's lifetime.

## 3. Proving it with pprof — baseline

```bash
go run ./cmd/router
curl -s "http://localhost:9095/debug/pprof/goroutine?debug=1" | head -5
```
Note the goroutine count (e.g. "goroutine profile: total 24"). This is
your baseline.

## 4. Triggering the leak on purpose

```bash
for i in $(seq 1 20); do
  curl -s -m 1 http://localhost:8081/v1/chat/completions/stream \
    -H "Authorization: Bearer <your-jwt>" > /dev/null
done
```
`-m 1` kills the connection after 1 second — mockllm's stream takes
~1.65s total, so this reliably disconnects mid-stream, not at a clean
end.

## 5. Proving the leak — after

```bash
curl -s "http://localhost:9095/debug/pprof/goroutine?debug=1" | head -5
```
Total should now read roughly 20 higher than baseline. For the smoking
gun:
```bash
curl -s "http://localhost:9095/debug/pprof/goroutine?debug=2" \
  | grep -A 8 "proxy.*streamSSE"
```
You should see ~20 identical stack traces, all parked at the exact
select line inside the heartbeat goroutine.

## 6. The fix

Replace the manual `close(stop)` at one call site with `defer
close(stop)` placed immediately after `stop`'s creation. Restart, rerun
steps 3-5 — goroutine count should return to baseline, because defer
guarantees cleanup fires no matter which return executes.

## 7. The generalizable lesson

Any time you spawn a goroutine with a "stop" signal, put the "tell it to
stop" call immediately next to where you created the channel, via defer
— not at whichever single exit point you happened to be looking at when
you wrote the function. A function with two returns needs cleanup at
both, or one defer covering both automatically — the second option
doesn't get forgotten when a third return gets added six months later.

## 8. What to say out loud in 60 seconds

*"I deliberately introduced a goroutine leak — a heartbeat goroutine only
signaled to stop on the clean end-of-stream path, not on client
disconnect, which is actually the more common real-world event. I proved
it with pprof's goroutine endpoint: baseline count, 20 forced mid-stream
disconnects, confirmed the count rose by exactly 20, all parked at the
same line. The fix was moving from a manual close() at one exit point to
defer close() right next to the channel's creation — that guarantees
cleanup regardless of how many return paths exist, including ones added
later."*

## 9. What's deliberately not here yet

- pprof's endpoints have zero authentication — same trust-boundary
  discussion as Day 13's gRPC admin API. This port must never be public.
- This was one specific, planted leak. A real leak hunt often starts with
  "goroutine count keeps climbing" and no idea where — today's exercise
  is the tool, not a guarantee of always knowing where to look.