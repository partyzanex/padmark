package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type contextKey uint8

const keyRequestID contextKey = 1

// NewRouter registers all routes and wraps them with middleware.
// If tokens is non-empty, all routes except /login, /static/, /healthz, /readyz require auth.
// Browser users without a valid cookie are redirected to the login page.
func NewRouter(handler *Handler, tokens []string) http.Handler {
	tokenSet := makeTokenSet(tokens)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /login", handler.LoginPage)
	mux.HandleFunc("POST /login", loginHandler(tokenSet))
	mux.HandleFunc("GET /api", handler.APIDocsPage)
	mux.HandleFunc("GET /api/openapi.yaml", APISpec)
	mux.HandleFunc("GET /", handler.IndexPage)
	mux.HandleFunc("GET /success", handler.SuccessPage)
	mux.Handle("GET /static/", StaticHandler)
	mux.HandleFunc("POST /notes", handler.CreateNote)
	mux.HandleFunc("GET /notes/{id}", handler.GetNote)
	mux.HandleFunc("PUT /notes/{id}", handler.UpdateNote)
	mux.HandleFunc("DELETE /notes/{id}", handler.DeleteNote)
	mux.HandleFunc("GET /healthz", handler.Healthz)
	mux.HandleFunc("GET /readyz", handler.Readyz)
	mux.HandleFunc("GET /edit/{id}", handler.EditPage)
	mux.HandleFunc("GET /{id}", handler.GetNote)

	stack := withRecovery(handler.log, mux)
	stack = newAuthMiddleware(tokens, stack)
	stack = withLogging(handler.log, stack)
	stack = withRequestID(stack)

	return stack
}

func makeTokenSet(tokens []string) map[string]struct{} {
	set := make(map[string]struct{}, len(tokens))

	for _, tok := range tokens {
		set[tok] = struct{}{}
	}

	return set
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

		level := slog.LevelInfo
		if strings.HasPrefix(r.URL.Path, "/static/") {
			level = slog.LevelDebug
		}

		log.Log(r.Context(), level, "http",
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
