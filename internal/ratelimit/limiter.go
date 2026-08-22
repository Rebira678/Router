package ratelimit

import (
	"context"
	"sync"
)

// Limiter is the interface the middleware depends on — not a concrete
// storage backend. Day 5's in-memory version and today's Redis version
// both implement it, which is what lets main.go swap one for the other
// by changing a single line, without touching middleware.go at all.
//
// Allow now takes a context.Context and can return an error — neither of
// which the Day 5 signature needed. A plain in-memory map access can
// never fail. A network call to Redis absolutely can (timeout, Redis
// down, connection refused) — the interface has to account for the
// backend that's harder to talk to, even though today's MemoryLimiter
// still can't actually produce an error.
type Limiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

// MemoryLimiter is Day 5's implementation, renamed (was: Limiter) now that
// "Limiter" is the interface name instead. Nothing about how it works
// changed — only its name, and the shape of its Allow method so it
// satisfies the new interface.
type MemoryLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*TokenBucket
	capacity   float64
	refillRate float64
}

// NewMemoryLimiter builds an in-memory Limiter — kept around after Day 6
// on purpose. It's still genuinely useful: local development and unit
// tests shouldn't require a running Redis instance just to exercise
// Router's rate-limiting logic.
func NewMemoryLimiter(capacity, refillRate float64) *MemoryLimiter {
	return &MemoryLimiter{
		buckets:    make(map[string]*TokenBucket),
		capacity:   capacity,
		refillRate: refillRate,
	}
}

// Allow satisfies the Limiter interface. ctx is accepted but unused here —
// a map lookup can't be canceled or time out — it's part of the signature
// purely so MemoryLimiter and RedisLimiter are interchangeable.
func (l *MemoryLimiter) Allow(ctx context.Context, key string) (bool, error) {
	return l.getOrCreateBucket(key).Allow(), nil
}

func (l *MemoryLimiter) getOrCreateBucket(key string) *TokenBucket {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, exists := l.buckets[key]
	if !exists {
		b = NewTokenBucket(l.capacity, l.refillRate)
		l.buckets[key] = b
	}
	return b
}
