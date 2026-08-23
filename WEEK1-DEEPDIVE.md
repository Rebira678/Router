# Week 1: Building an AI Inference Gateway in Go — What I Learned Shipping a Rate Limiter Twice

I'm 7 days into a 60-day public build sprint: two production-grade Go
systems, documented daily. This post is the Week 1 recap for **Router**,
an AI inference gateway — the thing that sits between an app and an LLM
provider, handling rate limiting, streaming, and failover.

Here's what got built, why, and the two mistakes I found (and fixed) when
I actually went back and reread my own code with fresh eyes.

## Day 1 — A reverse proxy is not "call another server"

I could have reached for `net/http/httputil.ReverseProxy` and been done
in 8 lines. I wrote it by hand instead, because the whole point of Day 1
was understanding what a reverse proxy actually has to get right: stripping
hop-by-hop headers (`Connection`, `Transfer-Encoding`, `Upgrade`) that
belong to one TCP hop and must never leak to the next one, threading
`context.Context` through from the start even before anything used it,
and streaming both the request and response bodies instead of buffering
them in memory.

## Day 2 — Goroutines are free; unbounded upstream calls are not

`net/http` gives every connection its own goroutine automatically — that's
not something you add. What's dangerous is letting every one of those
goroutines call your upstream LLM provider at the same instant. A fixed
pool of worker goroutines pulling from a bounded, buffered channel turns
"unlimited concurrent upstream calls" into a hard, known ceiling — and a
clean `503` instead of an unbounded pile-up when that ceiling is hit.

## Day 3 — Cancellation is cooperative, not a kill switch

`context.WithTimeout` puts a real deadline on the upstream call — a slow
provider now gets cut off cleanly at 5 seconds with a `504`, instead of
hanging for as long as it wants. The part that actually surprised me: a
canceled context doesn't kill anything by itself. It just closes a
channel that well-behaved code has to explicitly check. I had to add that
check in *two* separate places — the proxy's upstream call, and the
worker pool's queue wait — because fixing it in one didn't fix the other.

## Day 4 — "Streaming" isn't automatic just because you used `io.Copy`

Go's `http.ResponseWriter` buffers writes internally. `io.Copy` moves
bytes correctly, but without an explicit `Flush()` after every chunk, an
SSE response can silently degrade into "wait, then dump everything at
once" — technically still streaming at the network level, completely
invisible to the person watching a blank screen. `http.Flusher` is the
whole fix.

## Day 5 — Rate limiting per tenant, not per gateway

A token bucket per API key, not one shared bucket for all traffic. Bucket
starts full (a burst allowance), refills continuously at a fixed rate (no
discrete "reset boundary" to game), and — critically — one noisy tenant
draining their own bucket has zero effect on anyone else's, because
they're genuinely separate objects behind a mutex-protected map.

## Day 6 — Moving state out of one process's memory

Run two copies of Router, and Day 5's in-memory bucket becomes a lie —
each instance enforces its own separate limit, so a tenant effectively
gets double the allowance. Moving the bucket state into Redis fixes that,
and the token bucket *math* didn't change one line — only where the
numbers physically live did. The honest cost: this version does a GET,
computes in Go, then does a SET, as two separate network round trips.
That gap is a real race condition, left in deliberately — the stress test
that proves it, and the atomic fix, are Days 8 and 9.

## Day 7 — What I found rereading my own code

Two real issues, once I stopped writing new code for a day and just
reread what I'd shipped:

**Redis keys never expired.** Every API key that ever made one request —
including one-off scanning traffic with random fake tokens — left two
permanent entries in Redis. Small in a demo, a real slow memory leak in
production. Fixed with a TTL tied to the bucket's own refill math: a
little more than the time it takes to refill from empty to full. That
choice means the TTL can never change observable behavior — if a tenant
has been quiet longer than that, their bucket would be full again anyway.

**Raw bearer tokens were leaking into Redis key names and application
logs.** `ratelimit:Bearer sk-abc123...:tokens` is a real credential
sitting in plain text, visible to anyone running `redis-cli KEYS "*"` or
grepping logs. Hashing the identity (SHA-256, truncated) before it
touches either one fixes it — same tenant still produces the same
fingerprint, so rate limiting still works exactly the same, but the
original token is never recoverable from what gets stored or logged.

Neither of these is a glamorous fix. Both are the kind of thing that
looks completely fine in a demo and becomes a real incident in
production, and I only caught them by deliberately setting aside a day to
reread instead of only ever moving forward.

## What's next

Week 2 goes deeper on the rate limiter's race condition: a stress test
that proves it under real concurrency, then a Lua script that closes the
gap for good with a truly atomic check-and-decrement. After that: JWT
auth, gRPC for internal tenant management, and the reliability work
(retries, circuit breakers, provider failover) that Week 3 is built
around.
