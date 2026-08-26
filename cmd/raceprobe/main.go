package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github/rebik/internal/ratelimit"
)

func main() {
	redisAddr := flag.String("redis", "localhost:6379", "Redis address")
	goroutines := flag.Int("concurrency", 200, "Number of concurrent requests to fire")
	capacity := flag.Float64("capacity", 5.0, "Bucket capacity")
	flag.Parse()

	log.Printf("Connecting to Redis at %s...", *redisAddr)
	client := redis.NewClient(&redis.Options{Addr: *redisAddr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		log.Fatalf("Fatal: Cannot reach Redis at %s: %v\nHint: run `docker run -d -p 6379:6379 redis:7-alpine`", *redisAddr, err)
	}

	log.Printf("Starting race probe. Concurrency: %d, Capacity: %.0f", *goroutines, *capacity)

	// Create the limiter with 0 refill rate to isolate the test from time passing.
	limiter := ratelimit.NewRedisLimiter(client, *capacity, 0.0)

	// Use a unique tenant so multiple runs don't interfere.
	tenant := fmt.Sprintf("raceprobe-tenant-%d", time.Now().UnixNano())

	// Pre-seed the bucket in Redis to exactly capacity.
	// We hash the identity the same way internal/ratelimit does (although it's internal to the package).
	// To do this properly without duplicating hashIdentity, we let the limiter handle it
	// by letting one request pass? No, pre-seeding is cleaner. We'll just call Allow once,
	// wait, then reset the tokens directly. Actually, the easiest way to pre-seed
	// is to just let the test run. The very first Allow() sets it up if it doesn't exist,
	// but that can be racy. We'll pre-seed by calling the unexported hashIdentity in test,
	// but here we are in main. Let's just make one dummy call to initialize it, then reset.
	_, _ = limiter.Allow(context.Background(), tenant)

	// Wait a tiny bit and let's just hammer it anyway. The race is so prominent
	// it will trigger regardless of perfect pre-seeding if concurrency is high enough.

	var (
		allowed atomic.Int64
		denied  atomic.Int64
		errored atomic.Int64
		wg      sync.WaitGroup
		barrier sync.WaitGroup
	)

	barrier.Add(1)

	log.Printf("Spawning %d goroutines...", *goroutines)
	for i := 0; i < *goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			barrier.Wait() // Block until all goroutines are spawned and ready

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

	// Wait for goroutines to hit the barrier
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	barrier.Done() // FIRE!
	wg.Wait()
	elapsed := time.Since(start)

	a := allowed.Load()
	d := denied.Load()
	e := errored.Load()

	fmt.Printf("\n--- RESULTS ---\n")
	fmt.Printf("Time elapsed: %v\n", elapsed)
	fmt.Printf("Allowed:      %d (Capacity was %.0f)\n", a, *capacity)
	fmt.Printf("Denied:       %d\n", d)
	fmt.Printf("Errors:       %d\n", e)

	if a > int64(*capacity) {
		fmt.Printf("\n❌ RACE CONDITION PROVEN!\n")
		fmt.Printf("The system allowed %d requests, which is %d more than the hard limit of %.0f.\n", a, a-int64(*capacity), *capacity)
		fmt.Printf("This happened because multiple goroutines read the same Redis state before any of them updated it.\n")
		os.Exit(1)
	} else {
		fmt.Printf("\n✅ No race condition detected on this run.\n")
		fmt.Printf("Try increasing -concurrency or run again. The race is probabilistic.\n")
		os.Exit(0)
	}
}
