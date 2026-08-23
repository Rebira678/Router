package ratelimit

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashIdentity turns a raw rate-limit identity — today, that's a full
// Authorization header, which may contain a real bearer token or API key
// — into a short, irreversible fingerprint.
//
// Two things this buys us, found during Day 7's review of Week 1:
//
//  1. Redis key names built from this never contain the actual credential.
//     Running `redis-cli KEYS "*"` or watching Redis's slow log no longer
//     exposes real tokens in plain text.
//  2. Log lines that need to identify *which* tenant got rate-limited can
//     safely include this fingerprint — enough to correlate repeated hits
//     from the same caller across log lines, without ever printing the
//     credential itself.
//
// SHA-256 is used purely as a fingerprint here, not for any security
// property like "can't be reversed by a determined attacker with a
// wordlist" — that distinction matters less for our purposes than the
// simple fact that the raw token no longer appears verbatim anywhere
// Router writes to disk or exposes to an operational tool. Truncating to
// 16 hex characters (64 bits) is far more than enough entropy to keep
// distinct tenants from colliding, while keeping Redis key names and log
// lines short and readable.
func hashIdentity(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:16]
}
