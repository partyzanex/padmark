package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type contextKey uint8

const keyRequestID contextKey = 1

// NewRouter registers all routes and wraps them with request-ID, logging, and recovery middleware.
func NewRouter(h *Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /notes", h.CreateNote)
	mux.HandleFunc("GET /notes/{id}", h.GetNote)
	mux.HandleFunc("PUT /notes/{id}", h.UpdateNote)
	mux.HandleFunc("DELETE /notes/{id}", h.DeleteNote)
	mux.HandleFunc("GET /healthz", h.Healthz)
	mux.HandleFunc("GET /readyz", h.Readyz)

	return withRequestID(withLogging(h.log, withRecovery(h.log, mux)))
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyRequestID, id)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (sr *statusRecorder) WriteHeader(status int) {
	if !sr.written {
		sr.status = status
		sr.written = true
	}

	sr.ResponseWriter.WriteHeader(status)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.written {
		sr.status = http.StatusOK
		sr.written = true
	}

	return sr.ResponseWriter.Write(b)
}

func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		log.InfoContext(r.Context(), "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
		)
	})
}

func withRecovery(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				log.ErrorContext(r.Context(), "panic recovered", "panic", p)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}
