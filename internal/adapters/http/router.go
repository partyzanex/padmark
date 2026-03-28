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
func NewRouter(handler *Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", handler.IndexPage)
	mux.HandleFunc("GET /success", handler.SuccessPage)
	mux.Handle("GET /static/", StaticHandler)
	mux.HandleFunc("POST /notes", handler.CreateNote)
	mux.HandleFunc("GET /notes/{id}", handler.GetNote)
	mux.HandleFunc("PUT /notes/{id}", handler.UpdateNote)
	mux.HandleFunc("DELETE /notes/{id}", handler.DeleteNote)
	mux.HandleFunc("GET /healthz", handler.Healthz)
	mux.HandleFunc("GET /readyz", handler.Readyz)

	return withRequestID(withLogging(handler.log, withRecovery(handler.log, mux)))
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

	return sr.ResponseWriter.Write(b) //nolint:wrapcheck // pass-through implementation of io.Writer
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
		ctx := r.Context()

		defer func() {
			if p := recover(); p != nil {
				log.ErrorContext(ctx, "panic recovered", "panic", p)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}
