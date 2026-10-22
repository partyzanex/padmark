package http

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bluele/gcache"
	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
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
// back to this server: the operator-forced override when set (--public-scheme, see
// Handler.WithForcedScheme), otherwise the auto-detected value from isHTTPS.
func requestScheme(r *http.Request, forcedScheme string, trustedProxies []*net.IPNet) string {
	if forcedScheme != "" {
		return forcedScheme
	}

	if isHTTPS(r, "", trustedProxies) {
		return protoHTTPS
	}

	return protoHTTP
}

// isHTTPS reports whether the request must be treated as HTTPS — the decision behind the
// cookie Secure attribute and HSTS. An operator-forced "https" (--public-scheme) is honoured
// unconditionally: that flag is the recommended setup behind a TLS-terminating load balancer
// whose address is not stable enough for --trusted-proxies, so the request itself arrives as
// plain HTTP with no trustworthy X-Forwarded-Proto. Any other forcedScheme falls back to
// detection — a forced "http" only affects link building and never strips Secure from a
// cookie that really is travelling over TLS. X-Forwarded-Proto is only trusted when the remote
// address belongs to a trusted proxy; spoofed headers from untrusted clients are ignored.
func isHTTPS(r *http.Request, forcedScheme string, trustedProxies []*net.IPNet) bool {
	if forcedScheme == protoHTTPS || r.TLS != nil {
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
	ForcedScheme string
	// BootstrapToken is the operator's proof of server ownership for creating the first admin
	// via /setup without an invite (PADMARK_BOOTSTRAP_TOKEN). Empty keeps that bootstrap closed:
	// otherwise whoever reached /setup first would become the single, permanent admin.
	BootstrapToken string
	TrustedProxies []*net.IPNet
	CSRFSecret     []byte
	// AllowedHosts is the Host header allow-list for the main listener (--allowed-hosts). When
	// non-empty, any other Host is refused with 421 before a handler runs; empty disables the
	// check. See withAllowedHosts.
	AllowedHosts []string
	MaxBodyBytes int
	RateLimit    int
	RateBurst    int
	// SessionTTL sets the TOTP session cookie's Max-Age (see AuthHandler), matching the
	// server-side session lifetime configured via auth.NewManager's sessionTTL — both should be
	// fed the same --session-ttl value so the cookie never outlives (or expires before) the
	// session it represents. Zero falls back to auth.DefaultSessionTTL.
	SessionTTL time.Duration
	// DisableAPI turns off the REST/JSON API surface (/notes*, /api, /api/openapi.yaml): every
	// request reaching the disabling middleware gets 503 with code=api_disabled. The web UI and
	// /healthz, /readyz keep working — see withAPIDisabled.
	DisableAPI bool
	// DirectRedirect selects GET /r/{id}'s response mode: true issues an immediate 301 to the
	// short link's target URL, false renders a transparency interstitial. See LinkHandler.Redirect.
	DirectRedirect bool
}

// RouteRegistrar registers one concern's HTTP routes on mux. Each concern-specific handler
// (NoteHandler, AuthHandler, AdminHandler, PageHandler) implements it, so adding a route to an
// existing concern only touches that concern's own file, and adding a new concern handler means
// implementing this interface and appending it to the registrars slice in NewRouter — neither
// requires editing a shared, ever-growing route-registration function.
type RouteRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// registerOgenRoutes registers the generated REST API routes (note and link CRUD, health checks)
// served directly by ogenSrv. These aren't owned by any concern handler — ogenSrv wraps the
// separate OgenHandler — so they're registered here rather than via a RouteRegistrar. lockout is
// shared with NoteHandler's native {id...} routes (see NoteHandler.RegisterRoutes) so fail-lockout
// tracking is consistent for a note or link regardless of which route variant a client hits — a
// single cache keyed by path ID plus caller works since notes and links share one global slug
// namespace (a note and a link can never have the same ID, see domain.HashSlug/ErrSlugConflict).
func registerOgenRoutes(mux *http.ServeMux, ogenSrv http.Handler, lockout gcache.Cache, log *slog.Logger) {
	mux.Handle("POST /notes", ogenSrv)
	mux.Handle("PUT /notes/{id}", requireSameOrigin(log, withFailLockout(lockout, log, ogenSrv)))
	mux.Handle("DELETE /notes/{id}", requireSameOrigin(log, withFailLockout(lockout, log, ogenSrv)))
	mux.Handle("POST /links", ogenSrv)
	mux.Handle("POST /links/bulk", ogenSrv)
	mux.Handle("GET /links/{id}", ogenSrv)
	mux.Handle("PUT /links/{id}", requireSameOrigin(log, withFailLockout(lockout, log, ogenSrv)))
	mux.Handle("DELETE /links/{id}", requireSameOrigin(log, withFailLockout(lockout, log, ogenSrv)))
	mux.Handle("GET /healthz", ogenSrv)
	mux.Handle("GET /readyz", ogenSrv)
	// Account endpoints and note/link PUT/DELETE accept browser session cookies, so they enforce
	// same-origin requests. POST /notes, POST /links and POST /links/bulk are deliberately not
	// wrapped: they accept only JSON bodies (a cross-site JSON POST forces a CORS preflight the
	// server never answers) and the session cookie is SameSite=Strict.
	mux.Handle("POST /api/tokens", requireSameOrigin(log, ogenSrv))
	mux.Handle("POST /api/invites", requireSameOrigin(log, ogenSrv))
}

// registerAllRoutes registers the ogen-served REST API routes plus every concern handler's own
// routes (see RouteRegistrar) on mux.
func registerAllRoutes(mux *http.ServeMux, handler *Handler, ogenSrv http.Handler) {
	registerOgenRoutes(mux, ogenSrv, handler.lockout, handler.log)

	registrars := []RouteRegistrar{
		handler.NoteHandler,
		handler.LinkHandler,
		handler.AuthHandler,
		handler.AdminHandler,
		handler.PageHandler,
	}
	for _, registrar := range registrars {
		registrar.RegisterRoutes(mux)
	}
}

// NewRouter registers all routes and wraps them with middleware.
//
// Returns an error when building the generated ogen server fails, or when crypto/rand is
// unavailable to generate a CSRF secret (only when opts.CSRFSecret is empty). NewRouter runs
// only at the composition root, so the caller should treat either as a fatal startup error.
func NewRouter(
	handler *Handler, ogenHandler *OgenHandler, opts *RouterOptions,
) (http.Handler, error) {
	// The auth seam is optional on OgenHandler; attach it where it exists so the account
	// endpoints (POST /api/tokens, /api/invites) serve instead of 503-ing.
	if handler.authMgr != nil {
		ogenHandler = ogenHandler.WithAuthManager(handler.authMgr)
	}

	ogenSrv, err := ogenapi.NewServer(ogenHandler, ogenapi.WithErrorHandler(newOgenErrorHandler(handler.log)))
	if err != nil {
		return nil, fmt.Errorf("new ogen server: %w", err)
	}

	const csrfSecretSize = 32

	csrfSecret := opts.CSRFSecret
	if len(csrfSecret) == 0 {
		csrfSecret = make([]byte, csrfSecretSize)

		_, randErr := rand.Read(csrfSecret)
		if randErr != nil {
			return nil, fmt.Errorf("generate csrf secret: %w", randErr)
		}
	}

	handler.WithCSRFSecret(csrfSecret)
	handler.WithTrustedProxies(opts.TrustedProxies)
	handler.WithForcedScheme(opts.ForcedScheme)
	handler.WithSessionTTL(opts.SessionTTL)
	handler.WithDirectRedirect(opts.DirectRedirect)
	handler.WithBootstrapToken(opts.BootstrapToken)

	mux := http.NewServeMux()

	registerAllRoutes(mux, handler, ogenSrv)

	return wrapMiddleware(mux, handler, opts, csrfSecret), nil
}

// wrapMiddleware wraps the routed mux in the server's middleware chain. The listing order is
// innermost-first: the last wrapper applied is the one that sees a request first.
func wrapMiddleware(
	mux http.Handler, handler *Handler, opts *RouterOptions, csrfSecret []byte,
) http.Handler {
	var checker sessionChecker
	if handler.authMgr != nil {
		checker = handler.authMgr
	}

	stack := withRecovery(handler.log, mux)
	stack = withRequestOrigin(opts.ForcedScheme, opts.TrustedProxies, stack)

	if opts.DisableAPI {
		stack = withAPIDisabled(handler.log, stack)
	}

	stack = newAuthMiddleware(buildNamedRoutes(), checker, stack)
	stack = withCSRFToken(csrfSecret, opts.ForcedScheme, opts.TrustedProxies, stack)

	stack = withBodyLimit(int64(opts.MaxBodyBytes), stack)

	if opts.RateLimit > 0 {
		stack = withRateLimit(opts.RateLimit, opts.RateBurst, opts.TrustedProxies, handler.log, stack)
	}

	// Only meaningful when nothing is trusted — see warnUnconfiguredProxy.
	if len(opts.TrustedProxies) == 0 {
		stack = warnUnconfiguredProxy(handler.log, stack)
	}

	// Outside every stateful layer (auth, CSRF, rate limit) so a forged Host is refused before
	// any of them runs, yet inside security headers, logging and the request ID so the refusal
	// is still logged and carries them.
	stack = withAllowedHosts(opts.AllowedHosts, stack)

	stack = withSecurityHeaders(opts.ForcedScheme, opts.TrustedProxies, stack)
	stack = withLogging(handler.log, stack)
	stack = withRequestID(stack)

	// Second, outermost recovery layer: the inner withRecovery above only covers mux, so a
	// panic in any middleware between it and here (auth, CSRF, rate-limit, security-headers,
	// logging, request-ID) would otherwise fall through to net/http's bare per-connection
	// recovery, which drops the response body and skips structured logging entirely.
	return withRecovery(handler.log, stack)
}

// buildNamedRoutes returns the set of first path segments that name built-in routes rather than
// notes or links. The auth middleware treats any request whose first segment is in this set as
// a non-note route (so it keeps requiring auth); everything else may be a public note view.
// Sourced from domain.ReservedSlugPrefixes, the single source of truth also used to reject
// colliding custom slugs (shared by notes.Manager and links.Manager — a usecase package may not
// import another usecase package, so this can't live in either directly); keep the route
// registrations in NewRouter in sync with it.
// login/setup/logout/totp-login/healthz/readyz are also special-cased by exact path in
// isPublicPath, but stay listed here too: domain.ReservedSlugPrefixes blocks the whole
// first-segment namespace from custom-slug collisions (e.g. "healthz/status"), not just those
// literal paths.
func buildNamedRoutes() map[string]struct{} {
	return domain.ReservedSlugPrefixes()
}

// requireSameOrigin rejects a request that explicitly announces a foreign origin. Machine
// clients (CLI, agents, curl) send no Origin header; a browser's cross-site POST always sends
// one (CORS semantics), so comparing it with the request's own origin closes cookie-CSRF for
// the routes it wraps — session-authenticated account endpoints and note/link PUT/DELETE;
// SameSite alone admits sibling-subdomain calls. It is not applied to every mutation: the
// JSON-only creation routes (POST /notes, /links, /links/bulk) rely on the CORS preflight a
// cross-site JSON POST triggers plus the SameSite=Strict session cookie instead (see
// registerOgenRoutes). A missing Origin is allowed for non-browser clients.
func requireSameOrigin(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, origin) {
			writeJSONErrorStatus(w, r, log, http.StatusForbidden, "cross-origin request rejected")

			return
		}

		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether origin matches the origin the request itself arrived with: hosts
// must be equal (case-insensitively — Host is the strong gate, a sibling subdomain can never
// match), and when the requestOrigin middleware has stored a scheme, schemes must match too.
func sameOrigin(r *http.Request, origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.Scheme == "" {
		return false
	}

	if !strings.EqualFold(parsed.Host, r.Host) {
		return false
	}

	if ctxOrigin := originFromCtx(r.Context()); ctxOrigin.Host != "" {
		return strings.EqualFold(parsed.Scheme, ctxOrigin.Scheme)
	}

	return true
}

// requestOrigin carries the scheme+host a request arrived with. Ogen handlers receive only a
// context, and two of the account operations embed the server's base URL into their one-time
// responses — the pmk_… token envelope and the invite link. The HTML admin panel builds the
// same pair from the request directly (web_admin.go); this middleware is its context-borne
// counterpart.
type requestOrigin struct {
	Scheme string
	Host   string
}

type originCtxKey struct{}

// withRequestOrigin stores the request's origin in the context. forcedScheme is the
// operator-forced override (Handler.WithForcedScheme); an empty value defers to
// requestScheme's TLS/X-Forwarded-Proto detection — same rules as common.scheme.
func withRequestOrigin(forcedScheme string, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := requestOrigin{Scheme: requestScheme(r, forcedScheme, trustedProxies), Host: r.Host}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), originCtxKey{}, origin)))
	})
}

// originFromCtx returns the stored request origin; zero-valued when the middleware is not
// installed (callers treat that as an error rather than minting a URL with a broken base).
func originFromCtx(ctx context.Context) requestOrigin {
	origin, _ := ctx.Value(originCtxKey{}).(requestOrigin)

	return origin
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

	n, err := sr.ResponseWriter.Write(b)
	if err != nil {
		return n, fmt.Errorf("write response: %w", err)
	}

	return n, nil
}

func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK, written: false}

		// Install the identity holder so the (inner) auth layer can record who the request
		// resolved to; it stays anonymous when no auth runs or no valid session is present.
		// Only the request passed downstream carries the holder; the original r.Context() is
		// kept for logging (inherited context, preserves the request ID).
		holder := &logUser{name: "", admin: false}
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
		// Only built-in route names are safe to log; the first segment of /{slug}
		// is itself secret key material for legacy content.
		attrs := []any{
			"method", r.Method,
			"status", rec.status,
			"duration", time.Since(start),
			"remote", r.RemoteAddr,
			"xff", r.Header.Get("X-Forwarded-For"),
			"user", user,
			"admin", holder.admin,
			"route", logRoute(r.URL.Path),
		}

		// Debug correlation also uses a digest, never the plaintext path or query string.
		if log.Enabled(r.Context(), slog.LevelDebug) {
			attrs = append(attrs, "path_hash", domain.HashSlug(r.URL.Path))
		}

		log.Log(r.Context(), level, "http", attrs...)
	})
}

// logRoute reports a built-in route name or a constant label for note slug paths.
func logRoute(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return ""
	}

	if first, reserved := domain.FirstSegmentReserved(trimmed); reserved {
		return first
	}

	return "note"
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
		"style-src 'self' 'nonce-" + nonce + "'; " +
		"font-src 'self'; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		// default-src does not cover these: block <base> hijacking of relative URLs, forms posting
		// off-origin, framing (clickjacking; supersedes X-Frame-Options) and plugin content.
		"base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"
}

// withSecurityHeaders sets the hardening headers on every response. HSTS is emitted only when
// the request counts as HTTPS (see isHTTPS: direct TLS, a trusted proxy's X-Forwarded-Proto, or
// a forced "https" scheme) — sending it over plain HTTP would be ignored by browsers anyway.
func withSecurityHeaders(forcedScheme string, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
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

		if isHTTPS(r, forcedScheme, trustedProxies) {
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
