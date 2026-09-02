// Command router is Day 1's entrypoint. It starts two HTTP servers in one
// process for local development convenience:
//
//   - :9090  the mock LLM upstream (internal/mockllm)
//   - :8080  Router's own proxy, forwarding everything to :9090
//
// In any real deployment these are separate processes on separate
// machines — we only co-locate them here so `go run ./cmd/router` gives
// you a complete, testable loop with a single command.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
	"net"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	"github/rebik/internal/identity"
	"github/rebik/internal/logger"
	"github/rebik/internal/middleware"
	"github/rebik/internal/mockllm"
	"github/rebik/internal/proxy"
	"github/rebik/internal/ratelimit"
	"github/rebik/internal/requestid"
	"github/rebik/internal/tenant"
	"github/rebik/internal/usage"
	"github/rebik/internal/workerpool"
	pb "github/rebik/pkg/api/proto/router/v1"
)

func main() {
	baseHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	ctxLogger := slog.New(logger.NewContextHandler(baseHandler))
	slog.SetDefault(ctxLogger)

	const (
		mockAddr  = ":9091"
		proxyAddr = ":8081"
	)

	mockSrv := mockllm.NewServer(mockAddr, "mock-primary", 0)
	go func() {
		slog.Info("mockllm: listening", "addr", mockAddr)
		if err := mockSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("mockllm: server error", "error", err)
			os.Exit(1)
		}
	}()

	mockSecondaryAddr := ":9093"
	mockSecondarySrv := mockllm.NewServer(mockSecondaryAddr, "mock-secondary", 0)
	go func() {
		slog.Info("mockllm: listening (secondary)", "addr", mockSecondaryAddr)
		if err := mockSecondarySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("mockllm: server error", "error", err)
			os.Exit(1)
		}
	}()

	upstreamURL, err := url.Parse("http://localhost" + mockAddr)
	if err != nil {
		slog.Error("router: invalid upstream URL", "error", err)
		os.Exit(1)
	}
	upstreamSecondaryURL, err := url.Parse("http://localhost" + mockSecondaryAddr)
	if err != nil {
		slog.Error("router: invalid secondary upstream URL", "error", err)
		os.Exit(1)
	}

	proxyHandler := proxy.New([]proxy.Target{
		{
			Name:    "mock-primary",
			BaseURL: upstreamURL,
		},
		{
			Name:    "mock-secondary",
			BaseURL: upstreamSecondaryURL,
		},
	}, 5*time.Second, 5, 10*time.Second) // Day 3: hard 5s ceiling, Day 16: threshold 5, cooldown 10s

	// Day 2: wrap the proxy in a bounded worker pool instead of letting
	// every accepted connection call upstream directly. 20 workers is an
	// arbitrary starting number for local dev — the right number in
	// production is derived from upstream capacity and gets revisited
	// once Week 7's load testing gives you real numbers instead of a
	// guess. queueSize=50 means up to 50 requests can wait briefly for a
	// free worker before Router starts returning 503.
	const (
		poolWorkers   = 20
		poolQueueSize = 50
	)
	boundedHandler := workerpool.New(proxyHandler, poolWorkers, poolQueueSize)

	// Day 6: rate limit state now lives in Redis instead of this
	// process's own memory, so the limit holds correctly across
	// multiple Router instances sharing the same Redis — not just
	// within one process. redisAddr defaults to a local instance;
	// override with the REDIS_ADDR env var for anything else.
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr})

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		pingCancel()
		slog.Error("router: cannot reach redis at startup",
			"addr", redisAddr,
			"error", err,
			"hint", "start Redis locally, e.g. `docker run -d -p 6379:6379 redis:7-alpine`",
		)
		os.Exit(1)
	}
	pingCancel()
	slog.Info("redis: connected", "addr", redisAddr)

	const (
		rateLimitCapacity   = 10
		rateLimitRefillRate = 2
	)
	limiter := ratelimit.NewRedisLimiter(redisClient, rateLimitCapacity, rateLimitRefillRate)

	// Day 10: connect to Postgres for usage/billing tracking.
	pgDSN := os.Getenv("POSTGRES_DSN")
	if pgDSN == "" {
		pgDSN = "postgres://postgres:postgres@localhost:5432/router?sslmode=disable"
	}
	usageStore, err := usage.NewStore(pgDSN)
	if err != nil {
		slog.Error("router: cannot open postgres connection", "error", err)
		os.Exit(1)
	}

	// Day 12: Real JWT authentication secret.
	// In production, this must be a securely rotated secret injected via env.
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "super-secret-local-dev-key"
	}
	
	authMw := identity.Middleware(jwtSecret)
	rateLimitMw := ratelimit.Middleware(limiter, func(r *http.Request) string {
		return identity.FromContext(r.Context())
	})
	usageMw := usage.Middleware(usageStore, "mock-llm-v1")

	composedHandler := middleware.Chain(
		boundedHandler,
		requestid.Middleware, // executes first
		authMw,
		rateLimitMw,
		usageMw,
	)

	pgPingCtx, pgPingCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := usageStore.Ping(pgPingCtx); err != nil {
		pgPingCancel()
		slog.Error("router: cannot reach postgres at startup",
			"error", err,
			"hint", "start Postgres locally, e.g. `docker run -d -p 5432:5432 -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=router postgres:16-alpine`, then apply migrations/0001_usage_events.sql",
		)
		os.Exit(1)
	}
	pgPingCancel()
	slog.Info("postgres: connected")

	// Day 13: Internal gRPC API for tenant management
	grpcListener, err := net.Listen("tcp", ":9092")
	if err != nil {
		slog.Error("router: failed to listen for gRPC", "error", err)
		os.Exit(1)
	}
	grpcServer := grpc.NewServer()
	tenantServer := tenant.NewGRPCServer(jwtSecret)
	pb.RegisterTenantServiceServer(grpcServer, tenantServer)

	go func() {
		slog.Info("router: gRPC internal api listening", "addr", ":9092")
		if err := grpcServer.Serve(grpcListener); err != nil {
			slog.Error("router: gRPC server error", "error", err)
		}
	}()

	proxySrv := &http.Server{
		Addr:              proxyAddr,
		Handler:           composedHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("router: listening", "addr", proxyAddr, "forwarding_to", upstreamURL.String())
		if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("router: server error", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM. Not strictly a Day-1 topic,
	// but a server that dies ungracefully makes every later day's
	// testing (especially load testing in Week 7) noisier than it
	// needs to be, so it's cheap to get right now.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("router: shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	grpcServer.GracefulStop()
	_ = proxySrv.Shutdown(ctx)
	_ = mockSrv.Shutdown(ctx)
	_ = mockSecondarySrv.Shutdown(ctx)
	_ = redisClient.Close()
	_ = usageStore.Close()
}
