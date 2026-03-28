package http

import (
	"log/slog"

	"github.com/partyzanex/padmark/internal/usecases/notes"
)

// Handler holds dependencies for all HTTP handlers.
type Handler struct {
	manager *notes.Manager
	log     *slog.Logger
	pinger  Pinger
}

// NewHandler creates a Handler with required dependencies.
func NewHandler(manager *notes.Manager, log *slog.Logger) *Handler {
	return &Handler{manager: manager, log: log}
}

// WithPinger attaches a readiness probe to the handler.
func (h *Handler) WithPinger(p Pinger) *Handler {
	h.pinger = p
	return h
}
