# Day 12 — Real JWT Authentication (Stateless Auth)

**Concept mastered:** JWT structure, validation, and why stateless authentication (validating tokens in CPU via cryptography) is a massive performance win for an API Gateway compared to opaque token database lookups.
**Built:** Refactored `internal/identity/middleware.go` to use `github.com/golang-jwt/jwt/v5`, added a secret injection in `main.go`, and built a `cmd/keygen` utility to generate valid JWTs for testing.

---

## 1. The Scenario: The Fake ID Problem

Yesterday, our Auth middleware simply read the raw `Authorization` header and trusted it. If you sent `Authorization: Bearer test-user-1`, the system happily logged your usage under `test-user-1`. It was essentially a fake ID system where anyone could write a name on a piece of paper and walk through the door.

Today, we replaced it with **real cryptographic authentication**.

## 2. The Design Decision: JWTs vs Opaque Tokens

When building an API Gateway, you have two main choices for API keys/tokens:

**1. Opaque Tokens (e.g., standard Stripe/GitHub API keys):**
These are random strings (e.g., `sk_live_12345`). The gateway must take this string and query a database (Postgres or Redis) on *every single request* to ask: "Is this valid? Who does it belong to?"
* **Pros:** Instantly revocable.
* **Cons:** Adds network latency to every single request. For a high-traffic AI inference proxy where latency is critical, hitting a database just to verify identity is a massive bottleneck.

**2. JSON Web Tokens (JWTs):**
These are base64-encoded JSON objects cryptographically signed by the server. 
* **Pros:** The gateway can verify the token is valid, hasn't expired, and belongs to a specific tenant purely in CPU (using a secret key), **without ever talking to a database**. This is called *stateless authentication*.
* **Cons:** Harder to revoke instantly (you usually have to wait for them to expire).

**The Verdict:** For Router, we chose **JWTs**. Since Router is a proxy meant to process thousands of requests per second with minimal overhead, CPU-bound signature verification is vastly superior to network-bound database lookups.

## 3. Reading the Code (`internal/identity/middleware.go`)

We brought in the industry-standard `github.com/golang-jwt/jwt/v5` package.

1. **The Blacklight (Signature Verification):**
   The middleware uses a `secretKey` (injected at startup) to verify the HMAC signature of the incoming JWT. 
   *Critical security detail:* We explicitly verify that `token.Method` is `*jwt.SigningMethodHMAC`. This prevents a famous exploit where attackers change the header to `"alg": "none"`, tricking naive libraries into skipping signature validation entirely.
2. **Extracting the Subject:**
   Once verified, we extract the `sub` (subject) claim, which represents our `tenant_id`. 
3. **The Payoff of Day 11:**
   Once we extract the `sub` claim, we inject it into the request context (`r.Context()`). **Notice what we DIDN'T have to do:** We didn't touch the Rate-Limiter. We didn't touch the Usage-Tracker. Because we decoupled identity extraction (Day 11), they just keep reading the "sticky name tag" from the context, completely oblivious to the fact that we upgraded to military-grade cryptography under the hood.

## 4. Running It

Because we now require real JWTs, you can't just curl with random strings anymore. I built a utility to generate valid tokens.

**1. Generate a Token:**
```bash
go run ./cmd/keygen my-test-tenant
```
*Output:* `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...` (your token)

**2. Start the Server:**
```bash
go run ./cmd/router
```

**3. Make a Request:**
If you use the old fake ID, you will now get blocked:
```bash
curl -H "Authorization: Bearer test-user-1" http://localhost:8080/v1/chat/completions
# Output: invalid or expired token
```

Use your real JWT:
```bash
curl -H "Authorization: Bearer <YOUR_GENERATED_JWT>" http://localhost:8080/v1/chat/completions
```
*Success!* The rate limiter and usage tracker will now log events under the hash of `my-test-tenant`.

## 5. What to say out loud in 60 seconds

*"I upgraded the API Gateway's authentication layer to use JSON Web Tokens (JWTs) instead of opaque API keys. I chose JWTs because they enable stateless authentication — the gateway can verify a tenant's identity and expiration cryptographically in CPU, without adding network latency from database lookups on every single request. I used the `golang-jwt` package, ensuring strict algorithm validation to prevent 'alg: none' attacks. Best of all, because I implemented a composable middleware chain using request context yesterday, I completely swapped out the authentication mechanism today without altering a single line of code in the rate-limiting or usage-tracking layers."*

## 6. What's deliberately not here yet

- **A production tenant-management API:** We built a local CLI tool (`cmd/keygen`) to generate tokens today. Tomorrow (Day 13), we will build an internal gRPC endpoint so a backend admin service can programmatically issue these JWTs for new users.
