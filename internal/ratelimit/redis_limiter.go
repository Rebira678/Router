// Redis-backed implementation of the Limiter interface. This stores each
// tenant's bucket state as two keys in Redis instead of a Go map entry,
// so the limit is shared correctly across every Router process talking to
// the same Redis — not just correct within one process's memory.
//
// IMPORTANT: On Day 6, this version read the current state, computed the
// math in Go, and wrote back as two separate round trips (GET then SET).
// That left a race condition where concurrent requests could read the same
// state before it was updated.
//
// Today (Day 8/9 combined), we fixed it by moving the entire read-modify-write
// sequence into a single Lua script. Redis executes Lua scripts atomically —
// no other command can run while the script is evaluating. This guarantees
// the GET and SET happen together without any gap for a race condition.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// luaRateLimit is the atomic script that replaces Day 6's GET-then-SET.
// Redis guarantees that no other command runs while a Lua script executes,
// which makes the entire read-compute-write sequence indivisible.
var luaRateLimit = redis.NewScript(`
	local tokensKey = KEYS[1]
	local lastRefillKey = KEYS[2]

	local capacity = tonumber(ARGV[1])
	local refillRate = tonumber(ARGV[2]) -- tokens per second
	local nowMicro = tonumber(ARGV[3])
	local ttl = tonumber(ARGV[4]) -- in seconds

	local tokens = tonumber(redis.call("GET", tokensKey))
	local lastRefillMicro = tonumber(redis.call("GET", lastRefillKey))

	if tokens == nil or lastRefillMicro == nil then
		tokens = capacity
		lastRefillMicro = nowMicro
	end

	local elapsedSec = (nowMicro - lastRefillMicro) / 1000000.0
	if elapsedSec > 0 then
		tokens = tokens + (elapsedSec * refillRate)
	end

	if tokens > capacity then
		tokens = capacity
	end

	local allowed = 0
	if tokens >= 1 then
		tokens = tokens - 1
		allowed = 1
	end

	-- Save the new state back
	redis.call("SET", tokensKey, tostring(tokens), "EX", ttl)
	redis.call("SET", lastRefillKey, tostring(nowMicro), "EX", ttl)

	return allowed
`)

// RedisLimiter implements Limiter using Redis as the shared store.
type RedisLimiter struct {
	client     *redis.Client
	capacity   float64
	refillRate float64
}

// NewRedisLimiter builds a Redis-backed Limiter. client is expected to
// already be configured and reachable — connection setup happens once in
// main.go, not per-request here.
func NewRedisLimiter(client *redis.Client, capacity, refillRate float64) *RedisLimiter {
	return &RedisLimiter{
		client:     client,
		capacity:   capacity,
		refillRate: refillRate,
	}
}

// Allow implements the Limiter interface against Redis.
//
// Each tenant's state lives under two keys rather than one, because Redis
// string values are just bytes — there's no built-in "struct with two
// fields" value type for the plain GET/SET commands used here. (A Redis
// Hash could hold both fields under one key instead; two separate string
// keys is the simpler thing to reach for first, and it's what makes the
// race condition below easy to see: it's two GETs and two SETs, not one.)
func (l *RedisLimiter) Allow(ctx context.Context, key string) (bool, error) {
	// Day 7 fix: hash the raw identity before it ever becomes part of a
	// Redis key name. Without this, a real bearer token or API key would
	// sit in plain text in every key name — visible to anyone running
	// `redis-cli KEYS "*"`, watching Redis's slow log, or looking at a
	// monitoring dashboard. The hash is deterministic (same tenant always
	// produces the same fingerprint, so rate limiting still works
	// correctly) but not reversible back to the original credential.
	identity := hashIdentity(key)
	tokensKey := fmt.Sprintf("ratelimit:%s:tokens", identity)
	lastRefillKey := fmt.Sprintf("ratelimit:%s:last_refill_ns", identity)

	// The TTL ensures keys don't leak forever. We calculate it in Go and
	// pass it to Lua.
	ttlSeconds := int(2 * l.capacity / l.refillRate)
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}

	// nowMicro uses microseconds. It fits perfectly into a Lua double-precision
	// float without losing precision (max Lua double exact integer is ~9 quadrillion,
	// current Unix micro is ~1.7 quadrillion).
	nowMicro := time.Now().UnixMicro()

	// --- ATOMIC EVALUATION ---
	// The entire algorithm now runs inside Redis via Lua.
	res, err := luaRateLimit.Run(ctx, l.client,
		[]string{tokensKey, lastRefillKey}, // KEYS
		l.capacity, l.refillRate, nowMicro, ttlSeconds, // ARGV
	).Result()

	if err != nil {
		return false, fmt.Errorf("ratelimit: lua script failed: %w", err)
	}

	return res.(int64) == 1, nil
}


