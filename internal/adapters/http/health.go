package http

import (
	"net/http"
)

// Healthz handles GET /healthz — always returns 200.
func (h *Handler) Healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// Readyz handles GET /readyz — returns 200 if the database is reachable.
func (h *Handler) Readyz(w http.ResponseWriter, r *http.Request) {
	if h.pinger == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	err := h.pinger.PingContext(r.Context())
	if err != nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
}
