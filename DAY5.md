# Day 5 — Token Bucket Rate Limiter (data model)

**Concept mastered:** the token bucket algorithm — why it's the right shape
for rate limiting, and why "one bucket per tenant" is a distinct design
decision from "one bucket total."
**Built:** `internal/ratelimit` — `TokenBucket`, `Limiter` (per-key
registry), and HTTP middleware, wired outside the Day 2 worker pool in
`main.go`.

---

## 1. The scenario, in plain English

A club bouncer holds **your own personal bucket of tokens** — not a shared
one for the whole line. Every person who wants in hands over 1 token. The
bucket starts full (say, 10 tokens) so the first 10 people from *you* get
in instantly — a **burst allowance**. After that, tokens drip back in
slowly (2 per second) — so you're not blocked forever, just throttled to
a sustainable rate.

Two properties fall directly out of that scenario:

- **Bursts are allowed, but bounded.** Real traffic is bursty — a client
  legitimately fires 8 requests in the same second sometimes. A token
  bucket accommodates that, up to `capacity`, without needing special
  cases.
- **The long-run rate is still capped.** Once the burst is spent, you're
  limited to `refillRate` requests/sec, indefinitely.

## 2. Why not just count requests per second?

A naive "reset a counter every second" limiter has a real seam: nothing
stops 2 requests landing at `0.999s` and 2 more at `1.001s` — 4 requests
in 2 milliseconds, each half technically inside its own second's limit.
Token bucket has no such seam because refilling is continuous math based
on elapsed time, not a discrete reset — there's no boundary to game.

## 3. Reading `tokenbucket.go`

The one thing worth reading twice is `Allow()`'s refill step:

```go
elapsed := now.Sub(b.lastRefill).Seconds()
b.tokens += elapsed * b.refillRate
```

This is **lazy, exact refilling** — there's no background goroutine
ticking every N milliseconds adding tokens to every bucket whether it's
being used or not. Instead, the bucket only "catches up" the instant
someone calls `Allow()`, based on exactly how much wall-clock time passed.
A tenant who hasn't made a request in an hour costs nothing while idle,
and the math is correct regardless of how irregularly `Allow()` gets
called — there's no polling interval to be "off" by.

## 4. Reading `limiter.go` — the per-tenant part

`Limiter` is a `map[string]*TokenBucket` — one bucket per API key,
created lazily on first use. The subtle design point is **where the
lock lives**:

- `Limiter.mu` protects the *map* — safe concurrent creation of new
  tenant buckets.
- Each `TokenBucket.mu` protects *that bucket's own* token count.
- `getOrCreateBucket` releases the map lock immediately after fetching
  the pointer — it does NOT hold it while `Allow()` runs.

That split means tenant A and tenant B's rate-limit checks run in **true
parallel** — they only ever briefly contend if they happen to request a
brand-new bucket at the exact same instant. If `Limiter.mu` were held for
the whole `Allow()` call instead, every tenant in the system would
serialize behind one global lock — exactly the unnecessary contention
Day 2's worker pool was built to avoid, just reintroduced one layer up.

## 5. Where the middleware sits — and why that placement is deliberate

```
Client → ratelimit.Middleware → workerpool.Pool → proxy.Handler → upstream
```

Rate limiting is **outside** the worker pool, not inside it. A request
that's already over its own tenant's limit gets rejected before it takes
up a worker-pool queue slot. If it sat inside the pool instead, one
tenant hammering past their limit could still fill the shared queue with
requests that were always going to be rejected anyway — starving other
tenants of queue space for no reason.

## 6. Running & proving per-tenant isolation

```bash
go run ./cmd/router
```

Burn through tenant A's bucket (capacity 10) fast:

```bash
for i in $(seq 1 12); do
  curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer tenant-A"
done
```

Expect **ten `200`s, then `429`s** — the 11th and 12th should be rejected
immediately, not queued.

Now, in the same terminal, prove tenant B is untouched:

```bash
curl -i http://localhost:8080/v1/chat/completions -H "Authorization: Bearer tenant-B"
```

This should return a clean `200` even though tenant A is currently rate
limited — proof the buckets are genuinely independent, not a shared pool
with a label on it.

## 7. What to say out loud in 60 seconds

*"A token bucket holds up to N tokens, starts full so new clients can
burst immediately, and refills continuously at a fixed rate rather than
on a discrete timer — so there's no reset-boundary trick to exploit. I
gave every API key its own bucket instead of one global one, because
without that, one noisy tenant could throttle every other tenant just by
using up a shared allowance. The lock discipline matters too: the
registry's own lock only protects bucket creation, not the token
accounting inside each bucket, so different tenants' rate-limit checks
never block each other."*

## 8. What's deliberately not here yet

- Storage is an in-memory Go map — resets on restart, and doesn't share
  state across multiple Router instances. Day 6 replaces this with
  Redis, keeping the exact same algorithm.
- The GET-then-SET-style update inside `Allow()` is safe today only
  because one process's mutex guards it — that guarantee disappears the
  moment two instances share Redis as the backing store, which is
  exactly the race condition Day 8 finds and Day 9's Lua script fixes.
- `KeyFromAuthHeader` is a placeholder identity extractor — real
  authenticated tenant IDs arrive with Day 12's JWT/API-key validation.
- All tenants share one capacity/refill rate. Per-tenant custom limits
  are a natural extension of this same data model, just not built yet.