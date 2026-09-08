# ==============================================================================
# Build Stage — full Go toolchain, discarded entirely from the final image.
# ==============================================================================
FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /router ./cmd/router

# ==============================================================================
# Runtime Stage — minimal Alpine with CA certs only.
# ==============================================================================
FROM alpine:3.20

RUN apk --no-cache add ca-certificates \
    && addgroup -S router \
    && adduser -S router -G router

WORKDIR /home/router
COPY --from=builder /router .
COPY migrations/ ./migrations/

# Drop to non-root. This is a real hardening step that most tutorials skip.
# Without it, a container escape gives the attacker uid 0 on the host kernel.
USER router

EXPOSE 8081 9092 9095
CMD ["./router"]
