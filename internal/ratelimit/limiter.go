package ratelimit

import "sync"

// Limiter owns one independent TokenBucket per identity string (in
// practice, per API key) and creates a new one lazily the first time an
// identity is ever seen — there's no separate "register this tenant"
// step. This mirrors how real API gateways behave: a brand-new API key's
// first request should just work, not 404 because nobody pre-provisioned
// a bucket for it.
type Limiter struct {
	mu         sync.Mutex
	buckets    map[string]*TokenBucket
	capacity   float64
	refillRate float64
}

// NewLimiter builds a Limiter where every tenant gets the same capacity
// and refill rate. (Per-tenant custom limits — e.g. a paying customer
// getting a bigger bucket than a free-tier one — are a natural extension
// of this data model, but deliberately out of scope for Day 5: get one
// limit working correctly before making it configurable per tenant.)
func NewLimiter(capacity, refillRate float64) *Limiter {
	return &Limiter{
		buckets:    make(map[string]*TokenBucket),
		capacity:   capacity,
		refillRate: refillRate,
	}
}

// Allow reports whether the request identified by key may proceed. The
// first call for any given key allocates that tenant's bucket; every
// subsequent call for the same key reuses it.
func (l *Limiter) Allow(key string) bool {
	return l.getOrCreateBucket(key).Allow()
}

// getOrCreateBucket is the one place map access happens, so the map's own
// mutex discipline lives in exactly one function. Note carefully what this
// lock protects and what it does NOT protect: it guards the map (creating
// entries safely under concurrent access from many goroutines — remember,
// every request is its own goroutine), but it is released immediately
// after fetching or creating the bucket pointer. The actual token
// accounting happens inside TokenBucket.Allow(), under that bucket's OWN
// separate mutex. That split matters: if this function held the Limiter's
// lock for the whole Allow() call, every tenant in the system would
// serialize behind a single global lock — exactly the kind of unnecessary
// contention Day 2's worker pool was designed to avoid at a different
// layer. Splitting the locks means tenant A and tenant B's requests can be
// rate-limit-checked fully in parallel; they only briefly contend if they
// happen to hit getOrCreateBucket at the exact same nanosecond.
func (l *Limiter) getOrCreateBucket(key string) *TokenBucket {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, exists := l.buckets[key]
	if !exists {
		b = NewTokenBucket(l.capacity, l.refillRate)
		l.buckets[key] = b
	}
	return b
}
