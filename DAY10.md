# Day 11 — Composable Middleware: Auth → Rate-Limit → Usage-Tracking

**Concept mastered:** Standardizing the HTTP middleware pattern in Go to compose orthogonal concerns (authentication, rate-limiting, and usage tracking) cleanly, without coupling them to the core business logic.
**Built:** `internal/middleware` package (`Middleware` signature, `Chain` function), `internal/identity/middleware.go` (extracts identity to context), `internal/usage/middleware.go` (records async events to Postgres), and refactored `internal/ratelimit/middleware.go` to conform to the standard pattern.

---

## 1. The scenario: The "Nesting Dolls" problem

Look at how `main.go` built up the request handler previously:
```go
rateLimitedHandler := ratelimit.Middleware(boundedHandler, limiter, ratelimit.KeyFromAuthHeader)
```
This approach hardcoded the `next` handler directly into the function arguments. If we added two more layers (auth and usage-tracking), we'd end up wrapping variables manually in a brittle, deeply nested chain (`usageMw(rateLimitMw(authMw(boundedHandler)))`). Each layer would have to know exactly what to wrap next. It's like a chain of nesting dolls where you have to know the name of the doll directly inside you to nest correctly — brittle, and it gets uglier every time you add a layer.

Today fixes the *shape* of how middlewares compose — not by rewriting what each one does, but by giving them all the exact same signature so they can be listed and combined generically, the way real production Go services do it.

## 2. The design decision: The Standard Go Middleware Pattern

We introduced a standard type:
```go
type Middleware func(http.Handler) http.Handler
```
Instead of taking `next` as an argument alongside its dependencies (like the Redis limiter or Postgres store), a middleware factory now takes *only* its dependencies and returns a function that takes `next`. 

This enables a `Chain` function that iterates through a slice of middlewares and wraps them dynamically. `Chain(handler, m1, m2, m3)` produces `m1(m2(m3(handler)))`. The outermost middleware executes first.

## 3. The Execution Order: Outside-In

The order in which middlewares are applied is critical. We composed our chain in `main.go` as:
`middleware.Chain(boundedHandler, authMw, rateLimitMw, usageMw)`

Here is exactly what happens on every request:
1. **Auth (Outermost)**: Extracts the `Authorization` header and places the identity into the request's `context.Context`. It must run first because downstream layers rely on knowing *who* is calling.
2. **Rate-Limit**: Retrieves the identity from the context and checks Redis. If the bucket is empty, it rejects the request immediately. It does not call `next`. This protects the system from doing unnecessary work (and protects the billing ledger from recording empty usage).
3. **Usage-Tracking**: Calls `next.ServeHTTP(w, r)` *first*. It waits for the proxy to finish its work and stream the response back to the client. Once the request is fully completed, it reads the identity from the context and fires off a record to Postgres.
4. **Proxy (`boundedHandler`)**: The actual core business logic that forwards the request to the mock LLM.

## 4. Context as the Communication Channel

Notice how the layers talk to each other. The `ratelimit` and `usage` packages do not parse the `Authorization` header themselves anymore. Instead, they call `identity.FromContext(r.Context())`. 

This decouples the *extraction* of identity from its *usage*. On Day 12, when we replace the naive header check with real JWT validation, we only have to change the `Auth` middleware. The rate limiter and usage tracker will continue to pull the identity from the context, completely unaware that the underlying auth mechanism changed.

## 5. Backgrounding Usage Tracking (Context Cancellation)

Inside the `Usage-Tracking` middleware, we pass a `contextWithoutCancel(r.Context())` to `store.Record()`.
When an HTTP request finishes, the Go HTTP server automatically cancels the request's context. If we used that canceled context to make a database call after the request returned, the Postgres query would instantly fail with `context canceled`. By wrapping it in a custom context that masks the cancellation signal, we ensure the billing record always gets saved even if the client disconnects the millisecond the proxy finishes.

## 6. Running it

```bash
# Ensure Redis and Postgres are running from previous days
go run ./cmd/router
```

You can verify the middleware chain by making a request:
```bash
curl -H "Authorization: Bearer test-user-1" http://localhost:8080/v1/chat/completions
```
Look at your Postgres database:
```bash
docker exec -it postgres psql -U postgres -d router -c "SELECT * FROM usage_events;"
```
You will see a new usage row recorded with the hashed identity of `test-user-1`.

## 7. What to say out loud in 60 seconds

*"I refactored the application to use the standard Go middleware pattern (`func(http.Handler) http.Handler`). This allowed us to compose authentication, rate-limiting, and usage-tracking orthogonally using a generic `Chain` function, avoiding brittle nesting. I ordered them so that Auth runs first to inject the identity into the request context. Rate-Limiting runs second to reject abusive traffic early. Usage-Tracking runs third, waiting for the proxy to finish before recording the cost. This decoupling means we can swap out our naive auth for real JWT validation later without touching the rate-limiter or billing code."*

## 8. What's deliberately not here yet

- **Real JWT authentication:** Currently, we just trust the raw `Authorization` header (Day 12 fixes this).
- **Dynamic token counting:** The usage tracker currently hardcodes `10` tokens and a `$0.0001` cost. Parsing the actual LLM response to get real token counts is a future step, as we currently stream the response directly to the client without inspecting the JSON body.
