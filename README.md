# AI Inference Gateway (Router v1.0) 🚀

A production-grade AI Inference Gateway and Proxy built in **Go**. This system sits between user-facing applications and upstream LLM providers (e.g., OpenAI, Anthropic) to provide enterprise-level resilience, security, and observability.

## 🌟 Why this exists

When building AI applications, exposing raw LLM endpoints directly to frontends or microservices is dangerous. A sudden spike in traffic can cause runaway API costs, and if the primary LLM provider experiences an outage, your entire application goes down. 

This Gateway solves those problems by acting as a highly resilient reverse proxy. It enforces distributed rate-limits, tracks token usage per tenant, and automatically fails over to backup providers when outages occur.

## 🏗️ Architecture & Features

This project was built from scratch utilizing standard library primitives where possible, with minimal external dependencies.

* **Distributed Rate Limiting:** Implements an atomic Token Bucket algorithm using Redis Lua scripts. This protects upstream LLMs from tenant-level DDoS attacks or runaway cost spikes, correctly returning `429 Too Many Requests`.
* **Circuit Breaker & Automatic Failover:** If a primary LLM hangs or crashes, the proxy enforces a strict `ResponseHeaderTimeout`. Once the circuit trips, it seamlessly routes the payload to a configured secondary/fallback LLM, preventing user-facing `503` outages.
* **Concurrency & Resource Control:** Bounded worker pools (`sync.Cond` and channels) prevent Goroutine leaks and ensure the server degrades gracefully under extreme load rather than OOM-crashing.
* **Observability:** Injects `X-Request-Id` correlation headers, uses structured logging (`slog`), and exports RED (Rate, Errors, Duration) metrics to **Prometheus**. 
* **Data Persistence:** Tenant billing and usage tracking are securely written to **PostgreSQL**.
* **Live Visualizer UI:** Includes a custom **React + Vite** dashboard to visually demonstrate proxy resilience (simulating rate-limits and forcing upstream failures).

## 🚀 Getting Started

The entire backend infrastructure is containerized. You can spin up the Router, Mock LLM servers, Redis, Postgres, Prometheus, and Grafana with a single command.

### 1. Start the Backend Infrastructure
```bash
docker compose up --build -d
```

### 2. Start the Visualizer Dashboard
The repository includes a front-end UI that allows you to interact with the Gateway and simulate network failures in real-time.
```bash
cd ui
npm install
npm run dev
```

Visit `http://localhost:5173` in your browser.

## 🧪 Testing the Proxy Resilience

The Visualizer UI was built specifically to test the backend's resilience mechanisms:

1. **Test the Happy Path:** Type a message and hit send. Notice the `200 OK` and sub-50ms latency as the request routes to `mock-primary`.
2. **Test the Rate Limiter (Redis):** Click the **"⚡ Spam Requests (Test 429)"** button. This blasts 15 concurrent requests to the Gateway. The token bucket capacity is 10, so you will visually see 10 requests succeed and 5 requests instantly get blocked with `429 Too Many Requests`.
3. **Test the Circuit Breaker (Failover):** Toggle the **"Simulate Upstream Failure"** switch. This injects a header forcing the primary LLM to hang. Watch the Gateway respect its 5-second context timeout, trip the circuit breaker, and seamlessly route your request to `mock-secondary` with a `200 OK`.

## 📈 Monitoring

Grafana is automatically provisioned with the Docker stack.
* **Grafana:** `http://localhost:3000` (Visualize request latency, upstreams, and circuit states)
* **Prometheus:** `http://localhost:9090`
* **Go pprof:** `http://localhost:9095/debug/pprof` (Exposed for memory/goroutine profiling)

## 🛠️ Tech Stack
* **Core:** Go (1.23+)
* **State & Rate Limiting:** Redis
* **Storage:** PostgreSQL
* **Observability:** Prometheus, Grafana
* **UI:** React, Vite, Tailwind CSS, Framer Motion
