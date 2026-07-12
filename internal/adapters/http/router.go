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
	keyLogUser   contextKey = 5
)

// logUser is a request-scoped holder the auth layer fills with the resolved identity so the
// outer logging middleware can report it. A pointer is used because withLogging runs OUTSIDE
// the auth middleware: the user set via r.WithContext downstream is not visible on the logger's
// request, but a shared pointer installed before next() is mutated in place and read after.
type logUser struct {
	name  string
	admin bool
}

// setLogUser records the identity for the request log, if the holder is present.
func setLogUser(ctx context.Context, name string, admin bool) {
	if lu, ok := ctx.Value(keyLogUser).(*logUser); ok {
		lu.name = name
		lu.admin = admin
	}
}

func nonceFromContext(ctx context.Context) string {
	v, _ := ctx.Value(keyNonce).(string)

	return v
}

const protoHTTPS = "https"

// protoHTTP is the non-TLS URL scheme, paired with protoHTTPS when building absolute links.
const protoHTTP = "http"

// requestScheme returns the URL scheme ("http" or "https") to use when building absolute links
// back to this server, honouring the trusted-proxy handling in isHTTPS.
func requestScheme(r *http.Request, trustedProxies []*net.IPNet) string {
	if isHTTPS(r, trustedProxies) {
		return protoHTTPS
	}

	return protoHTTP
}

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
	ForcedScheme   string
	TrustedProxies []*net.IPNet
	CSRFSecret     []byte
	CookieMaxAge   int
	MaxBodyBytes   int
	RateLimit      int
	RateBurst      int
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

	// POST /login is the legacy bearer-token login — no TOTP brute-force concern,
	// so the TOTP-tuned 10 req/min rate limit is not applied here.
	mux.HandleFunc("POST /login", guard(loginHandler(tokenSet, cookieMaxAge, trustedProxies)))
	mux.HandleFunc("POST /totp-login", guard(withTOTPRateLimit(trustedProxies, handler.TOTPLoginHandler)))
	mux.HandleFunc("POST /logout", guard(handler.LogoutHandler))
	mux.HandleFunc("GET /setup", handler.SetupPage)
	mux.HandleFunc("POST /setup", guard(handler.SetupHandler))
	mux.HandleFunc("GET /admin", handler.AdminPage)
	mux.HandleFunc("POST /admin/invite", guard(handler.AdminInviteHandler))
	mux.HandleFunc("POST /admin/users/{id}/revoke", guard(handler.AdminRevokeHandler))
	mux.HandleFunc("POST /admin/api-keys", guard(handler.AdminCreateKeyHandler))
	mux.HandleFunc("POST /admin/api-keys/{id}/revoke", guard(handler.AdminRevokeKeyHandler))
	mux.HandleFunc("GET /change-password", handler.ChangePasswordPage)
	mux.HandleFunc("POST /change-password", guard(handler.ChangePasswordHandler))
	mux.HandleFunc("GET /api", handler.APIDocsPage)
	mux.HandleFunc("GET /api/openapi.yaml", APISpec)
	mux.HandleFunc("GET /", handler.IndexPage)
	mux.HandleFunc("GET /success", handler.SuccessPage)
	mux.Handle("GET /static/", withStaticCacheControl(StaticHandler))
	mux.HandleFunc("GET /notes/{id}", handler.GetNote)
	mux.HandleFunc("POST /notes/{id}", guard(handler.HandleReveal))
	mux.HandleFunc("GET /edit/{id}", handler.EditPage)
	mux.HandleFunc("GET /{id}", handler.GetNote)
	mux.HandleFunc("POST /{id}", guard(handler.HandleReveal))
}

// NewRouter registers all routes and wraps them with middleware.
func NewRouter(
	handler *Handler, ogenHandler *OgenHandler, opts *RouterOptions,
) http.Handler {
	ogenSrv, err := ogenapi.NewServer(ogenHandler, ogenapi.WithErrorHandler(newMaxBodyErrorHandler(handler.log)))
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
	handler.WithForcedScheme(opts.ForcedScheme)

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

	// Second, outermost recovery layer: the inner withRecovery above only covers mux, so a
	// panic in any middleware between it and here (auth, CSRF, rate-limit, security-headers,
	// logging, request-ID) would otherwise fall through to net/http's bare per-connection
	// recovery, which drops the response body and skips structured logging entirely.
	stack = withRecovery(handler.log, stack)

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

// makeTokenSet builds a lookup set from the configured bearer tokens.
//
// Deprecated: part of the legacy bearer-token write auth (PADMARK_AUTH_TOKENS), superseded by
// the TOTP account system (--enable-accounts). Will be removed in a future release.
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

		// Install the identity holder so the (inner) auth layer can record who the request
		// resolved to; it stays anonymous when no auth runs or no valid session is present.
		// Only the request passed downstream carries the holder; the original r.Context() is
		// kept for logging (inherited context, preserves the request ID).
		holder := &logUser{}
		rr := r.WithContext(context.WithValue(r.Context(), keyLogUser, holder))

		next.ServeHTTP(rec, rr)

		level := slog.LevelInfo
		if strings.HasPrefix(r.URL.Path, "/static/") {
			level = slog.LevelDebug
		}

		user := "-"
		if holder.name != "" {
			user = holder.name
		}

		// remote is the immediate peer (the proxy/LB IP when behind one); xff is the
		// X-Forwarded-For chain the proxy sent; user/admin is the resolved identity ("-" when
		// anonymous). These make trusted-proxy and auth debugging possible without shell access.
		log.Log(r.Context(), level, "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
			"remote", r.RemoteAddr,
			"xff", r.Header.Get("X-Forwarded-For"),
			"user", user,
			"admin", holder.admin,
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

// redocScriptSrc is the external origin Redoc is loaded from. It is added to script-src only on
// the API docs page (see APIDocsPage), keeping the global CSP free of third-party origins.
const redocScriptSrc = "https://cdn.redoc.ly"

// buildCSP assembles the Content-Security-Policy for a response. extraScriptSrc adds extra
// allowed script origins (e.g. the Redoc CDN on the /api page) without widening script-src
// globally — every other page keeps script-src restricted to 'self' + nonce.
func buildCSP(nonce string, extraScriptSrc ...string) string {
	scriptSrcParts := append([]string{"script-src 'self' 'nonce-" + nonce + "'"}, extraScriptSrc...)
	scriptSrc := strings.Join(scriptSrcParts, " ")

	return "default-src 'self'; " +
		scriptSrc + "; " +
		"style-src 'self' 'nonce-" + nonce + "' https://fonts.googleapis.com; " +
		"font-src 'self' https://fonts.gstatic.com; " +
		"img-src 'self' data:; " +
		"connect-src 'self'"
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
		header.Set("Content-Security-Policy", buildCSP(nonce))

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
