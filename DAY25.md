# Day 25 — Docker Multi-Stage Builds

**Concept mastered:** why shipping a build toolchain inside a runtime
image is wasteful, and the specific mechanism (COPY --from=builder) that
discards it.
**Built:** `Dockerfile` (production, multi-stage),
`Dockerfile.naive-for-comparison` (deliberately bad, comparison only),
`.dockerignore`.

---

## 1. The scenario

Mailing someone a sandwich by shipping them the whole kitchen. The Go
compiler, the module cache, your full source tree — none of it is needed
to run the compiled binary, only to produce it. A naive Dockerfile ships
all of it anyway, because nothing tells Docker "throw away everything
except the one file I actually need."

## 2. The mechanism: two FROM lines, one COPY --from

```dockerfile
FROM golang:1.22-alpine AS builder
...
RUN go build -o /router ./cmd/router

FROM alpine:3.19
COPY --from=builder /router .
```

Everything before the second FROM — the ~800MB+ builder image, your
full source tree, the module download cache — simply doesn't exist in
the final image. COPY --from=builder reaches back into the discarded
stage for exactly one file. This one instruction is the entire mechanism.

## 3. Three decisions worth being able to defend

- **CGO_ENABLED=0** — forces a fully static binary. Without it, Go may
  dynamically link against glibc (present in the builder), which then
  fails at runtime on Alpine's musl-based final image — a confusing
  "not found" error unrelated to any actually-missing file.
- **alpine, not scratch, for the final stage** — scratch would be even
  smaller, but has no shell for emergency debugging and, critically, no
  CA certificates — which silently breaks any real HTTPS call the
  instant Router talks to an actual LLM provider instead of the mock.
- **-ldflags="-s -w"** — strips symbol table and debug info, typically
  cutting binary size 20-30%. Trade-off: no direct debugger attachment
  to this exact binary. Fine for production; keep a separate debug build
  if needed.

## 4. Getting your real before/after numbers

```bash
docker build -f Dockerfile.naive-for-comparison -t router:naive .
docker images router:naive

docker build -f Dockerfile -t router:optimized .
docker images router:optimized
```

Compare the SIZE column. Delete the naive image and its Dockerfile
afterward — it was only ever for this comparison.

## 5. What to say out loud in 60 seconds

*"A naive Dockerfile for a Go service ships the entire build toolchain in
the runtime image, because nothing tells Docker to discard it. A
multi-stage build compiles in one stage with the full toolchain, then a
second stage only copies out the one compiled binary via COPY
--from=builder — everything from the first stage is discarded. I also
had to set CGO_ENABLED=0, because without it the binary can end up
dynamically linked against glibc from the build stage, which then fails
to run on Alpine's musl-libc final image."*

## 6. What's deliberately not here yet

- No docker-compose.yml tying Router together with Redis, Postgres,
  Prometheus, and Grafana — that's Day 26.
- EXPOSE is documentation, not enforcement — real network isolation for
  admin/telemetry ports still needs orchestrator-level config.
- No non-root USER directive in the final image yet — a real, common
  hardening gap worth revisiting.