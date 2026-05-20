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
	keyUser      contextKey = 3
	keyCSRF      contextKey = 4
)

func nonceFromContext(ctx context.Context) string {
	v, _ := ctx.Value(keyNonce).(string)

	return v
}

const protoHTTPS = "https"

// isHTTPS reports whether the request arrived over HTTPS.
// X-Forwarded-Proto is only trusted when the remote address belongs to a trusted proxy;
// spoofed headers from untrusted clients are ignored.
func isHTTPS(r *http.Request, trustedProxies []*net.IPNet) bool {
	if r.TLS != nil {
		return true
	}

	if len(trustedProxies) == 0 {
		return false
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	return isTrustedProxy(host, trustedProxies) && r.Header.Get("X-Forwarded-Proto") == protoHTTPS
}

// RouterOptions holds configurable parameters for the middleware stack.
type RouterOptions struct {
	TrustedProxies []*net.IPNet
	// CSRFSecret is used to HMAC-sign CSRF tokens. When empty a random 32-byte key is generated
	// at startup (tokens are invalidated on restart, which is acceptable for a single-process deploy).
	CSRFSecret   []byte
	CookieMaxAge int
	MaxBodyBytes int
	RateLimit    int
	RateBurst    int
}

func registerRoutes(
	mux *http.ServeMux, handler *Handler, ogenSrv http.Handler,
	tokenSet map[string]struct{}, cookieMaxAge int, trustedProxies []*net.IPNet, csrfSecret []byte,
) {
	lockout := newFailLockoutCache()
	guard := func(next http.HandlerFunc) http.HandlerFunc { return csrfGuard(csrfSecret, next) }

	mux.Handle("POST /notes", ogenSrv)
	mux.Handle("PUT /notes/{id}", withFailLockout(lockout, ogenSrv))
	mux.Handle("DELETE /notes/{id}", withFailLockout(lockout, ogenSrv))
	mux.Handle("GET /healthz", ogenSrv)
	mux.Handle("GET /readyz", ogenSrv)

	mux.HandleFunc("GET /login", handler.LoginPage)

	legacyLogin := withTOTPRateLimit(trustedProxies, loginHandler(tokenSet, cookieMaxAge, trustedProxies))
	mux.HandleFunc("POST /login", guard(legacyLogin))
	mux.HandleFunc("POST /totp-login", guard(withTOTPRateLimit(trustedProxies, handler.TOTPLoginHandler)))
	mux.HandleFunc("POST /logout", guard(handler.LogoutHandler))
	mux.HandleFunc("GET /setup", handler.SetupPage)
	mux.HandleFunc("POST /setup", guard(handler.SetupHandler))
	mux.HandleFunc("GET /admin", handler.AdminPage)
	mux.HandleFunc("POST /admin/invite", guard(handler.AdminInviteHandler))
	mux.HandleFunc("POST /admin/users/{id}/revoke", guard(handler.AdminRevokeHandler))
	mux.HandleFunc("GET /change-password", handler.ChangePasswordPage)
	mux.HandleFunc("POST /change-password", guard(handler.ChangePasswordHandler))
	mux.HandleFunc("GET /api", handler.APIDocsPage)
	mux.HandleFunc("GET /api/openapi.yaml", APISpec)
	mux.HandleFunc("GET /", handler.IndexPage)
	mux.HandleFunc("GET /success", handler.SuccessPage)
	mux.Handle("GET /static/", withStaticCacheControl(StaticHandler))
	mux.HandleFunc("GET /notes/{id}", handler.GetNote)
	mux.HandleFunc("POST /notes/{id}", handler.HandleReveal)
	mux.HandleFunc("GET /edit/{id}", handler.EditPage)
	mux.HandleFunc("GET /{id}", handler.GetNote)
	mux.HandleFunc("POST /{id}", handler.HandleReveal)
}

// NewRouter registers all routes and wraps them with middleware.
func NewRouter(
	handler *Handler, ogenHandler *OgenHandler, opts *RouterOptions,
) http.Handler {
	ogenSrv, err := ogenapi.NewServer(ogenHandler)
	if err != nil {
		panic("ogen server: " + err.Error())
	}

	const csrfSecretSize = 32

	csrfSecret := opts.CSRFSecret
	if len(csrfSecret) == 0 {
		csrfSecret = make([]byte, csrfSecretSize)

		_, randErr := rand.Read(csrfSecret)
		if randErr != nil {
			panic("generate csrf secret: " + randErr.Error())
		}
	}

	handler.WithCSRFSecret(csrfSecret)
	handler.WithTrustedProxies(opts.TrustedProxies)

	tokenSet := handler.AllowedTokens()
	mux := http.NewServeMux()

	registerRoutes(mux, handler, ogenSrv, tokenSet, opts.CookieMaxAge, opts.TrustedProxies, csrfSecret)

	namedRoutes := buildNamedRoutes()

	var checker sessionChecker
	if handler.authMgr != nil {
		checker = handler.authMgr
	}

	stack := withRecovery(handler.log, mux)
	stack = newAuthMiddleware(tokenSet, namedRoutes, checker, stack)
	stack = withCSRFToken(csrfSecret, opts.TrustedProxies, stack)

	stack = withBodyLimit(int64(opts.MaxBodyBytes), stack)

	if opts.RateLimit > 0 {
		stack = withRateLimit(opts.RateLimit, opts.RateBurst, opts.TrustedProxies, stack)
	}

	stack = withSecurityHeaders(opts.TrustedProxies, stack)
	stack = withLogging(handler.log, stack)
	stack = withRequestID(stack)

	return stack
}

// buildNamedRoutes returns the set of single-segment GET path names that are registered
// page routes (not note IDs). Keep in sync with route registrations in NewRouter.
func buildNamedRoutes() map[string]struct{} {
	return map[string]struct{}{
		"login":           {},
		"setup":           {},
		"admin":           {},
		"api":             {},
		"success":         {},
		"healthz":         {},
		"readyz":          {},
		"notes":           {},
		"edit":            {},
		"change-password": {},
	}
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
	if sr.written {
		return
	}

	sr.status = status
	sr.written = true
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

func withSecurityHeaders(trustedProxies []*net.IPNet, next http.Handler) http.Handler {
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

		if isHTTPS(r, trustedProxies) {
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
