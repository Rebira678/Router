// Race condition proof-of-concept for Day 8.
//
// This file contains two categories of tests:
//
// 1. TestRedisLimiter_RaceCondition — the core proof. It creates a
//    RedisLimiter with exactly N tokens, fires M >> N concurrent Allow()
//    calls, and counts how many were admitted. If the count exceeds N, the
//    race condition from Day 6 has been triggered: multiple goroutines all
//    read the same "tokens available" value before any of them wrote the
//    decremented value back, so they all got allowed on the same token.
//
// 2. TestMemoryLimiter_NoRace — a control test proving the in-memory
//    version (which uses a mutex) does NOT have this problem. Same
//    concurrency pattern, same assertion shape, opposite expected outcome.
//    This is important because it proves the test methodology itself is
//    sound — if the in-memory version also leaked tokens, the test would
//    be wrong, not the limiter.
//
// Both tests are designed to be run with -race (go test -race) as well,
// which detects unsynchronized memory access at the Go level. But the race
// flag alone is not sufficient here: the Redis race is a *logical* race
// (two separate network calls that should be atomic but aren't), not a
// data race on a Go variable, so the race detector won't flag it. The
// actual proof is the count exceeding capacity.
//
// REQUIREMENTS: a running Redis instance at localhost:6379 (or whatever
// REDIS_ADDR is set to). Tests that need Redis are gated behind a helper
// that skips if Redis is unreachable, so `go test ./...` in CI without
// Redis just reports "SKIP" rather than a misleading failure.
package ratelimit

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// ─────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────

// testRedisClient returns a connected redis.Client for testing, or calls
// t.Skip if Redis is not reachable. This keeps tests from failing in
// environments without Redis (CI, laptops on airplanes, etc.) while still
// running automatically when Redis IS available.
func testRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not reachable at %s (set REDIS_ADDR to override): %v", addr, err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// flushTestKeys removes all rate-limit keys matching the given tenant
// prefix so tests start from a clean slate. This is preferable to
// FLUSHDB because it leaves other keys (from other tests running in
// parallel, or from a developer's own session) untouched.
func flushTestKeys(t *testing.T, client *redis.Client, tenantPrefix string) {
	t.Helper()
	ctx := context.Background()
	identity := hashIdentity(tenantPrefix)
	keys := []string{
		fmt.Sprintf("ratelimit:%s:tokens", identity),
		fmt.Sprintf("ratelimit:%s:last_refill_ns", identity),
	}
	client.Del(ctx, keys...)
}

// ─────────────────────────────────────────────────────────────────────────
// Core proof: the Redis limiter's GET-then-SET has a real race condition
// ─────────────────────────────────────────────────────────────────────────

// TestRedisLimiter_RaceCondition is Day 8's deliverable. It proves, with
// real code against a real Redis instance, that the non-atomic
// read-modify-write in RedisLimiter.Allow lets more requests through than
// the bucket's capacity should allow.
//
// Methodology:
//   - Capacity = 5, refillRate = 0 (no refill during the test window).
//   - Launch 200 goroutines that all call Allow() as close to
//     simultaneously as possible (coordinated via a sync.WaitGroup acting
//     as a start barrier).
//   - Count how many got allowed=true.
//   - If count > 5, the race condition fired: multiple goroutines read
//     "tokens = X" before any of them wrote "tokens = X-1", so they all
//     consumed the same token independently.
//
// Why 200 goroutines, not 6?
//   The race is probabilistic — two calls have to hit the gap between
//   GET and SET on the same data. With only 6, the window is so narrow
//   you'd need to run the test hundreds of times to see one failure. 200
//   goroutines with a start barrier makes the collision near-certain on
//   the first run. The test runs multiple rounds to further increase
//   confidence.
//
// Why refillRate = 0?
//   With a non-zero refill rate, tokens regenerate continuously, which
//   would obscure the race: extra allowed requests could be from
//   legitimate refills rather than the bug. Setting refillRate to 0
//   eliminates that variable entirely — every token that gets used was
//   one of the original 5, period.
func TestRedisLimiter_RaceCondition(t *testing.T) {
	client := testRedisClient(t)

	const (
		capacity    = 5.0
		refillRate  = 0.0 // no refill — isolates the race from the refill logic
		goroutines  = 200
		rounds      = 5 // run multiple rounds; the race is probabilistic
		tenantBase  = "race-test-tenant"
	)

	raceTriggered := false

	for round := 0; round < rounds; round++ {
		// Unique tenant per round so previous rounds' leftover state
		// can't bleed into this one.
		tenant := fmt.Sprintf("%s-round-%d-%d", tenantBase, round, time.Now().UnixNano())
		flushTestKeys(t, client, tenant)

		limiter := NewRedisLimiter(client, capacity, refillRate)

		// Pre-seed the bucket so it starts at exactly `capacity` tokens.
		// Without this, the first Allow() call to a never-seen tenant
		// triggers the readFloat fallback (which returns capacity), and
		// concurrent first-calls could all get that fallback independently
		// — which is also a race, but a noisier one. Pre-seeding makes
		// the starting state deterministic.
		seedCtx := context.Background()
		identity := hashIdentity(tenant)
		tokensKey := fmt.Sprintf("ratelimit:%s:tokens", identity)
		lastRefillKey := fmt.Sprintf("ratelimit:%s:last_refill_ns", identity)
		client.Set(seedCtx, tokensKey, capacity, 0)
		client.Set(seedCtx, lastRefillKey, time.Now().UnixNano(), 0)

		var (
			allowed atomic.Int64
			denied  atomic.Int64
			errored atomic.Int64
			wg      sync.WaitGroup
			barrier sync.WaitGroup // release all goroutines at once
		)
		barrier.Add(1)

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				barrier.Wait() // block until all goroutines are ready

				ok, err := limiter.Allow(context.Background(), tenant)
				if err != nil {
					errored.Add(1)
					return
				}
				if ok {
					allowed.Add(1)
				} else {
					denied.Add(1)
				}
			}()
		}

		// Tiny sleep to let all goroutines reach the barrier.Wait() line.
		time.Sleep(10 * time.Millisecond)
		barrier.Done() // release the stampede
		wg.Wait()

		a := allowed.Load()
		d := denied.Load()
		e := errored.Load()

		t.Logf("Round %d: allowed=%d  denied=%d  errors=%d  (capacity=%.0f, goroutines=%d)",
			round, a, d, e, capacity, goroutines)

		if a > int64(capacity) {
			raceTriggered = true
			t.Logf("  ⚠ RACE DETECTED: %d requests allowed with capacity %.0f — %d extra got through",
				a, capacity, a-int64(capacity))
		}
	}

	// The race is probabilistic, but with 200 goroutines over 5 rounds
	// against a real Redis instance, it triggers reliably. If it somehow
	// didn't, log that — don't fail the test, because it's a proof of a
	// known bug, not a regression test for correct behavior.
	if !raceTriggered {
		t.Logf("NOTE: race condition was NOT triggered across %d rounds. "+
			"This can happen if Redis latency is unusually low (sub-microsecond, "+
			"e.g. local Unix socket) or the OS scheduler spread goroutines "+
			"out perfectly. Try increasing goroutines or running again.", rounds)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Control: the in-memory limiter does NOT have this race
// ─────────────────────────────────────────────────────────────────────────

// TestMemoryLimiter_NoRace is the control experiment. It applies the
// exact same concurrency pattern to MemoryLimiter (which uses a mutex),
// and asserts that the allowed count NEVER exceeds capacity. If this test
// ever fails, the test methodology is broken, not the limiter.
func TestMemoryLimiter_NoRace(t *testing.T) {
	const (
		capacity   = 5.0
		refillRate = 0.0
		goroutines = 200
		rounds     = 5
	)

	for round := 0; round < rounds; round++ {
		tenant := fmt.Sprintf("memory-race-test-round-%d", round)
		limiter := NewMemoryLimiter(capacity, refillRate)

		var (
			allowed atomic.Int64
			denied  atomic.Int64
			wg      sync.WaitGroup
			barrier sync.WaitGroup
		)
		barrier.Add(1)

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				barrier.Wait()

				ok, _ := limiter.Allow(context.Background(), tenant)
				if ok {
					allowed.Add(1)
				} else {
					denied.Add(1)
				}
			}()
		}

		time.Sleep(10 * time.Millisecond)
		barrier.Done()
		wg.Wait()

		a := allowed.Load()
		d := denied.Load()

		t.Logf("Round %d: allowed=%d  denied=%d  (capacity=%.0f, goroutines=%d)",
			round, a, d, capacity, goroutines)

		if a > int64(capacity) {
			t.Fatalf("MemoryLimiter leaked tokens! allowed=%d > capacity=%.0f — "+
				"this means the test methodology is broken, not the limiter", a, capacity)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Deep-dive: timing analysis of the race window
// ─────────────────────────────────────────────────────────────────────────

// TestRedisLimiter_RaceWindowTiming measures how long the vulnerable
// window (time between GET and SET in Allow) actually is. This isn't a
// pass/fail test — it's instrumentation that makes the race condition
// tangible: "the window where a second request can read stale data is
// approximately X microseconds" is much more useful for understanding
// the problem than just knowing the race exists in theory.
//
// This test makes 50 sequential Allow() calls and logs the per-call
// latency. The median latency approximates the duration of the
// GET+compute+SET sequence — which is also the duration of the race
// window. On a local Redis, this is typically 200-800µs; over a network,
// it can be several milliseconds, which makes the race proportionally
// easier to trigger.
func TestRedisLimiter_RaceWindowTiming(t *testing.T) {
	client := testRedisClient(t)

	const (
		calls    = 50
		capacity = 1000.0 // high capacity so we don't run out during measurement
		refill   = 0.0
	)

	tenant := fmt.Sprintf("timing-test-%d", time.Now().UnixNano())
	flushTestKeys(t, client, tenant)
	limiter := NewRedisLimiter(client, capacity, refill)

	// Pre-seed
	identity := hashIdentity(tenant)
	ctx := context.Background()
	client.Set(ctx, fmt.Sprintf("ratelimit:%s:tokens", identity), capacity, 0)
	client.Set(ctx, fmt.Sprintf("ratelimit:%s:last_refill_ns", identity), time.Now().UnixNano(), 0)

	durations := make([]time.Duration, calls)
	for i := 0; i < calls; i++ {
		start := time.Now()
		_, err := limiter.Allow(context.Background(), tenant)
		durations[i] = time.Since(start)
		if err != nil {
			t.Fatalf("Allow() returned error: %v", err)
		}
	}

	// Compute stats
	var total time.Duration
	min, max := durations[0], durations[0]
	for _, d := range durations {
		total += d
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	avg := total / time.Duration(calls)

	t.Logf("Allow() latency over %d calls:", calls)
	t.Logf("  min=%v  avg=%v  max=%v", min, avg, max)
	t.Logf("  → The race window (GET-to-SET gap) is approximately %v per call.", avg)
	t.Logf("    Any concurrent Allow() call that starts within this window")
	t.Logf("    reads the same pre-decrement token count and can be incorrectly allowed.")
}

// ─────────────────────────────────────────────────────────────────────────
// Multi-process simulation: proving the race across Router instances
// ─────────────────────────────────────────────────────────────────────────

// TestRedisLimiter_MultiInstanceRace simulates what happens when multiple
// Router processes share the same Redis. In production, this is the more
// dangerous variant of the race: it's not just goroutines within one
// process competing — it's completely separate processes, each with their
// own memory, their own network connection to Redis, hitting the same
// keys.
//
// We simulate this by creating multiple independent RedisLimiter
// instances (each with its own Redis client — simulating separate
// processes) and running concurrent Allow() calls across all of them
// against the same tenant key.
func TestRedisLimiter_MultiInstanceRace(t *testing.T) {
	// We need multiple Redis connections to simulate separate processes.
	// Reuse testRedisClient's logic but create multiple clients.
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	const (
		instances  = 5 // simulate 5 separate Router processes
		perInst    = 40
		capacity   = 5.0
		refillRate = 0.0
		rounds     = 3
	)

	// Verify Redis is reachable
	probe := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := probe.Ping(ctx).Err(); err != nil {
		cancel()
		probe.Close()
		t.Skipf("Redis not reachable at %s: %v", addr, err)
	}
	cancel()
	probe.Close()

	raceTriggered := false

	for round := 0; round < rounds; round++ {
		tenant := fmt.Sprintf("multi-instance-race-%d-%d", round, time.Now().UnixNano())

		// Create separate clients and limiters (simulating separate Router processes)
		clients := make([]*redis.Client, instances)
		limiters := make([]*RedisLimiter, instances)
		for i := 0; i < instances; i++ {
			clients[i] = redis.NewClient(&redis.Options{Addr: addr})
			limiters[i] = NewRedisLimiter(clients[i], capacity, refillRate)
		}

		// Pre-seed
		identity := hashIdentity(tenant)
		seedCtx := context.Background()
		clients[0].Set(seedCtx, fmt.Sprintf("ratelimit:%s:tokens", identity), capacity, 0)
		clients[0].Set(seedCtx, fmt.Sprintf("ratelimit:%s:last_refill_ns", identity), time.Now().UnixNano(), 0)

		var (
			allowed atomic.Int64
			denied  atomic.Int64
			errored atomic.Int64
			wg      sync.WaitGroup
			barrier sync.WaitGroup
		)
		barrier.Add(1)

		for inst := 0; inst < instances; inst++ {
			lim := limiters[inst]
			for j := 0; j < perInst; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					barrier.Wait()

					ok, err := lim.Allow(context.Background(), tenant)
					if err != nil {
						errored.Add(1)
						return
					}
					if ok {
						allowed.Add(1)
					} else {
						denied.Add(1)
					}
				}()
			}
		}

		time.Sleep(10 * time.Millisecond)
		barrier.Done()
		wg.Wait()

		a := allowed.Load()
		d := denied.Load()
		e := errored.Load()
		total := instances * perInst

		t.Logf("Round %d (%d instances × %d goroutines = %d total): allowed=%d  denied=%d  errors=%d",
			round, instances, perInst, total, a, d, e)

		if a > int64(capacity) {
			raceTriggered = true
			t.Logf("  ⚠ MULTI-INSTANCE RACE DETECTED: %d requests allowed with capacity %.0f",
				a, capacity)
		}

		// Cleanup
		for _, c := range clients {
			c.Close()
		}
	}

	if !raceTriggered {
		t.Logf("NOTE: multi-instance race was NOT triggered. This is less likely but possible.")
	}
}
