package circuitbreaker_test

import (
	"testing"
	"time"

	"github/rebik/internal/circuitbreaker"
)

// Benchmark the real production circuit breaker that we just upgraded
// to use the hybrid "atomic fast-path, mutex slow-path" pattern.
func BenchmarkProductionBreakerAllow(b *testing.B) {
	breaker := circuitbreaker.New(5, 10*time.Second)
	
	// Benchmark the "happy path" (closed state), which is 99.9% of traffic.
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			breaker.Allow()
		}
	})
}
