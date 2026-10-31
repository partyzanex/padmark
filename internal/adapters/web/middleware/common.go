package middleware

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

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/adapters/web/response"
	"github.com/partyzanex/padmark/internal/domain"
)

// RequireSameOrigin rejects a request that explicitly announces a foreign origin. Machine
// clients (CLI, agents, curl) send no Origin header; a browser's cross-site POST always sends
// one (CORS semantics), so comparing it with the request's own origin closes cookie-CSRF for
// the routes it wraps — session-authenticated account endpoints and note/link PUT/DELETE;
// SameSite alone admits sibling-subdomain calls. It is not applied to every mutation: the
// JSON-only creation routes (POST /notes, /links, /links/bulk) rely on the CORS preflight a
// cross-site JSON POST triggers plus the SameSite=Strict session cookie instead (see
// registerOgenRoutes). A missing Origin is allowed for non-browser clients.
func RequireSameOrigin(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !SameOrigin(r, origin) {
			response.WriteJSONErrorStatus(w, r, log, http.StatusForbidden, "cross-origin request rejected")

			return
		}

		next.ServeHTTP(w, r)
	})
}

// SameOrigin reports whether origin matches the origin the request itself arrived with: hosts
// must be equal (case-insensitively — Host is the strong gate, a sibling subdomain can never
// match), and when the RequestOrigin middleware has stored a scheme, schemes must match too.
func SameOrigin(r *http.Request, origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.Scheme == "" {
		return false
	}

	if !strings.EqualFold(parsed.Host, r.Host) {
		return false
	}

	if ctxOrigin := OriginFromCtx(r.Context()); ctxOrigin.Host != "" {
		return strings.EqualFold(parsed.Scheme, ctxOrigin.Scheme)
	}

	return true
}

// RequestOrigin carries the scheme+host a request arrived with. Ogen handlers receive only a
// context, and two of the account operations embed the server's base URL into their one-time
// responses — the pmk_… token envelope and the invite link. The HTML admin panel builds the
// same pair from the request directly (web_admin.go); this middleware is its context-borne
// counterpart.
type RequestOrigin struct {
	Scheme string
	Host   string
}

type originCtxKey struct{}

// WithRequestOrigin stores the request's origin in the context. forcedScheme is the
// operator-forced override (Handler.WithForcedScheme); an empty value defers to
// RequestScheme's TLS/X-Forwarded-Proto detection — same rules as common.scheme.
func WithRequestOrigin(forcedScheme string, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := RequestOrigin{Scheme: RequestScheme(r, forcedScheme, trustedProxies), Host: r.Host}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), originCtxKey{}, origin)))
	})
}

// OriginFromCtx returns the stored request origin; zero-valued when the middleware is not
// installed (callers treat that as an error rather than minting a URL with a broken base).
func OriginFromCtx(ctx context.Context) RequestOrigin {
	origin, ok := ctx.Value(originCtxKey{}).(RequestOrigin)
	if !ok {
		return RequestOrigin{Scheme: "", Host: ""}
	}

	return origin
}

// WithRequestID gives each request a new UUID. It sends the ID in the X-Request-ID response
// header and stores it in the request context for the logs.
func WithRequestID(next http.Handler) http.Handler {
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

// WithLogging writes one log record for each request with its status, duration and resolved user.
// Requests for /static/ use the debug level.
func WithLogging(log *slog.Logger, next http.Handler) http.Handler {
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

// WithBodyLimit limits a request body to maxBytes; a larger body fails when the handler reads it.
func WithBodyLimit(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.ContentLength != 0 {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}

		next.ServeHTTP(w, r)
	})
}

// WithStaticCacheControl lets browsers and proxies cache a static asset for one day.
func WithStaticCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		next.ServeHTTP(w, r)
	})
}

// buildCSP assembles the Content-Security-Policy applied to every response, the /api docs page
// included: scripts come only from the app origin ('self', e.g. the vendored Redoc bundle under
// /static/) or carry the per-request nonce — no third-party origin is ever allowed.
func buildCSP(nonce string) string {
	return "default-src 'self'; " +
		"script-src 'self' 'nonce-" + nonce + "'; " +
		"style-src 'self' 'nonce-" + nonce + "'; " +
		"font-src 'self'; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		// default-src does not cover these: block <base> hijacking of relative URLs, forms posting
		// off-origin, framing (clickjacking; supersedes X-Frame-Options) and plugin content.
		"base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"
}

// WithSecurityHeaders sets the hardening headers on every response. HSTS is emitted only when
// the request counts as HTTPS (see IsHTTPS: direct TLS, a trusted proxy's X-Forwarded-Proto, or
// a forced "https" scheme) — sending it over plain HTTP would be ignored by browsers anyway.
func WithSecurityHeaders(forcedScheme string, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
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

		if IsHTTPS(r, forcedScheme, trustedProxies) {
			header.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

// WithRecovery converts a handler panic into a 500 response and an error log record, so one
// request cannot stop the server.
func WithRecovery(log *slog.Logger, next http.Handler) http.Handler {
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
