// Redis-backed implementation of the Limiter interface. This stores each
// tenant's bucket state as two keys in Redis instead of a Go map entry,
// so the limit is shared correctly across every Router process talking to
// the same Redis — not just correct within one process's memory.
//
// IMPORTANT, and this is today's whole lesson: this version reads the
// current state, does the token math in Go, then writes the new state
// back — as two separate round trips to Redis (GET, then SET). Between
// those two round trips, nothing stops a second goroutine — or a second
// Router *process* entirely — from doing the exact same GET and computing
// against the same stale numbers. That's a real race condition, left in
// on purpose. Day 8 writes a concurrent stress test that proves it happens
// in practice, not just in theory. Day 9 fixes it properly with a Lua
// script that makes the whole read-modify-write happen as one atomic
// operation on Redis's side. Today's job is just: get the state moved
// into shared storage, and be able to explain exactly where the crack is.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

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

	// --- ROUND TRIP 1: read current state ---
	tokens, err := l.readFloat(ctx, tokensKey, l.capacity)
	if err != nil {
		return false, fmt.Errorf("ratelimit: reading tokens: %w", err)
	}
	lastRefillNs, err := l.readInt(ctx, lastRefillKey, time.Now().UnixNano())
	if err != nil {
		return false, fmt.Errorf("ratelimit: reading last refill time: %w", err)
	}

	// --- Same math as Day 5's in-memory bucket, unchanged ---
	// This is the point worth noticing: the ALGORITHM didn't change at
	// all moving to Redis. Only where the numbers live changed.
	now := time.Now()
	elapsed := now.Sub(time.Unix(0, lastRefillNs)).Seconds()
	tokens += elapsed * l.refillRate
	if tokens > l.capacity {
		tokens = l.capacity
	}

	allowed := false
	if tokens >= 1 {
		tokens--
		allowed = true
	}

	// --- ROUND TRIP 2: write the new state back ---
	// THIS is the gap. Between the GET above and this SET, another
	// goroutine — or another Router process entirely — could have run
	// the exact same steps against the exact same starting numbers.
	// Nothing here detects or prevents that; it's simply not addressed
	// yet. A pipeline batches the two SETs into one network round trip
	// for efficiency, but that is NOT the same thing as making the
	// overall GET+GET+compute+SET+SET sequence atomic — it only saves a
	// network hop, it does not add any locking.
	//
	// Day 7 fix: both keys now carry a TTL instead of living forever.
	// The TTL isn't an arbitrary guess — it's set to a bit longer than
	// however long it takes this bucket to refill from completely empty
	// back to completely full (capacity / refillRate seconds), with a
	// 2x safety margin. That choice means expiry can never change
	// observable behavior: if a tenant has genuinely been silent longer
	// than that, their bucket would already be back at full capacity
	// anyway — so letting Redis delete the key and having the next
	// request fall back to readFloat's default (a full bucket) produces
	// the exact same result as if the key had never expired. Without
	// this, every API key that's ever made one request — including
	// abusive one-off scanning traffic — leaves two permanent entries in
	// Redis forever, a slow, silent memory leak.
	ttl := time.Duration(2 * l.capacity / l.refillRate * float64(time.Second))

	pipe := l.client.Pipeline()
	pipe.Set(ctx, tokensKey, tokens, ttl)
	pipe.Set(ctx, lastRefillKey, now.UnixNano(), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, fmt.Errorf("ratelimit: persisting bucket state: %w", err)
	}

	return allowed, nil
}

// readFloat fetches a float64 stored at key, or returns fallback if the
// key doesn't exist yet (a brand-new tenant's first-ever request).
func (l *RedisLimiter) readFloat(ctx context.Context, key string, fallback float64) (float64, error) {
	s, err := l.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

// readInt fetches an int64 stored at key, or returns fallback if unset.
func (l *RedisLimiter) readInt(ctx context.Context, key string, fallback int64) (int64, error) {
	s, err := l.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}
