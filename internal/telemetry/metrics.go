package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ActiveRequests gauges how many requests are currently in flight overall.
	// Used to visualize instantaneous load on the proxy.
	ActiveRequests = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "router_active_requests",
			Help: "Current number of requests in-flight being processed by the proxy.",
		},
	)

	// RequestDuration tracks end-to-end latency histograms for client-facing requests.
	RequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "router_request_duration_seconds",
			Help: "End-to-end duration of HTTP requests in seconds.",
			// LLM inference can be slow, so buckets go from 100ms up to 60s
			Buckets: []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 30.0, 60.0},
		},
		[]string{"status"},
	)

	// UpstreamAttempts counts how many times we try to hit an upstream (including failovers).
	UpstreamAttempts = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "router_upstream_attempts_total",
			Help: "Total upstream HTTP calls made (including retries and failovers).",
		},
		[]string{"target", "status", "reason"},
	)

	// UpstreamDuration tracks the exact latency of the upstream network hop.
	UpstreamDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "router_upstream_duration_seconds",
			Help:    "Latency of the raw upstream LLM network hop.",
			Buckets: []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 30.0},
		},
		[]string{"target"},
	)
)
