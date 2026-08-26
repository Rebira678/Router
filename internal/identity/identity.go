// Package identity provides one shared way to fingerprint a raw caller
// identity (today: an Authorization header value) into a short,
// irreversible hash — so a real bearer token or API key never has to be
// stored in plain text anywhere: not in a Redis key name, not in a log
// line, and — as of Day 10 — not in a Postgres row either.
//
// This was originally written inline inside internal/ratelimit on Day 7.
// Pulled out into its own package on Day 10 because internal/usage needs
// the exact same hash for the exact same reason, and duplicating the
// logic in two places would mean a future change (e.g. switching hash
// algorithms) silently drifting out of sync between them. One function,
// two callers.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
)

// Hash turns a raw identity string into a short, deterministic
// fingerprint. Same input always produces the same output — which is
// exactly what lets a tenant's rate-limit bucket (internal/ratelimit) and
// their usage/billing rows (internal/usage) be correlated by this same
// hash, without either system ever needing to see the real credential.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:16]
}
