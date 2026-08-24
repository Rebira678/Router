# Day 8 & 9 — Exposing and Fixing the Race Condition

**Concept mastered:** Race conditions in distributed counters, and using Lua scripting for Redis atomicity.
**Built:** 
1. `internal/ratelimit/race_test.go` and `cmd/raceprobe` (to prove the race condition).
2. Rewrote `internal/ratelimit/redis_limiter.go` to use a Lua script for an atomic check-and-decrement.

---

## 1. The Theory vs. The Reality (The Race Condition)

On Day 6, we migrated our rate limiter state from a local Go map to Redis. This solved the "multiple Router instances enforcing separate limits" problem. However, it introduced a new, subtler issue that only manifests under high concurrency.

The original `Allow()` logic worked in three distinct steps:
1. **GET**: Fetch current tokens and last refill time from Redis.
2. **Compute**: Calculate token consumption locally in Go.
3. **SET**: Write the new token count and time back to Redis.

Between step 1 and step 3, there is a gap (usually less than a millisecond). If a second request arrives during this gap, it will perform its GET against the exact same pre-decrement token count as the first request. Both requests conclude they have enough tokens, both decrement locally, and both write back. 

**Result:** Two requests were allowed, but only one token was technically consumed from Redis's perspective. The limiter leaked.

## 2. Proving It With Code

To make this undeniable, we built a tangible proof-of-concept. 

The tests in `internal/ratelimit/race_test.go` and the `cmd/raceprobe` CLI tool are designed to forcefully exploit this gap by spawning 200 goroutines behind a start barrier and firing them simultaneously at a bucket with a capacity of 5.

When run against the original GET-then-SET code, the output makes it clear why "close enough" doesn't work for distributed state:
```text
--- RESULTS ---
Allowed:      28 (Capacity was 5)
Denied:       172

❌ RACE CONDITION PROVEN!
```

## 3. The Fix: Lua Script Atomicity

To fix this, we need the entire read-compute-write sequence to happen indivisibly. Redis doesn't have a built-in "token bucket" command, but it *does* support executing custom Lua scripts. 

**The golden rule of Redis Lua scripts:** Redis guarantees that no other command runs while a Lua script is evaluating.

We rewrote `internal/ratelimit/redis_limiter.go` to push the math into a Lua script:
```lua
local tokens = tonumber(redis.call("GET", tokensKey))
local lastRefillMicro = tonumber(redis.call("GET", lastRefillKey))

-- (Math and refill logic happens here...)

if tokens >= 1 then
    tokens = tokens - 1
    allowed = 1
end

redis.call("SET", tokensKey, tostring(tokens), "EX", ttl)
redis.call("SET", lastRefillKey, tostring(nowMicro), "EX", ttl)
```

Now, instead of two round trips to Redis, Go makes a single `EVAL` call. Because the script is atomic, it is mathematically impossible for two requests to read the same pre-decrement token count. 

Running `cmd/raceprobe` after this change yields a perfect result every time:
```text
--- RESULTS ---
Allowed:      5 (Capacity was 5)
Denied:       195

✅ No race condition detected.
```

## 4. What to say out loud in 60 seconds

*"I built a concurrent stress test to prove a race condition in my rate limiter. Because the limiter read state from Redis and wrote it back as two separate network calls, a spike of concurrent requests would read the same 'tokens available' count before any wrote the decremented value back, leaking requests. I fixed this by rewriting the read-modify-write logic into a Lua script. Redis evaluates Lua scripts atomically, so the entire sequence now executes as one indivisible operation, completely eliminating the race condition."*

---

## LinkedIn Post

**Draft:**
I deliberately left a race condition in my AI inference gateway's rate limiter. Today, I built the benchmark to exploit it—and then fixed it.

Because my Go service originally read tokens from Redis and wrote them back as two separate network calls (GET then SET), there was a sub-millisecond gap. If multiple requests hit that gap simultaneously, they all read the same token count, and the limiter failed open. 

To prove it, I wrote a test suite that fired 200 concurrent requests at a 5-token bucket. The result? 28 allowed requests. A 460% leak.

The fix? I pushed the math down into Redis itself using a Lua script. Because Redis guarantees atomic evaluation of Lua scripts, the entire read-compute-write sequence became indivisible. 

Before: 2 Redis calls, race condition under load.
After: 1 atomic Lua call, mathematically sound. 

Code is live in the repo!

## X (Twitter) Post

**Draft:**
Day 8 & 9 of building an AI inference gateway in Go:

Bug: Redis GET-then-SET rate limiter leaks tokens under load. 
Proof: Fired 200 concurrent requests at a 5-token bucket. 28 got through. 
Fix: Rewrote the logic into a Redis Lua script. 

Before: 2 Redis calls (racy)
After: 1 atomic Lua call (perfectly strict)
