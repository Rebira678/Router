// Package usage records what each request actually cost, as a permanent,
// append-only log — the foundation Week 2's later billing work builds on.
package usage

import "time"

// Event represents one completed request's usage — the Go-side mirror of
// one row in the usage_events table. Deliberately a plain struct with no
// behavior: this package's job is recording facts, not computing pricing
// policy (that's a separate, future concern — keeping this struct dumb
// keeps that boundary clean).
type Event struct {
	// TenantHash is the same hashed identity used by internal/ratelimit —
	// never the raw API key. Set by Store.Record, not by the caller
	// directly, so there's exactly one place in the codebase that decides
	// how a raw identity becomes a stored hash.
	TenantHash string

	// Model identifies which upstream model served the request (e.g.
	// "mock-llm-v1" today; a real provider's model name once Router
	// talks to a real upstream). Stored per-event, not per-tenant,
	// because a single tenant can and will call different models across
	// different requests, each at a different price.
	Model string

	// TokensUsed is the total token count for this single request.
	TokensUsed int

	// CostMicros is the cost of this request in micro-dollars (millionths
	// of a dollar) — an integer, deliberately never a float. $0.0034
	// is stored as 3400. See migrations/0001_usage_events.sql for the
	// full reasoning.
	CostMicros int64

	// OccurredAt is when the request completed. Left as time.Time here;
	// Store.Record is responsible for ensuring it's written to Postgres
	// as a timezone-aware value.
	OccurredAt time.Time
}