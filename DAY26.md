# Day 26 — Orchestration: Docker Compose & The Observability Stack

**Concept Mastered:** Local Infrastructure Orchestration & Service Discovery.
**Built:** `docker-compose.yml`, Prometheus configuration, and Grafana dashboard provisioning.

---

## 1. The Goal: One Command to Rule Them All

Up until now, starting the AI Gateway meant running Redis in one terminal, Postgres in another, and compiling the Go router in a third. If you wanted metrics, you had to manually start Prometheus and Grafana.

This is fine for prototyping, but a nightmare for onboarding new engineers or testing the full system locally.

**The Solution:** `docker-compose.yml`. With a single command (`docker-compose up --build`), the entire microservice architecture spins up, connects to each other, and becomes ready to serve traffic.

## 2. Expert Architecture Decisions

### A. Healthchecks as Dependency Gates
You can't just start the Router at the exact same millisecond as Postgres and Redis. If the databases aren't ready to accept connections, the Router will panic and crash on startup.

**The Amateur Approach:** Write a bash `sleep 5` script before starting the Router.
**The Expert Approach:** Implement native Docker healthchecks.
```yaml
healthcheck:
  test: ["CMD-SHELL", "pg_isready -U postgres -d router"]
  interval: 5s
```
Then, the Router's `depends_on` block is configured to wait specifically for `condition: service_healthy`. The Router mathematically cannot start until the databases are fully online.

### B. Internal Service Discovery (DNS)
When running locally, your Go app connected to `localhost:6379` for Redis. Inside Docker Compose, `localhost` means "this specific container".

Docker Compose automatically creates an internal DNS network. The `REDIS_ADDR` environment variable is updated to `redis:6379`. The hostname `redis` automatically resolves to the internal IP of the Redis container.

### C. Grafana Provisioning (Infrastructure as Code)
Manually clicking through Grafana to add Prometheus as a data source and importing a JSON dashboard is tedious and not version-controlled.

Instead, I used Grafana's **Provisioning** feature. By mounting YAML config files into `/etc/grafana/provisioning/` and the dashboard JSON into `/var/lib/grafana/dashboards`, Grafana boots up fully pre-configured. The dashboard is instantly available without a single click.

## 3. How to Test It

1. Shut down any loose Redis/Postgres containers running on your host.
2. Run `docker-compose up --build -d`
3. Generate a token: `TOKEN=$(go run ./cmd/keygen test-tenant | grep "eyJ")`
4. Send a request: `curl -i http://localhost:8081/v1/chat/completions -H "Authorization: Bearer $TOKEN"`
5. Visit **http://localhost:3000** (Grafana). You don't even need to log in (anonymous auth is enabled). The dashboard is right there, completely wired up to Prometheus!

## 4. What to Say Out Loud

*"Managing local dependencies for a microservice architecture quickly becomes a bottleneck. I implemented a docker-compose stack that completely orchestrates the Go Router, Postgres, Redis, Prometheus, and Grafana. Crucially, I used native Docker healthchecks to enforce strict startup ordering, ensuring the Go binary never panics due to a database that hasn't finished booting. Finally, I treated the Grafana dashboards as Infrastructure-as-Code, using provisioning YAMLs so the entire observability suite boots up fully pre-configured without any manual UI clicks."*
