# 🏛️ The Complete Visual Guide to the API Gateway

Welcome to the **Exclusive Gym**. This document explains every single file in your project using simple, real-world analogies. Think of your API Gateway not as code, but as a fully functioning luxury gym with front desk staff, bouncers, accountants, and personal trainers. 

We will go through every single file in the project so you understand exactly what it holds and how data flows through it.

---

## 📂 1. The `cmd/` Directory (The Entry Points)
These are the files you can actually run using `go run`. They are the "front doors" to different tools.

### `cmd/router/main.go` (The Gym Manager)
**Analogy:** The Gym Manager unlocks the building in the morning. He doesn't do the heavy lifting himself; instead, he turns on the lights, connects the phones (Redis/Postgres), hires the staff (middleware), and opens the front doors to the public.
**What it holds:**
- `redisClient` & `pgDSN`: The phone numbers to call the external Bouncer (Redis) and Accountant (Postgres).
- `jwtSecret`: The special stamp used to verify VIP ID cards.
- `composedHandler`: The physical line that customers stand in (Auth -> Rate Limit -> Usage -> Proxy).
- **How data moves:** It runs once at startup. It passes the secret keys and database connections into the staff (middleware) and then starts the HTTP Server on Port 8080.

### `cmd/loadtester/main.go` (The Health & Safety Inspector)
**Analogy:** An inspector who clones himself 50 times and tries to barge through the front doors at the exact same millisecond to prove the Bouncer (Rate Limiter) works.
**What it holds:**
- `workers` (50): How many concurrent clones to spawn.
- `duration` (5s): How long the attack lasts.
- `results`: A channel (bucket) where clones drop their success (200) or failure (429) receipts.
- **How data moves:** It creates 50 goroutines that constantly fire HTTP requests at `localhost:8080`.

### `cmd/keygen/main.go` (The ID Printing Machine)
**Analogy:** A small machine in the back room used to manually print VIP ID cards (JWTs) for testing.
**What it holds:**
- `golang-jwt`: The library used to generate the token.
- `subject`: The name you type in the terminal (e.g., `go run ./cmd/keygen test-user`).
- **How data moves:** It takes your terminal input, signs it with the `jwtSecret`, and prints a giant Base64 encoded string to your screen.

### `cmd/grpcclient/main.go` (The Back-Office Telephone)
**Analogy:** A direct red telephone to the Back-Office Administrator (gRPC Server) used to test the hidden Port 9092.
**What it holds:**
- `grpc.Dial`: The network connection to Port 9092.
- `tenant.NewTenantServiceClient`: The strict rulebook on how to talk to the Admin.
- **How data moves:** It sends a binary `CreateTenantRequest` over the wire, and prints the raw JWT returned by the Admin.

### `cmd/raceprobe/main.go` & `cmd/testdb/main.go` (The Plumbers)
**Analogy:** Small diagnostic tools used by mechanics to check if the pipes (Goroutine memory and Postgres connections) are leaking.
**What it holds:** Simple `main` functions that run isolated tests. They do not interact with live customers.

---

## 📂 2. The `internal/middleware/` Directory
### `internal/middleware/middleware.go` (The Velvet Rope)
**Analogy:** The physical velvet rope that forces customers to walk in a straight line past the ID Checker, then the Bouncer, then the Accountant.
**What it holds:**
- `Middleware` (type): A function signature.
- `Chain()`: A helper function that takes a list of staff members and links them together.
- **How data moves:** A customer enters `Chain(Auth, RateLimit, Proxy)`. The data flows sequentially: Auth passes it to RateLimit, which passes it to Proxy.

---

## 📂 3. The `internal/identity/` Directory (Authentication)
### `internal/identity/middleware.go` (The ID Checker)
**Analogy:** The security guard who inspects your ID card (JWT) under a UV light to make sure it's not fake.
**What it holds:**
- `secret []byte`: The UV light / cryptographic signature.
- **How data moves:** It extracts the `Authorization: Bearer <TOKEN>` string from the HTTP Header, verifies the math, and extracts the `sub` (Tenant ID). 
- **The Handoff:** It slaps a sticky name tag on the customer (`context.WithValue`) so the rest of the staff knows who they are.

### `internal/identity/identity.go` (The Name Tag Reader)
**Analogy:** A tiny helper tool that other staff use to read the sticky name tag.
**What it holds:**
- `FromContext()`: A function that looks at the `context` and returns the Tenant ID string.

---

## 📂 4. The `internal/ratelimit/` Directory (The Bouncer)
### `internal/ratelimit/middleware.go` (The Bouncer's Desk)
**Analogy:** The physical desk where the Bouncer stands.
**What it holds:**
- `keyFunc`: A function that tells the Bouncer where to look for the customer's name (the sticky name tag).
- **How data moves:** It extracts the name, hashes it, and asks the `Limiter` if they are allowed in. If not, it returns HTTP 429.

### `internal/ratelimit/limiter.go` (The Bouncer's Rulebook)
**Analogy:** The blank interface that says "Every bouncer must have an Allow() method."

### `internal/ratelimit/redis_limiter.go` (The Cloud Whiteboard)
**Analogy:** A Bouncer who uses an external whiteboard (Redis) so that even if there are 5 doors (servers), all Bouncers share the same count.
**What it holds:**
- `evalCmd`: The raw Lua Script executed inside Redis to prevent race conditions.
- **How data moves:** It sends the `key` to Redis. Redis subtracts 1 ticket and returns a boolean (true/false).

### `internal/ratelimit/tokenbucket.go` & `race_test.go` (The Old Local Whiteboard)
**Analogy:** The old Bouncer from Week 1 who used a local paper notepad (`sync.Mutex`). We don't use him for production anymore because he forgets everything if the Gym restarts, but the files remain as a fallback.

### `internal/ratelimit/keys.go` (The Alias Generator)
**Analogy:** To protect privacy, the Bouncer doesn't write "John Doe" on the whiteboard. He runs it through a cryptographic hash (SHA-256) and writes "a4b8c9..." instead. This file holds that hashing math.

---

## 📂 5. The `internal/usage/` Directory (The Accountant)
### `internal/usage/middleware.go` (The Turnstile)
**Analogy:** The turnstile you walk through right before getting on the treadmill. It records the exact moment you started your workout.
**What it holds:**
- `Store`: The connection to the vault.
- `modelName`: What machine you are using.
- **How data moves:** It reads your sticky name tag. It lets you workout (`next.ServeHTTP`). When you finish, it calculates the cost and calls the Store.
- **The Magic Shield:** It uses `contextWithoutCancel` so that if you run away mid-workout (close your browser), the system doesn't cancel the receipt writing process.

### `internal/usage/store.go` (The Vault Connection)
**Analogy:** The secure armored truck that carries the receipt from the turnstile to the actual Postgres Database.
**What it holds:**
- `db *sql.DB`: The live pool of database connections.
- `RecordEvent()`: The raw SQL `INSERT INTO usage_events...` query.
- **How data moves:** It takes the Go struct and safely injects the strings into the SQL database to permanently save the billing record.

### `internal/usage/event.go` (The Receipt Paper)
**Analogy:** The blank template of a receipt.
**What it holds:**
- `Event` (Struct): Contains `TenantID`, `Model`, `CostMicroUSD`, and `Timestamp`. It defines exactly what data is needed to bill a customer.

---

## 📂 6. The `internal/proxy/` Directory
### `internal/proxy/proxy.go` (The Personal Trainer / Conveyor Belt)
**Analogy:** The Personal Trainer who takes your request, walks it over to the Treadmill (OpenAI), and brings the result back to you.
**What it holds:**
- `Target`: The URL of the treadmill (e.g., `localhost:9091`).
- `http.Client`: The phone used to call OpenAI.
- `bodyBytes`: The trainer writes your exact request on a notepad (buffers it) so he can retry it later.
- **How data moves:** 
1. It copies your HTTP request.
2. It sends it to OpenAI.
3. **Day 15 Magic:** If OpenAI is broken, a `for` loop catches the error, sleeps for a random amount of time (Exponential Backoff + Jitter), and tries again up to 3 times.
4. If it succeeds, it streams the response back to you.

---

## 📂 7. The `internal/mockllm/` Directory
### `internal/mockllm/server.go` (The Fake Treadmill)
**Analogy:** Since OpenAI costs real money, we built a fake, cardboard treadmill to practice on locally.
**What it holds:**
- `Server` (Struct): A miniature HTTP server listening on Port 9091.
- **How data moves:** Whenever it receives a request, it instantly replies with a fake, hardcoded JSON string: `{"choices":[{"message":"This is a canned response..."}]}`.

---

## 📂 8. The `internal/tenant/` & `proto/` Directories (The Back-Office)
### `proto/router/v1/tenant.proto` (The Corporate Rulebook)
**Analogy:** The strict legal contract defining how to talk to the Back-Office.
**What it holds:**
- Written in Protobuf, not Go. It states: "To create a tenant, you must send a `CreateTenantRequest` with a string called `name`. I will return a `CreateTenantResponse` with a string called `jwt_token`."

### `pkg/api/proto/router/v1/tenant.pb.go` (The Robot Translators)
**Analogy:** You never touch these files. These are automatically generated robot translators that convert the `.proto` rulebook into native Go code.

### `internal/tenant/grpc_server.go` (The Back-Office Administrator)
**Analogy:** The administrator sitting behind Port 9092.
**What it holds:**
- `TenantServiceServer`: The interface implementation.
- `jwtSecret`: The stamp used to mint new cards.
- **How data moves:** It receives the gRPC request, uses the `golang-jwt` library to sign a brand new token, and returns it over the wire.

---

## 📂 9. The `internal/workerpool/` Directory
### `internal/workerpool/pool.go` (The Ticket Counter)
**Analogy:** An older system we built (Day 9) to limit how many people can be inside the gym at the exact same time (using Go Channels). 
**What it holds:**
- `sem chan struct{}`: A channel acting as a bucket of physical entry tokens. If the bucket is empty, customers have to wait in line.

---

## 📂 10. The `migrations/` Directory
### `migrations/0001_usage_events.sql` (The Blueprint for the Vault)
**Analogy:** The physical architectural blueprint given to Postgres instructing it on how to build the `usage_events` table inside the database.
**What it holds:**
- Raw SQL `CREATE TABLE IF NOT EXISTS usage_events (...)`.

---

## 📂 11. The Configuration Files
### `buf.gen.yaml` (The Robot Factory Manual)
**Analogy:** The instructions for the `buf` tool on how to generate the Go code from the `.proto` files.

### `go.mod` & `go.sum` (The Gym's Supply Chain)
**Analogy:** The manifest of all external suppliers the Gym relies on.
**What it holds:**
- Links and exact version numbers to external code like `github.com/redis/go-redis/v9` (The Bouncer's whiteboard brand) and `github.com/golang-jwt/jwt/v5` (The ID Checker's UV light manufacturer). 

---
*Every line of code in this project serves one of these distinct roles. By separating them cleanly, you have achieved a modular, production-grade Architecture!*
