package logger

import (
	"context"
	"log/slog"

	"github/rebik/internal/requestid"
)

// ContextHandler wraps a slog.Handler to automatically extract request IDs.
type ContextHandler struct {
	slog.Handler
}

// NewContextHandler creates a handler that injects req_id into all log lines
// if a Request ID is present in the context.
func NewContextHandler(h slog.Handler) *ContextHandler {
	return &ContextHandler{Handler: h}
}

// Handle implements slog.Handler
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	reqID := requestid.FromContext(ctx)
	if reqID != "" {
		r.AddAttrs(slog.String("req_id", reqID))
	}
	return h.Handler.Handle(ctx, r)
}
