# Day 20: Structured Logging & Distributed Correlation

Today we evolved the Router from emitting raw text logs to a fully structured, context-aware observability pipeline. In distributed systems, tracing a single request across multiple microservices (or proxy hops) is impossible without correlation IDs.

## The Architecture Decision
We replaced standard unstructured logging with Go's `log/slog` combined with a custom `ContextHandler`. 

### Why a Custom `slog.Handler`?
By default, developers tend to manually attach variables to logs:
`slog.Info("msg", "req_id", id)`

This approach is error-prone. If a developer forgets to add the `req_id` in one file, the trace is broken. 
Instead, we built the `internal/logger.ContextHandler` which wraps the standard JSON logger. This handler automatically intercepts every log record, checks the `context.Context` for a Request ID, and automatically injects `"req_id": "req-xyz..."` into the JSON payload. 

Now, business logic can simply call:
`slog.InfoContext(ctx, "proxy: request forwarded")`
And the correlation ID is guaranteed to be present.

### The Request ID Middleware
We built `internal/requestid.Middleware` which sits at the very outer edge of our HTTP chain (before rate limiting, before auth). 
1. It looks for `X-Request-ID` from the incoming client (allowing clients to trace their own calls).
2. If absent, it generates a cryptographically secure random ID.
3. It injects this ID into the Go `context.Context`.
4. It explicitly sets `X-Request-ID` on the **downstream response** and the **upstream proxied request**, ensuring the external LLM provider's logs can also be tied back to our internal proxy logs.

## Senior-Level Tradeoffs
- **Coupling Context to Logging:** The tradeoff of our design is that all helper functions (like `streamSSE` and `writeDownstreamResponse`) must now accept a `context.Context` argument just to support logging. While this slightly bloats function signatures, passing `ctx` is an idiomatic Go best practice that enables timeouts, tracing, and logging system-wide.
- **JSON vs Text:** We switched to `slog.NewJSONHandler`. While harder for a human to read raw in a terminal, JSON logs are instantly ingestible by modern APM tools (Datadog, ELK, Grafana Loki) to allow querying by `req_id`.
