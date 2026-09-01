// Package circuitbreaker implements the classic closed/open/half-open
// state machine from scratch. Real libraries (sony/gobreaker, etc.) exist
// and are what you'd reach for in production — writing this by hand once
// is what makes the state machine an actual mental model instead of an
// API you call without understanding.
package circuitbreaker

import (
	"sync"
	"sync/atomic"
	"time"
)

// state is unexported — callers interact with the breaker through Allow,
// RecordSuccess, and RecordFailure, never by reading or setting state
// directly. That's deliberate: the only valid state transitions are the
// ones this file defines, and keeping state private is what enforces
// that from outside the package.
type state int

const (
	closed state = iota
	open
	halfOpen
)

func (s state) String() string {
	switch s {
	case closed:
		return "closed"
	case open:
		return "open"
	case halfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Breaker protects a single upstream dependency from being hammered
// during an outage. One Breaker corresponds to one upstream target — in
// Router's case, one Breaker per Target (today, there's exactly one
// target, so exactly one Breaker; Week 3's later provider-failover work
// gives each provider its own).
type Breaker struct {
	mu sync.Mutex

	state               atomic.Int32
	consecutiveFailures int
	failureThreshold    int // consecutive failures before tripping to open
	openedAt            time.Time
	cooldown            time.Duration // how long to stay open before trying half-open
	halfOpenInFlight    bool          // ensures exactly one trial request during half-open
}

// New builds a Breaker. failureThreshold consecutive failures trip it
// open; cooldown is how long it stays open before allowing a single
// trial request through (half-open).
func New(failureThreshold int, cooldown time.Duration) *Breaker {
	b := &Breaker{
		failureThreshold: failureThreshold,
		cooldown:         cooldown,
	}
	b.state.Store(int32(closed))
	return b
}

// Allow reports whether the caller may proceed with calling the upstream
// right now. This must be checked BEFORE making the call — the entire
// point of a circuit breaker is that a request in the open state never
// reaches the upstream at all, not even to fail fast against it.
//
// The half-open case is the subtle part: when the cooldown has elapsed,
// exactly ONE caller is allowed through as a trial — not zero (the
// breaker would never recover on its own) and not all of them (that just
// repeats the exact overload that tripped it in the first place). The
// halfOpenInFlight flag is what enforces "exactly one": the first caller
// to observe the cooldown has elapsed flips it to true and becomes the
// trial; every other concurrent caller sees it already true and is still
// rejected until that trial resolves.
func (b *Breaker) Allow() bool {
	// FAST PATH: lock-free atomic read (0.1ns)
	if state(b.state.Load()) == closed {
		return true
	}

	// SLOW PATH: lock required for open/halfOpen state management
	b.mu.Lock()
	defer b.mu.Unlock()

	switch state(b.state.Load()) {
	case closed:
		return true // could have closed between our fast-path read and acquiring lock

	case open:
		if time.Since(b.openedAt) < b.cooldown {
			return false
		}
		// Cooldown elapsed — transition to half-open and let THIS
		// caller be the trial request.
		b.state.Store(int32(halfOpen))
		b.halfOpenInFlight = true
		return true

	case halfOpen:
		// Another trial is already in flight; everyone else waits.
		return false

	default:
		return false
	}
}

// RecordSuccess reports that the most recent call succeeded. From ANY
// state, a success means the upstream is healthy: closed stays closed
// with its failure count reset, and a half-open trial succeeding closes
// the breaker fully.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecutiveFailures = 0
	b.state.Store(int32(closed))
	b.halfOpenInFlight = false
}

// RecordFailure reports that the most recent call failed. Behavior
// depends on the state the failure happened in:
//   - closed: increment the streak; trip to open if it hits threshold.
//   - halfOpen: the trial failed — the upstream is still unhealthy, so
//     go straight back to open and restart the cooldown clock. This is
//     what stops a flaky-but-still-broken upstream from bouncing the
//     breaker open/half-open/open every single cooldown interval with no
//     real recovery — it needs a genuinely successful trial to close.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch state(b.state.Load()) {
	case halfOpen:
		b.state.Store(int32(open))
		b.openedAt = time.Now()
		b.halfOpenInFlight = false

	case closed:
		b.consecutiveFailures++
		if b.consecutiveFailures >= b.failureThreshold {
			b.state.Store(int32(open))
			b.openedAt = time.Now()
		}

	case open:
		// Allow() should have already rejected this caller before it
		// ever reached the upstream — reaching here would mean a caller
		// bypassed Allow(), which is a usage bug elsewhere, not a state
		// this function needs to handle specially.
	}
}

func (b *Breaker) State() string {
	return state(b.state.Load()).String()
}
