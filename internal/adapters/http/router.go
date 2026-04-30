package http

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
)

type contextKey uint8

const (
	keyRequestID contextKey = 1
	keyNonce     contextKey = 2
)

func nonceFromContext(ctx context.Context) string {
	v, _ := ctx.Value(keyNonce).(string)
	return v
}

const protoHTTPS = "https"

// RouterOptions holds configurable parameters for the middleware stack.
type RouterOptions struct {
	TrustedProxies []*net.IPNet
	CookieMaxAge   int
	MaxBodyBytes   int
	RateLimit      int
	RateBurst      int
}

// NewRouter registers all routes and wraps them with middleware.
func NewRouter(
	handler *Handler, ogenHandler *OgenHandler, tokens []string, opts RouterOptions,
) http.Handler {
	ogenSrv, err := ogenapi.NewServer(ogenHandler)
	if err != nil {
		panic("ogen server: " + err.Error())
	}

	handler.WithAuth(tokens)

	mux := http.NewServeMux()
	lockout := newNoteFailLockout()

	// Ogen-handled JSON API routes
	mux.Handle("POST /notes", ogenSrv)
	mux.Handle("PUT /notes/{id}", withFailLockout(lockout, ogenSrv))
	mux.Handle("DELETE /notes/{id}", withFailLockout(lockout, ogenSrv))
	mux.Handle("GET /healthz", ogenSrv)
	mux.Handle("GET /readyz", ogenSrv)

	// Manual handlers: HTML pages + content-negotiated GET + login + static + api docs
	mux.HandleFunc("GET /login", handler.LoginPage)
	mux.HandleFunc("POST /login", loginHandler(makeTokenSet(tokens), opts.CookieMaxAge))
	mux.HandleFunc("GET /api", handler.APIDocsPage)
	mux.HandleFunc("GET /api/openapi.yaml", APISpec)
	mux.HandleFunc("GET /", handler.IndexPage)
	mux.HandleFunc("GET /success", handler.SuccessPage)
	mux.Handle("GET /static/", withStaticCacheControl(StaticHandler))
	mux.HandleFunc("GET /notes/{id}", handler.GetNote)
	mux.HandleFunc("GET /edit/{id}", handler.EditPage)
	mux.HandleFunc("GET /{id}", handler.GetNote)

	stack := withRecovery(handler.log, mux)
	stack = newAuthMiddleware(tokens, stack)

	stack = withBodyLimit(int64(opts.MaxBodyBytes), stack)

	if opts.RateLimit > 0 {
		stack = withRateLimit(opts.RateLimit, opts.RateBurst, opts.TrustedProxies, stack)
	}

	stack = withSecurityHeaders(stack)
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

func withBodyLimit(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.ContentLength != 0 {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}

		next.ServeHTTP(w, r)
	})
}

func withStaticCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		next.ServeHTTP(w, r)
	})
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [16]byte

		_, err := rand.Read(b[:])
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		nonce := base64.StdEncoding.EncodeToString(b[:])
		r = r.WithContext(context.WithValue(r.Context(), keyNonce, nonce))

		header := w.Header()
		header.Set("Cache-Control", "private, no-store")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		header.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'nonce-"+nonce+"' https://cdn.redoc.ly; "+
				"style-src 'self' 'nonce-"+nonce+"' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"img-src 'self' data:; "+
				"connect-src 'self'",
		)

		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == protoHTTPS {
			header.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
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
