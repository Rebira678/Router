# Day 23 — Grafana Dashboard Design

**Concept mastered:** the RED method (Rate, Errors, Duration) as a
dashboard design discipline, and why observability config lives on disk
as provisioned files, not manual UI clicks.
**Built so far:** `deploy/prometheus/prometheus.yml` (scrape config),
`deploy/grafana/provisioning/...` (datasource + dashboard auto-loading).
**Waiting on:** your `internal/telemetry/metrics.go` content, so the
dashboard's actual panel queries reference metric names that really
exist rather than guessed ones.

---

## 1. The scenario: a dashboard is not "graphs of whatever's available"

A dashboard with 15 panels showing everything you could measure is worse
than one with 4 panels showing what you'd actually look at during an
incident. The RED method forces that discipline for any request-driven
service:

- **Rate** — requests per second. Is traffic normal, spiking, or dead?
- **Errors** — what fraction of requests are failing? A rate graph alone
  can't tell you if the service is healthy.
- **Duration** — latency, as percentiles (p50/p95/p99), not just an
  average. An average hides the fact that 1% of users are waiting 30
  seconds while everyone else gets sub-second responses.

For Router specifically, this needs answering twice: inbound (client to
Router) and upstream (Router to provider) — a slow upstream and a slow
Router are different problems requiring different fixes.

## 2. Why provisioning files, not clicking through the UI

Configuring a datasource by hand in Grafana's UI works, right up until
the container gets recreated (a deploy, a crash, `docker-compose down`)
and that configuration vanishes with it. Provisioning files are read from
disk on every startup — the dashboard is defined by version-controlled
files sitting in your repo, not by clicks nobody remembers making. Same
principle as Day 10's migration `.sql` file versus manually running
`CREATE TABLE` once and never writing it down.

## 3. Running it today (standalone, before Day 26's docker-compose)

```bash
docker run -d --name prometheus -p 9091:9090 \
  -v $(pwd)/deploy/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml \
  prom/prometheus

docker run -d --name grafana -p 3000:3000 \
  -v $(pwd)/deploy/grafana/provisioning:/etc/grafana/provisioning \
  grafana/grafana
```

Prometheus's own UI is mapped to host port 9091 here, not 9090, because
your Router process is already using 9090 for its own /metrics endpoint
on your host machine.

Visit `http://localhost:9091/targets` — the `router` job should show
`UP`. If it shows `DOWN` on Linux, add
`--add-host=host.docker.internal:host-gateway` to the Prometheus
`docker run` command — a known Linux-specific Docker DNS quirk.

Visit `http://localhost:3000` (login `admin`/`admin`) — Prometheus should
already be configured as a data source with zero manual clicking.

## 4. What's next, once you paste `metrics.go`

The dashboard JSON itself — panels for request rate split by status
class, error rate as a percentage, latency percentiles using your custom
buckets, and upstream-specific versions of each so a slow provider is
visually distinguishable from a slow Router. Exact PromQL queries need
your real metric and label names to be correct rather than guessed.