// Package ratelimit implements per-tenant rate limiting using the token
// bucket algorithm.
//
// Today's version keeps the bucket state in an in-memory Go map, protected
// by a mutex. That is intentional and temporary: Day 6 replaces this
// in-memory map with Redis, so the same rate limit applies across multiple
// Router instances instead of resetting every time a process restarts (and
// instead of each instance enforcing its own separate limit). The whole
// point of building it in memory first is that the *algorithm* — the part
// that's actually hard to get right — is identical either way. Swapping
// the storage backend tomorrow should not require re-deriving the bucket
// math.
package ratelimit

import (
	"sync"
	"time"
)

// TokenBucket holds the rate-limiting state for a single identity (in
// Router's case, one API key / tenant). It is not safe for zero-value use —
// always construct one through NewTokenBucket so tokens and lastRefill
// start in a consistent state.
type TokenBucket struct {
	mu sync.Mutex

	capacity   float64 // maximum tokens the bucket can ever hold (the burst allowance)
	tokens     float64 // tokens currently available
	refillRate float64 // tokens added per second, applied continuously — not in discrete steps
	lastRefill time.Time
}

// NewTokenBucket creates a bucket that starts completely full. Starting
// full — rather than empty — is a deliberate choice: a brand-new tenant
// (or a tenant whose bucket was just created after a process restart)
// should be able to make an immediate burst of requests up to capacity,
// not be throttled from the very first request just because their bucket
// record didn't exist a moment ago.
func NewTokenBucket(capacity, refillRate float64) *TokenBucket {
	return &TokenBucket{
		capacity:   capacity,
		tokens:     capacity,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// Allow reports whether a single request may proceed right now, and — if
// so — deducts one token from the bucket as a side effect. This is the
// entire rate-limiting decision, in one method call.
//
// The refill step is the part worth reading carefully: rather than running
// a background goroutine on a ticker to "add tokens every N milliseconds,"
// we calculate how much time has passed since the bucket was last touched
// and add the exact proportional number of tokens for that elapsed
// duration, right here, lazily, only when someone actually asks. This
// means a bucket nobody has touched in an hour costs nothing while idle —
// there's no timer running for every single tenant, active or not — and
// the math is exact regardless of how irregularly Allow() gets called.
//
// This method takes a lock for its entire body. For today's in-memory
// version that's correct and cheap — the critical section is a handful of
// floating-point operations. Day 9 revisits this exact problem in the
// Redis version, where two round trips (GET, then SET) would NOT be
// atomic across multiple Router instances the way this single mutex is
// atomic within one process.
func (b *TokenBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.lastRefill = now

	b.tokens += elapsed * b.refillRate
	if b.tokens > b.capacity {
		b.tokens = b.capacity // never let a long idle period bank more than capacity
	}

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}
