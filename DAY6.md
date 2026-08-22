# Day 6 — Redis as the Rate Limit Store

**Concept mastered:** why rate-limit state can't safely live in one
process's memory once you run more than one instance of Router — and what
"basic, not yet atomic" honestly means, including the bug it leaves in.
**Built:** `internal/ratelimit/redis_limiter.go` (new), plus a `Limiter`
interface introduced in `limiter.go` so `MemoryLimiter` (Day 5, renamed)
and `RedisLimiter` (today) are interchangeable from `middleware.go`'s
point of view.

---

## 1. The scenario: one bouncer's memory vs. a shared notebook

Day 5's bucket lived inside one Go process's memory — like a bouncer
keeping every customer's remaining tokens in his own head. Fine, as long
as there's exactly one bouncer.

The moment you run **two copies of Router** side by side (which you will,
for reliability — Week 4 puts this behind a load balancer), each copy has
its *own* separate memory. Tenant A could drain their 10 tokens against
copy 1, then immediately get 10 *more* fresh tokens hitting copy 2 —
because copy 2's map has never heard of tenant A. The limit becomes
theater, not enforcement.

The fix: move the numbers out of any one process's memory into **Redis** —
one shared notebook every copy of Router reads from and writes to.

## 2. What's genuinely new here vs. what's identical to Day 5

This is the single most important thing to understand about today:

**The algorithm did not change.** Refill math, capacity cap, "take 1 if
available" — identical to `tokenbucket.go`. Only *where the numbers are
stored* changed: a Go map field → two Redis keys.

```go
// Day 5 (in memory):
b.tokens += elapsed * b.refillRate

// Day 6 (Redis) — same line, just after reading/before writing to Redis:
tokens += elapsed * l.refillRate
```

If you can point at that and say "the math is the same, only the storage
changed," you understand today's lesson.

## 3. The bug we're leaving in, on purpose

`RedisLimiter.Allow` does exactly two things, as two **separate** network
round trips:

1. **GET** the current token count and last-refill time from Redis.
2. Do the math in Go.
3. **SET** the new numbers back to Redis.

Nothing stops a *second* request — running concurrently, maybe even on a
different Router process entirely — from doing step 1 against the exact
same starting numbers, **before** the first request finishes step 3.

Concretely: tenant A has exactly 1 token left. Two requests arrive at the
same instant, on two different Router instances:

- Both GET → both see `tokens = 1`
- Both compute → both decide "1 is enough, allow it, new value = 0"
- Both SET → Redis ends up with `tokens = 0`

**Result: 2 requests got through on a budget of 1 token.** The limiter
just failed at its one job, silently, with no error anywhere.

This is a genuine, real bug. It's left in deliberately because "read,
compute, write as three separate steps against shared state" is one of
the most common concurrency mistakes in backend engineering — recognizing
it is worth more than never having seen it. Day 8 writes a stress test
that fires many concurrent requests at once and counts how many actually
got through vs. how many *should* have — and proves this happens for
real, not just on paper. Day 9 fixes it with a Lua script that makes
Redis perform the whole read-modify-write as one atomic operation, so
there's no gap for a second request to land in.

## 4. Reading `redis_limiter.go`

- **Two Redis keys per tenant** — `ratelimit:<key>:tokens` and
  `ratelimit:<key>:last_refill_ns` — rather than one. Redis's plain GET/SET
  only stores simple values, not a whole struct, so the bucket's two
  fields become two keys. (A Redis Hash could hold both under one key —
  intentionally not used today, because two separate keys make the race
  condition above easier to see: it's visibly two GETs and two SETs, not
  hidden inside one call.)
- **`redis.Nil` handling** (`readFloat`/`readInt`) — this is Redis's way
  of saying "this key doesn't exist." For a brand-new tenant, that's
  expected and correct: fall back to a full bucket, exactly like Day 5's
  `NewTokenBucket` starting full.
- **The `Pipeline()` at the end** batches the two `SET` calls into one
  network round trip instead of two, for efficiency. This is easy to
  mistake for "atomicity" — it is not. A pipeline just reduces network
  hops; it does nothing to stop another goroutine's GET from having
  already happened moments earlier against stale data. Real atomicity
  (Day 9) requires the read-and-write to happen as a single operation
  Redis executes without interruption — a pipeline of independent
  commands doesn't provide that.

## 5. Reading the `Limiter` interface change

```go
type Limiter interface {
    Allow(ctx context.Context, key string) (bool, error)
}
```

Day 5's `Limiter` struct is renamed `MemoryLimiter`, and `Limiter` is now
this interface instead. Two things had to change in its `Allow` signature
to make this possible:

- **`ctx context.Context` added** — a map lookup can never fail or hang,
  but a Redis call can (network latency, timeout), so the interface has
  to accommodate the harder case even though `MemoryLimiter` ignores it.
- **`error` added to the return** — same reasoning. `MemoryLimiter.Allow`
  always returns `nil` for the error; `RedisLimiter.Allow` can return a
  real one.

`middleware.go` now depends only on the **interface**, not on which
concrete type it was handed — that's what let `main.go` swap
`ratelimit.NewLimiter(...)` for `ratelimit.NewRedisLimiter(...)` without
touching `middleware.go` at all.

## 6. The new design decision: what happens when Redis is down?

This didn't exist as a question on Day 5 — a Go map can't be "down."
Today it's real, and `middleware.go` makes a specific, defensible choice:
**fail open**. If `limiter.Allow` returns an error, the request is let
through anyway (loudly logged), rather than rejected.

The reasoning: rate limiting exists to protect the system. If the thing
protecting the system becomes the reason the system is unreachable, the
protection has inverted into the problem. A few minutes of unenforced
rate limits during a Redis blip is recoverable; a total outage because a
side-dependency hiccuped is a worse failure than the one you were
guarding against. (The opposite choice — fail closed — is legitimate too,
if the priority were "never let the limit be bypassed, even briefly." Both
are correct answers to different priorities — the point is having an
answer, not which one.)

## 7. Running it — requires actual Redis and actual internet access

This environment has neither, so this part is on your machine, not mine.
I reviewed this code by hand rather than compiling it — treat the following
as your verification step, not optional.

**Start Redis** (pick one):
```bash
docker run -d --name redis -p 6379:6379 redis:7-alpine
# or, if you have Redis installed natively:
redis-server --daemonize yes
```

**Fetch the new dependency and fix up go.sum:**
```bash
cd router-day1
go mod tidy
```

**Run Router as before:**
```bash
go run ./cmd/router
```

You should see `"redis: connected"` in the logs before `"router: listening"`.
If Redis isn't reachable, Router now exits immediately at startup with a
clear hint — rather than silently falling back to something that looks
like it's working but isn't.

**Prove the state is genuinely external to the process** — this is the
real payoff of today, and something Day 5's version could never do:

```bash
# burn tenant-A's bucket down
for i in $(seq 1 10); do
  curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer tenant-A"
done

# now restart Router (Ctrl+C, go run ./cmd/router again)
# then immediately try tenant-A again:
curl -i http://localhost:8080/v1/chat/completions -H "Authorization: Bearer tenant-A"
```

On Day 5, restarting Router would have reset tenant-A's bucket back to
full (it lived in memory that just got wiped). Today, it should **still
be rate-limited** immediately after restart — proof the state genuinely
survived outside the process, in Redis, exactly as intended.

You can also inspect the raw keys directly:
```bash
redis-cli GET ratelimit:"Bearer tenant-A":tokens
```

## 8. What to say out loud in 60 seconds

*"I moved the rate limiter's state out of Router's own memory and into
Redis, because with more than one Router instance running, in-memory
state means each instance enforces its own separate limit — a tenant
could get the full allowance from every instance simultaneously. The
algorithm itself didn't change at all, only where the numbers live. The
honest caveat is that this version reads the current count and writes it
back as two separate steps, which has a real race condition: two
concurrent requests can both read the same 'last token available' state
and both get allowed. I know that's there — I'm not fixing it today. A
stress test tomorrow proves it happens under real concurrency, and the
fix after that is an atomic Lua script so the read-and-write becomes one
indivisible operation on Redis's side."*

## 9. What's deliberately not here yet

- The race condition above — found Day 8, fixed Day 9.
- `MemoryLimiter` is still in the codebase, unused by `main.go` now, but
  kept — genuinely useful for tests and local dev without a Redis
  dependency.
- No connection pooling tuning, no Redis cluster/sentinel support — a
  single `redis.Client` against a single instance is enough to prove the
  concept; production Redis topology is its own separate concern.
- Fail-open on Redis errors is a real design decision, not a default —
  worth being able to argue the other side (fail-closed) if asked.