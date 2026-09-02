// Package workerpool bounds how many requests Router will process
// concurrently, regardless of how many connections net/http has open at
// once. It sits in front of any http.Handler (in practice, the proxy from
// Day 1) as ordinary middleware.
//
// The problem this solves: net/http already gives every inbound
// connection its own goroutine — that part is not something we add or
// control. What IS unbounded, left alone, is how many of those goroutines
// are simultaneously allowed to call out to the upstream LLM provider.
// Under a traffic spike, that turns into a thundering herd against your
// own upstream — and every one of those in-flight calls is also holding a
// TCP connection, a slot in Router's connection pool, and (once Week 2
// adds cost tracking) a billing liability, all at once.
//
// The fix is not "add more goroutines" — Go already gave you those for
// free. The fix is a fixed number of workers pulling from a bounded
// queue, so there is a hard, known ceiling on concurrent upstream work,
// and a defined behavior (fail fast) once that ceiling is hit.
package workerpool

import (
	"log/slog"
	"net/http"
)

// job bundles everything a worker goroutine needs to finish handling a
// request that the accepting goroutine is now blocked waiting on.
//
// Passing http.ResponseWriter across goroutines like this is safe *only*
// because of the discipline enforced below: exactly one goroutine (the
// worker) writes to w, and the original request goroutine does nothing
// but block on done until the worker signals completion. If both sides
// touched w concurrently, this would be a data race — the done channel is
// what prevents that by construction, not by convention.
type job struct {
	w    http.ResponseWriter
	r    *http.Request
	done chan struct{}
}

// Pool bounds concurrency for a wrapped http.Handler.
type Pool struct {
	next    http.Handler
	jobs    chan job
	workers int
}

// New starts `workers` goroutines and returns a Pool that satisfies
// http.Handler itself, so it drops straight into an existing handler
// chain: workerpool.New(proxyHandler, 20, 100).
//
//   - workers is the hard concurrency ceiling: at most this many requests
//     are ever being actively processed (i.e. making upstream calls) at
//     the same time.
//   - queueSize is the buffer on the jobs channel: how many additional
//     requests may wait for a free worker before Router starts responding
//     503 instead of queueing further. This is the "buffered channel"
//     half of today's concept — queueSize=0 would make the channel
//     unbuffered, meaning a request only gets accepted into the queue at
//     the exact instant a worker is free to take it immediately, which in
//     practice rejects almost everything under any real concurrency.
//     A small positive buffer lets short bursts smooth out without
//     letting the queue (and therefore memory and worst-case latency)
//     grow without limit.
func New(next http.Handler, workers, queueSize int) *Pool {
	p := &Pool{
		next:    next,
		jobs:    make(chan job, queueSize),
		workers: workers,
	}
	for i := 0; i < workers; i++ {
		go p.runWorker(i)
	}
	return p
}

// runWorker is the loop each of the fixed N goroutines runs for the
// lifetime of the process. `for j := range p.jobs` blocks on an empty
// channel and exits cleanly if the channel is ever closed — we don't
// close it in this version (the pool lives as long as the process), but
// using range instead of an infinite `for { j := <-p.jobs }` costs
// nothing and keeps shutdown semantics available for free if you add
// pool.Close() later.
func (p *Pool) runWorker(id int) {
	for j := range p.jobs {
		p.next.ServeHTTP(j.w, j.r)
		close(j.done) // signal completion; never send a value, just close
	}
}

// ServeHTTP implements http.Handler. This is the accepting side: it tries
// to hand the request off to a worker without blocking indefinitely.
func (p *Pool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	j := job{w: w, r: r, done: make(chan struct{})}

	// A non-blocking send: `select` with a `default` case means "try the
	// send; if it can't complete THIS INSTANT, take the other branch
	// instead of waiting." This is what makes the pool bounded rather
	// than just "eventually consistent" — without the default case, a
	// full queue would make every new request's goroutine pile up
	// blocked on the channel send, which is precisely the unbounded
	// growth we set out to avoid, just moved one level up.
	select {
	case p.jobs <- j:
		// Accepted into the queue. Wait for the worker to finish —
		// but watch r.Context().Done() too (Day 3), not just j.done.
		// Without this, a client that disconnects while still queued
		// (waiting for a free worker) would leave this goroutine
		// blocked until a worker eventually gets to it anyway — wasted
		// work for a response nobody is listening for.
		//
		// Important nuance: canceling here does NOT stop the worker.
		// The job is already sitting in p.jobs; once a worker dequeues
		// it, p.next.ServeHTTP(j.w, j.r) still runs — and inside that
		// call, proxy.go's own context handling (Step 2/3 above) is
		// what will actually notice r.Context() is canceled and abort
		// the upstream call. This select only stops THIS goroutine
		// from pointlessly waiting around for a result it can no
		// longer deliver anywhere. That's the honest limit of
		// cancellation in Go: it's cooperative, propagated by
		// convention, not a forceful kill switch.
		select {
		case <-j.done:
		case <-r.Context().Done():
			slog.InfoContext(r.Context(), "workerpool: client disconnected while request was queued/in-flight",
				"path", r.URL.Path)
		}
	default:
		slog.WarnContext(r.Context(), "workerpool: queue full, rejecting request",
			"path", r.URL.Path,
			"workers", p.workers,
			"queue_capacity", cap(p.jobs),
		)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "service overloaded: try again shortly", http.StatusServiceUnavailable)
	}
}
