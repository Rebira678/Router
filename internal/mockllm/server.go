// Package mockllm implements a tiny stand-in for an OpenAI/Anthropic-style
// LLM API. Router's whole job on Day 1 is to sit in front of something like
// this and forward requests to it, so we need a believable upstream to point
// at before we've earned the right to talk to a real provider.

package mockllm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// completionResponse mimics the shape of a non-streaming chat completion.
// We are not trying to match any real provider's schema exactly here —
// just enough structure that Router's proxying logic has real JSON to move.

type completionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index   int    `json:"index"`
		Message string `json:"message"`
	} `json:"choices"`
	Upstream string `json:"upstream"` // which mock instance answered - useful once we add failover in week 3

}

// NewServer builds an *http.Server representing a fake LLM provider.
// It deliberately does three things a real provider does that a naive
// "return 200 OK" stub would not:
//  1. It echoes back request headers it received, so you can prove in
//     Postman/curl that the proxy actually forwarded them.
//  2. It has an artificial latency knob, so Day 3 (timeouts) and Day 15
//     (retries/backoff) have something real to react to.
//  3. It logs every request it receives, independently of Router's own
//     logs — this lets you verify "did the proxy actually reach me"
//     as a separate fact from "did the proxy report success".

func NewServer(addr string, name string, artificialDelay time.Duration) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		slog.Info("mockllm:recieved request",
			"upstream", name,
			"method", r.Method,
			"path", r.URL.Path,
			"authorization_present", r.Header.Get("Authorization") != "",
			"x_forwarded_for", r.Header.Get("X-Forwarded-For"),
		)

		if artificialDelay > 0 {
			time.Sleep(artificialDelay)
		}

		resp := completionResponse{
			ID:       fmt.Sprintf("mockcmpl-%d", time.Now().UnixNano()),
			Model:    "mock-llm-v1",
			Created:  time.Now().Unix(),
			Upstream: name,
		}

		resp.Choices = []struct {
			Index   int    `json:"index"`
			Message string `json:"message"`
		}{
			{Index: 0, Message: "This is a canned from the mock LLM upstream"},
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Mock-Upstream", name)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}
