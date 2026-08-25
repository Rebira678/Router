-- Day 10: usage_events — an append-only log of every completed request.
--
-- Design decisions, explained (not just declared):
--
-- id BIGSERIAL: a simple auto-incrementing integer, not a UUID. For an
--   append-only event log written by one logical service (Router), a
--   UUID's main advantage — safe ID generation across many independent
--   writers with no coordination — isn't needed here; Postgres already
--   serializes inserts to one sequence cheaply. BIGSERIAL is smaller on
--   disk and faster to index than UUID, and "this row was written before
--   that row" is a genuinely useful property in an event log, which a
--   BIGSERIAL gives you for free and a random UUID does not.
--
-- tenant_hash, not tenant_id or the raw API key: same principle as Day 7's
--   fix in the rate limiter — a real bearer token should never sit in
--   plain text in a database that operators, backups, and analytics
--   tools can all read. This is the SAME hash function as ratelimit's
--   (see internal/identity), so a tenant's rate-limit key and their
--   billing rows are correlatable without ever storing their real token.
--
-- cost_micros BIGINT, not a FLOAT/NUMERIC dollar amount: cost is stored
--   as an integer count of micro-dollars (1 / 1,000,000 of a dollar).
--   $0.0034 is stored as the integer 3400. This makes rounding error
--   structurally impossible — there's no fractional representation to
--   lose precision on, in Postgres OR in Go. Convert to a display dollar
--   amount only at the very end, in a report or UI, never mid-calculation.
--
-- occurred_at TIMESTAMPTZ, not TIMESTAMP: always store timezone-aware
--   timestamps for anything billing-related. A plain TIMESTAMP silently
--   assumes a timezone that depends on server/session config — exactly
--   the kind of ambiguity that turns into a real invoice dispute later.
--
-- The composite index on (tenant_hash, occurred_at) — not two separate
--   single-column indexes — matches how this table will actually be
--   queried: "give me tenant X's usage between date A and date B." Column
--   order in a composite index matters: tenant_hash first because it's
--   the equality filter (narrows to one tenant immediately), occurred_at
--   second because it's the range filter within that already-narrowed
--   set. Reversing the order would make Postgres scan much more of the
--   index before the tenant filter even applies.

CREATE TABLE IF NOT EXISTS usage_events (
    id           BIGSERIAL PRIMARY KEY,
    tenant_hash  TEXT NOT NULL,
    model        TEXT NOT NULL,
    tokens_used  INTEGER NOT NULL CHECK (tokens_used >= 0),
    cost_micros  BIGINT NOT NULL CHECK (cost_micros >= 0),
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_usage_events_tenant_time
    ON usage_events (tenant_hash, occurred_at);