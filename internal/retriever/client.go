// Package retriever provides a gRPC client for the Retriever RAG Search Service.
//
// This client is used by the Router to forward /v1/search requests to the
// Retriever backend, allowing the Router's full middleware stack (JWT auth,
// rate limiting, circuit breaker, usage tracking) to protect the RAG pipeline.
package retriever

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	searchv1 "github/rebik/pkg/api/search/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Client wraps a gRPC connection to the Retriever SearchService.
// It includes connection-level health tracking for the Router's
// circuit breaker integration.
type Client struct {
	conn         *grpc.ClientConn
	searchClient searchv1.SearchServiceClient
	addr         string
	healthy      atomic.Bool
}

// NewClient establishes a gRPC connection to the Retriever backend
// at the given address (e.g., "localhost:50051").
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second, // Ping the server every 10s if idle
			Timeout:             5 * time.Second,  // Wait 5s for ping ack before considering dead
			PermitWithoutStream: true,             // Send pings even without active RPCs
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("retriever: failed to connect to %s: %w", addr, err)
	}

	c := &Client{
		conn:         conn,
		searchClient: searchv1.NewSearchServiceClient(conn),
		addr:         addr,
	}
	c.healthy.Store(true)

	slog.Info("retriever: gRPC client connected", "addr", addr)
	return c, nil
}

// Search sends a query to the Retriever's RAG pipeline and returns
// the semantically ranked results from the Hybrid RRF search.
func (c *Client) Search(ctx context.Context, query string, topK int32) (*searchv1.SearchResponse, error) {
	start := time.Now()

	resp, err := c.searchClient.Search(ctx, &searchv1.SearchRequest{
		Query: query,
		TopK:  topK,
	})

	elapsed := time.Since(start)

	if err != nil {
		c.healthy.Store(false)
		slog.WarnContext(ctx, "retriever: search RPC failed",
			"error", err,
			"addr", c.addr,
			"elapsed_ms", elapsed.Milliseconds(),
		)
		return nil, fmt.Errorf("retriever: search failed: %w", err)
	}

	c.healthy.Store(true)
	slog.InfoContext(ctx, "retriever: search RPC succeeded",
		"addr", c.addr,
		"result_count", len(resp.Results),
		"elapsed_ms", elapsed.Milliseconds(),
	)

	return resp, nil
}

// IsHealthy returns the last known health state of the Retriever connection.
// Used by the Router's readiness probes and circuit breaker logic.
func (c *Client) IsHealthy() bool {
	return c.healthy.Load()
}

// Close gracefully shuts down the gRPC connection.
func (c *Client) Close() error {
	slog.Info("retriever: closing gRPC connection", "addr", c.addr)
	return c.conn.Close()
}
