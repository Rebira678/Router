package retriever

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// searchRequest is the JSON body expected from the client for RAG search.
type searchRequest struct {
	Query string `json:"query"`
	TopK  int32  `json:"top_k,omitempty"`
}

// searchResultJSON is a single search result in the REST response.
type searchResultJSON struct {
	DocumentID string  `json:"document_id"`
	ChunkIndex int32   `json:"chunk_index"`
	ChunkText  string  `json:"chunk_text"`
	Score      float32 `json:"score"`
}

// searchResponseJSON is the complete REST response returned to the client.
type searchResponseJSON struct {
	Results   []searchResultJSON `json:"results"`
	Answer    string             `json:"answer"`
	Telemetry telemetryPayload   `json:"telemetry"`
}

type telemetryPayload struct {
	LatencyMs   float64 `json:"latency_ms"`
	ResultCount int     `json:"result_count"`
	Relevant    bool    `json:"relevant"`
	Source      string  `json:"source"`
}

// Handler returns an http.HandlerFunc that accepts RAG search requests,
// forwards them to the Retriever gRPC backend, and writes a JSON response.
//
// This handler is designed to slot directly into the Router's middleware chain,
// meaning the request has already been authenticated, rate-limited, and metered
// by the time it reaches this function.
func Handler(client *Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req searchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Query == "" {
			http.Error(w, "query cannot be empty", http.StatusBadRequest)
			return
		}

		if req.TopK <= 0 {
			req.TopK = 5
		} else if req.TopK > 100 {
			req.TopK = 100
		}

		start := time.Now()
		resp, err := client.Search(r.Context(), req.Query, req.TopK)
		elapsed := time.Since(start)

		if err != nil {
			slog.ErrorContext(r.Context(), "retriever handler: search failed", "error", err)
			http.Error(w, "RAG search service unavailable", http.StatusBadGateway)
			return
		}

		// Build the REST response
		results := make([]searchResultJSON, 0, len(resp.Results))
		for _, res := range resp.Results {
			results = append(results, searchResultJSON{
				DocumentID: res.DocumentId,
				ChunkIndex: res.ChunkIndex,
				ChunkText:  res.ChunkText,
				Score:      res.Score,
			})
		}

		// Construct a human-readable answer from the top result
		var answerText string
		isRelevant := len(results) > 0

		if isRelevant {
			topChunk := results[0].ChunkText
			if len(topChunk) > 300 {
				topChunk = topChunk[:300]
			}
			answerText = fmt.Sprintf(
				"Based on the RAG pipeline retrieval, I found %d relevant chunks. The top result is from document '%s': \"%s...\"",
				len(results),
				results[0].DocumentID,
				topChunk,
			)
		} else {
			answerText = "I don't have specific information about that topic in my knowledge base."
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(searchResponseJSON{
			Results: results,
			Answer:  answerText,
			Telemetry: telemetryPayload{
				LatencyMs:   float64(elapsed.Milliseconds()),
				ResultCount: len(results),
				Relevant:    isRelevant,
				Source:      "retriever-rag-pipeline",
			},
		})
	}
}
