package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yourname/router/internal/identity"

	_ "github.com/lib/pq" // registers the "postgres" driver with database/sql
)

// Store writes usage events to Postgres. It wraps the standard library's
// *sql.DB rather than any ORM — database/sql's connection pooling,
// prepared-statement caching, and context support are already exactly
// what's needed here, and an ORM would add a layer of abstraction over
// four lines of SQL that don't need one.
type Store struct {
	db *sql.DB
}

// NewStore opens a connection pool to Postgres. dsn follows the standard
// "postgres://user:password@host:port/dbname?sslmode=disable" format.
// Connection is NOT verified here — call Ping in the caller (main.go),
// the same pattern already used for Redis, so a Postgres outage at
// startup fails loudly and immediately rather than being discovered on
// the first real request.
func NewStore(dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("usage: opening postgres connection: %w", err)
	}
	return &Store{db: db}, nil
}

// Ping verifies the database is actually reachable. sql.Open alone never
// establishes a real connection — it just validates the DSN string — so
// this is the call that would actually surface "Postgres is down."
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Close releases the connection pool. Should be called on shutdown,
// mirroring how main.go already closes the Redis client.
func (s *Store) Close() error {
	return s.db.Close()
}

// Record writes one usage event. rawIdentity is the caller's raw
// identity (e.g. the full Authorization header) — hashing happens here,
// inside Record, so there's exactly one place in the codebase responsible
// for making sure a raw credential never reaches a SQL statement.
//
// occurredAt is passed explicitly rather than always using time.Now()
// internally, so callers (and tests) can control it precisely — useful
// once this gets wired into the real request path on Day 11, where
// "when the request actually completed" is a fact the caller already
// knows and shouldn't be recomputed here.
func (s *Store) Record(ctx context.Context, rawIdentity, model string, tokensUsed int, costMicros int64, occurredAt time.Time) error {
	tenantHash := identity.Hash(rawIdentity)

	const query = `
		INSERT INTO usage_events (tenant_hash, model, tokens_used, cost_micros, occurred_at)
		VALUES ($1, $2, $3, $4, $5)
	`

	_, err := s.db.ExecContext(ctx, query, tenantHash, model, tokensUsed, costMicros, occurredAt)
	if err != nil {
		return fmt.Errorf("usage: recording event: %w", err)
	}
	return nil
}

// TotalCostMicros sums a tenant's cost over a time window — a first,
// simple example of the "compute totals on demand" principle from
// today's design discussion, rather than maintaining a separately updated
// running counter. rawIdentity is hashed internally, same as Record, so
// callers never need to know or handle the hash themselves.
func (s *Store) TotalCostMicros(ctx context.Context, rawIdentity string, since, until time.Time) (int64, error) {
	tenantHash := identity.Hash(rawIdentity)

	const query = `
		SELECT COALESCE(SUM(cost_micros), 0)
		FROM usage_events
		WHERE tenant_hash = $1 AND occurred_at >= $2 AND occurred_at < $3
	`

	var total int64
	err := s.db.QueryRowContext(ctx, query, tenantHash, since, until).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("usage: summing cost: %w", err)
	}
	return total, nil
}