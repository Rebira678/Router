# Day 10 — Postgres Schema for Usage Tracking

**Concept mastered:** designing an append-only event log for
billing-critical data, and why money should never be a float.
**Built:** `migrations/0001_usage_events.sql`, `internal/identity`
(extracted shared hashing), `internal/usage` (`Event`, `Store`), Postgres
connectivity wired into `main.go` — deliberately not yet wired into the
live request path.

---

## 1. The scenario: a phone company with no call log

Every request Router forwards costs real money in LLM tokens. Right now,
nothing remembers that a request even happened once it's answered — like
a phone company that charges by the minute but keeps no record of any
call, just trusts the bill to be right. Today fixes that: every completed
request gets written to a permanent record.

## 2. The design decision: event log, not running counter

The tempting shortcut is a single `tenant_totals` table with one mutable
number per tenant, incremented on every request. Rejected, for two real
reasons:

- **A bug that double-counts becomes permanent and unrecoverable.** With
  an event log, a bad row can be deleted or corrected and everything else
  stays intact. With a running counter, there's no way to know which part
  of the number is wrong.
- **A single number can't answer "how much did tenant A use on Tuesday?"**
  — that detail was never kept. An event log can answer any historical
  breakdown, because every individual fact is still there. Totals get
  computed *on demand* with `SUM()`, not maintained incrementally.

## 3. The other real decision: cost is an integer, never a float

`float64` cannot represent most decimal fractions exactly — `0.1 + 0.2`
famously doesn't equal `0.3` in floating point. Across millions of
billing rows, that's not a rounding curiosity, it's a ledger that doesn't
reconcile. The fix: store cost as an **integer count of micro-dollars**
(millionths of a dollar). `$0.0034` becomes the integer `3400`. There's
no fractional representation involved anywhere in storage or arithmetic,
so there's no rounding error to accumulate. Convert to a dollar amount
only at the very last step — a report or a UI — never mid-calculation.

## 4. Reading the schema (`migrations/0001_usage_events.sql`)

- **`BIGSERIAL`, not UUID, for the primary key.** A UUID's real advantage
  is safe ID generation across many independent, uncoordinated writers —
  not needed here, since Postgres already serializes one sequence
  cheaply for a single logical writer (Router). BIGSERIAL is smaller,
  faster to index, and "this row was written before that one" falls out
  for free — a genuinely useful property in an event log.
- **`tenant_hash`, not the raw API key.** Exactly Day 7's lesson, applied
  again: a real bearer token has no business sitting in plain text in a
  database that backups, analytics tools, and other engineers can all
  read. Reusing the *same* hash function as the rate limiter (see below)
  means a tenant's usage rows and their rate-limit bucket are
  correlatable without either system ever storing the real credential.
- **The composite index on `(tenant_hash, occurred_at)`, in that column
  order specifically.** Every real query here looks like "this tenant,
  this date range." `tenant_hash` first narrows to one tenant
  immediately (an equality filter); `occurred_at` second scans a range
  *within* that already-narrow set. Reversing the order would make
  Postgres scan far more of the index before the tenant filter even
  applies — index column order is a real design decision, not
  boilerplate.

## 5. The `internal/identity` refactor

Day 7's hash function lived inline inside `internal/ratelimit`. Today it
moved into its own tiny package, because `internal/usage` needs the
*exact same* hash for the exact same reason — and duplicating that logic
in two places would mean a future change (switching algorithms, say)
silently drifting out of sync between them. `ratelimit.hashIdentity` now
just delegates to `identity.Hash` — one function, two callers, one source
of truth.

## 6. Reading `internal/usage/store.go`

- **`database/sql` directly, no ORM.** Connection pooling, prepared
  statement handling, and context support are already exactly what's
  needed for four lines of SQL — an ORM would add abstraction over
  something that doesn't need any.
- **Hashing happens inside `Record`, not in the caller.** `Record` takes
  the *raw* identity and hashes it internally — so there's exactly one
  place in the whole codebase responsible for making sure a raw
  credential never reaches a SQL statement, the same discipline as
  `RedisLimiter.Allow`.
- **`Ping` is separate from `NewStore`.** `sql.Open` never actually
  connects to anything — it only validates the DSN string. `Ping` is
  the real connectivity check, called explicitly in `main.go`, mirroring
  exactly how Redis's connectivity is verified at startup.

## 7. Why this is NOT wired into the live request path yet

`main.go` connects to Postgres and proves it's reachable — but no request
handler calls `usageStore.Record(...)` yet. That's deliberate, not an
oversight: Day 11's entire job is "wire auth → rate-limit →
usage-tracking as composable middleware." Wiring `Record` into a handler
today would mean redoing that wiring tomorrow inside a proper middleware
chain — better to build the storage layer completely first, prove it
independently, and compose it into the request path once, correctly, on
Day 11.

## 8. Running it — needs Postgres, same pattern as Day 6 needed Redis

```bash
docker run -d --name postgres \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=router \
  -p 5432:5432 postgres:16-alpine

# apply the schema
docker exec -i postgres psql -U postgres -d router < migrations/0001_usage_events.sql

# fetch the new dependency
go mod tidy

go run ./cmd/router
```

You should see `"postgres: connected"` logged, right after
`"redis: connected"`, before `"router: listening"`.

**Prove the schema and Store actually work**, independent of the HTTP
server, with a short one-off script:

```go
store, _ := usage.NewStore("postgres://postgres:postgres@localhost:5432/router?sslmode=disable")
store.Record(context.Background(), "Bearer tenant-A", "mock-llm-v1", 1200, 3400, time.Now())
```

Then verify directly:
```bash
docker exec -it postgres psql -U postgres -d router -c "SELECT * FROM usage_events;"
```
You should see one row, with `tenant_hash` as a 16-character hex string —
not `Bearer tenant-A` in plain text.

## 9. What to say out loud in 60 seconds

*"I designed usage tracking as an append-only event log instead of a
running total per tenant, because a running counter can't be corrected if
a bug overcounts, and it can't answer historical questions like 'usage on
a specific day' — that detail was never kept. Cost is stored as an
integer number of micro-dollars, never a float, because floating point
can't represent most decimal fractions exactly, and that error compounds
across millions of billing rows. The tenant identity is hashed using the
same function the rate limiter already uses, refactored into a shared
package, so a raw API key never touches the database, and usage rows
still correlate with rate-limit buckets through the same fingerprint."*

## 10. What's deliberately not here yet

- Not wired into the live request path — Day 11.
- No pricing logic (converting tokens → cost) lives here yet; `Record`
  takes `costMicros` as a pre-computed input.
- No migration tooling (golang-migrate, etc.) — one plain `.sql` file,
  applied manually, is enough for a single migration.
- `TotalCostMicros` is a minimal example of computing on demand, not a
  full reporting/billing API — that's future scope, not today's.