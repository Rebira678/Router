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
)

func main() {
	// STEP 1: Set up JSON Logging
	// Configures standard output logging to print structured JSON messages.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Port addresses for both servers running on your machine.
	const (
		mockAddr  = ":9091"
		proxyAddr = ":8080"
	)

	// STEP 2: Initialize & Start Mock LLM Server (:9090)
	// Creates the fake AI server.
	mockSrv := mockllm.NewServer(mockAddr, "mock-primary", 0)

	// Starts mock server in a background routine (goroutine) so it doesn't block the program.
	go func() {
		slog.Info("mockllm: listening", "addr", mockAddr)
		if err := mockSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("mockllm: server error", "error", err)
			os.Exit(1)
		}
	}()

	// STEP 3: Configure Proxy Target URL
	// Converts the string "http://localhost:9090" into a Go URL object.
	upstreamURL, err := url.Parse("http://localhost" + mockAddr)
	if err != nil {
		slog.Error("router: invalid upstream URL", "error", err)
		os.Exit(1)
	}

	// STEP 4: Initialize & Start Reverse Proxy Server (:8080)
	// Passes the upstream URL into the proxy package created in proxy.go.
	proxyHandler := proxy.New(proxy.Target{
		Name:    "mock-primary",
		BaseURL: upstreamURL,
	})

	// Configures the HTTP server for the reverse proxy.
	proxySrv := &http.Server{
		Addr:              proxyAddr,
		Handler:           proxyHandler,
		ReadHeaderTimeout: 5 * time.Second, // Safety timeout for slow header transmissions
	}

	// Starts proxy server in a separate background routine.
	go func() {
		slog.Info("router: listening", "addr", proxyAddr, "forwarding_to", upstreamURL.String())
		if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("router: server error", "error", err)
			os.Exit(1)
		}
	}()

	// STEP 5: Handle Graceful Shutdown
	// Graceful shutdown on SIGINT/SIGTERM. Not strictly a Day-1 topic,
	// but a server that dies ungracefully makes every later day's
	// testing (especially load testing in Week 7) noisier than it
	// needs to be, so it's cheap to get right now.
	//
	// SIMPLE EXPLANATION: Listens for system interrupt signals (e.g. Ctrl+C).
	// When caught, it gives active requests up to 5 seconds to finish before stopping.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop // Program waits here until a shutdown signal is received

	slog.Info("router: shutting down")

	// Gives active connections a 5-second deadline to complete before shutting down hard.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Safely shuts down both HTTP servers.
	_ = proxySrv.Shutdown(ctx)
	_ = mockSrv.Shutdown(ctx)
}
