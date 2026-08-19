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

	"github/rebik/internal/mockllm"
	"github/rebik/internal/proxy"
	"github/rebik/internal/workerpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	const (
		mockAddr  = ":9091"
		proxyAddr = ":8080"
	)

	mockSrv := mockllm.NewServer(mockAddr, "mock-primary", 0)
	go func() {
		slog.Info("mockllm: listening", "addr", mockAddr)
		if err := mockSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("mockllm: server error", "error", err)
			os.Exit(1)
		}
	}()

	upstreamURL, err := url.Parse("http://localhost" + mockAddr)
	if err != nil {
		slog.Error("router: invalid upstream URL", "error", err)
		os.Exit(1)
	}

	proxyHandler := proxy.New(proxy.Target{
		Name:    "mock-primary",
		BaseURL: upstreamURL,
	}, 5*time.Second) // Day 3: hard 5s ceiling on waiting for the upstream

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

	proxySrv := &http.Server{
		Addr:              proxyAddr,
		Handler:           boundedHandler,
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

	_ = proxySrv.Shutdown(ctx)
	_ = mockSrv.Shutdown(ctx)
}
