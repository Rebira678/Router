# Day 7 — Review Day: Fixing Week 1's Rough Edges

**Concept mastered:** none new — today is deliberately a review day, per
the roadmap. The skill being practiced is rereading your own code
critically enough to find real problems before someone else does.
**Built:** two fixes to `internal/ratelimit`, plus `WEEK1-DEEPDIVE.md`,
the published-ready recap post.

---

## 1. Rough edge #1 — Redis keys with no TTL (a slow memory leak)

**Where:** `internal/ratelimit/redis_limiter.go`, the final `pipe.Set(...)`
calls.

**Before:** `pipe.Set(ctx, tokensKey, tokens, 0)` — a TTL of `0` in
go-redis means "never expire." Every API key that ever made one request
— including throwaway, malicious, or scanning traffic using random fake
tokens — left two permanent Redis keys behind. Nothing ever cleaned them
up. Left running long enough against real internet traffic, this is
unbounded memory growth for no operational benefit.

**After:**
```go
ttl := time.Duration(2*l.capacity/l.refillRate*float64(time.Second))
pipe.Set(ctx, tokensKey, tokens, ttl)
pipe.Set(ctx, lastRefillKey, now.UnixNano(), ttl)
```

**Why this specific TTL, not just "pick a number":** `capacity /
refillRate` is exactly how long the bucket takes to go from empty back to
completely full. Doubling that as a safety margin means: if a tenant has
genuinely been silent longer than the TTL, their bucket would already be
back at full capacity anyway. So Redis deleting the key and the next
request falling back to `readFloat`'s default (a full bucket) produces
**the identical result** to the key never having expired at all. The fix
adds cleanup with zero risk of ever changing what a real request
experiences.

## 2. Rough edge #2 — raw credentials in Redis key names and logs

**Where:** `redis_limiter.go`'s key construction, and both `slog` calls in
`middleware.go`.

**Before:** `fmt.Sprintf("ratelimit:%s:tokens", key)` where `key` is the
literal `Authorization` header value. Anyone with `redis-cli` access
(`KEYS "*"`, `MONITOR`, the slow log) could read real bearer tokens in
plain text. Separately, `slog.Warn(..., "key", key)` put the same raw
token into every rejected-request log line.

**After:** a new shared helper,
```go
func hashIdentity(raw string) string {
    sum := sha256.Sum256([]byte(raw))
    return hex.EncodeToString(sum[:])[:16]
}
```
used both when building Redis key names and in the two `slog` calls that
previously logged the raw key. Same tenant always hashes to the same
fingerprint — so rate limiting and log correlation both still work
exactly as before — but the original credential is never recoverable
from anything Router writes to disk or exposes through an operational
tool.

## 3. Why these two and not something else

I specifically looked for things that are **invisible in a demo and real
in production** — the exact category of bug that's easy to ship
accidentally because everything still "works" while you're testing it
locally. Both fit:

- A memory leak that takes days of real traffic to matter at all.
- A credential-exposure issue that only shows up if someone actually
  looks at Redis directly or greps a log file — nothing about a normal
  `curl` test would ever reveal it.

That's worth naming explicitly, because it's the actual skill a review
day is meant to build: not finding syntax errors (the compiler does that
for free), but finding the class of mistake that compiles fine, runs
fine, and quietly costs you later.

## 4. Verifying the fix

```bash
go run ./cmd/router

# make one request as tenant-A, then inspect Redis directly:
curl -s http://localhost:8080/v1/chat/completions -H "Authorization: Bearer tenant-A" -o /dev/null

redis-cli KEYS "ratelimit:*"
# should show something like: ratelimit:3f9a2c88b1e04d5a:tokens
# — a hash, not the literal string "Bearer tenant-A"

redis-cli TTL "ratelimit:3f9a2c88b1e04d5a:tokens"
# should return a positive number of seconds, not -1 (which means "no TTL")
```

## 5. What to say out loud in 60 seconds

*"Reviewing Week 1, I found two things that looked completely fine in
testing but would have been real problems in production: Redis keys with
no expiration, meaning any traffic — including malicious scanning —
leaves permanent entries behind forever; and raw bearer tokens sitting in
plain text in both Redis key names and application logs. I fixed the
first with a TTL calculated from the bucket's own refill math, so
expiring a key can never change observable behavior. I fixed the second
by hashing the identity before it touches either Redis or logs, so the
same tenant is still recognized consistently, but the actual credential
is never stored or printed anywhere."*

## 6. What's still open going into Week 2

- The core race condition from Day 6 (GET-then-SET not being atomic) is
  **not** fixed today — that's explicitly Day 8 (prove it) and Day 9 (fix
  it with Lua). Today's fixes were real, but orthogonal to that one.
- `MemoryLimiter` doesn't get the same hashing treatment — a Go map isn't
  exposed to any external tool the way Redis is, so the risk that
  motivated today's fix doesn't apply there. Worth being able to explain
  *why* that asymmetry is fine if asked, rather than "fixing" something
  that isn't actually broken.