package http

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/partyzanex/padmark/internal/domain"
)

// Deprecated: tokenCookieName is the cookie for the legacy bearer-token auth
// (PADMARK_AUTH_TOKENS), superseded by the TOTP session cookie. Will be removed in a future release.
const tokenCookieName = "padmark_token"

// sessionChecker resolves a session ID or an API-token bearer key to a User; used by the
// auth middleware.
type sessionChecker interface {
	GetSession(ctx context.Context, sessionID string) (*domain.User, error)
	// ResolveAPIToken maps a bearer API key to its owning user, recording last-used.
	// Returns domain.ErrNotFound when the key is unknown, revoked, or expired.
	ResolveAPIToken(ctx context.Context, plainToken string) (*domain.User, error)
}

// newAuthMiddleware wraps the entire handler tree with token and/or session auth.
// If tokenSet is empty and checker is nil, auth is disabled and all requests pass through.
// Browser requests without a valid credential are redirected to /login.
// API requests without a valid Bearer token receive 401.
// namedRoutes is the set of single-segment path names that are named page routes,
// not note IDs — maintained in NewRouter alongside route registrations.
func newAuthMiddleware(
	tokenSet, namedRoutes map[string]struct{}, checker sessionChecker, next http.Handler,
) http.Handler {
	if len(tokenSet) == 0 && checker == nil {
		return next
	}

	return &authMiddleware{allowed: tokenSet, namedRoutes: namedRoutes, checker: checker, next: next}
}

type authMiddleware struct {
	checker     sessionChecker
	allowed     map[string]struct{}
	namedRoutes map[string]struct{}
	next        http.Handler
}

func (am *authMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case isPublicPath(r.URL.Path):
		// Static assets, health checks, /login etc. never need the user — pass straight
		// through without a session lookup (avoids a query per static asset).
		am.next.ServeHTTP(w, r)

		return
	case isPublicRoute(r, am.namedRoutes):
		// Note view/burn routes: auth is not required, but resolve the caller when a
		// credential is present so the handler's private-note and CanEdit checks read the
		// user from context. A session cookie (browser) or an API-token Bearer key (CLI)
		// both count; a request with neither costs no query (each resolver short-circuits
		// when its credential is absent).
		rr, ok := am.resolveSessionUser(r)
		if !ok {
			rr, _ = am.resolveAPITokenUser(rr)
		}

		am.next.ServeHTTP(w, rr)

		return
	}

	// TOTP session cookie check — puts user in context on success.
	if rr, ok := am.resolveSessionUser(r); ok {
		am.next.ServeHTTP(w, rr)

		return
	}

	// Bearer token / padmark_token cookie check.
	if len(am.allowed) > 0 {
		token := extractToken(r)
		if _, ok := am.allowed[token]; ok {
			setLogUser(r.Context(), "bearer-token", false)
			am.next.ServeHTTP(w, r)

			return
		}
	}

	// API-token Bearer check — resolve the key to its owning user (DB-backed).
	// ResolveAPIToken records last-used internally; an unknown/expired key falls
	// through to the 401/redirect decision below.
	if rr, ok := am.resolveAPITokenUser(r); ok {
		am.next.ServeHTTP(w, rr)

		return
	}

	// Sec-Fetch-Dest: document is set by browsers on top-level navigation;
	// API clients don't set it. Use it as the primary signal for whether to
	// redirect vs. return 401 to avoid sending HTML redirects to API callers.
	sfDest := r.Header.Get("Sec-Fetch-Dest")
	isBrowser := sfDest == "document" ||
		(sfDest == "" && strings.Contains(r.Header.Get("Accept"), "text/html"))

	if isBrowser {
		loginURL := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, loginURL, http.StatusSeeOther)

		return
	}

	writeUnauthorized(w)
}

// writeUnauthorized answers a non-browser request with the same JSON shape as every other API
// error (errorJSON, matching the OpenAPI ErrorResponse schema) instead of the plain-text body
// http.Error would produce — the generated ogen client (used by the CLI) decodes API errors as
// JSON, so a plain-text 401 here surfaces as an opaque decode failure instead of a clean message.
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", mimeJSON)
	w.WriteHeader(http.StatusUnauthorized)

	// Encoding a fixed two-field struct only fails on a broken connection, which is
	// unactionable here — not worth threading a logger into this otherwise stateless
	// middleware just to report it.
	err := json.NewEncoder(w).Encode(errorJSON{Message: "unauthorized"})
	if err != nil {
		return
	}
}

// resolveSessionUser returns r enriched with the authenticated user in context when a
// valid session cookie is present, and whether a user was resolved. A missing cookie or a
// failed lookup returns the original request and false — so an anonymous request (no
// cookie) costs no query.
func (am *authMiddleware) resolveSessionUser(r *http.Request) (*http.Request, bool) {
	if am.checker == nil {
		return r, false
	}

	sessID := extractSessionID(r)
	if sessID == "" {
		return r, false
	}

	usr, err := am.checker.GetSession(r.Context(), sessID)
	if err != nil {
		return r, false
	}

	setLogUser(r.Context(), usr.Username, usr.IsAdmin)

	return r.WithContext(context.WithValue(r.Context(), keyUser, usr)), true
}

// resolveAPITokenUser returns r enriched with the authenticated user in context when the
// request carries a valid API-token Bearer key, and whether one was resolved. A missing
// Authorization: Bearer header or an unknown/expired key returns the original request and
// false — so a request without a Bearer header costs no database query.
func (am *authMiddleware) resolveAPITokenUser(r *http.Request) (*http.Request, bool) {
	if am.checker == nil {
		return r, false
	}

	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || token == "" {
		return r, false
	}

	usr, err := am.checker.ResolveAPIToken(r.Context(), token)
	if err != nil {
		return r, false
	}

	setLogUser(r.Context(), usr.Username, usr.IsAdmin)

	return r.WithContext(context.WithValue(r.Context(), keyUser, usr)), true
}

func extractToken(r *http.Request) string {
	hdr := r.Header.Get("Authorization")

	if token, found := strings.CutPrefix(hdr, "Bearer "); found {
		return token
	}

	cookie, err := r.Cookie(tokenCookieName)
	if err == nil {
		return cookie.Value
	}

	return ""
}

// apiDocsPath and apiSpecPath are always public (see isPublicPath) and are also part of the
// REST/JSON API surface that --disable-api turns off (see isDisabledAPIPath).
const (
	apiDocsPath = "/api"
	apiSpecPath = "/api/openapi.yaml"
)

func isPublicPath(path string) bool {
	return path == "/login" ||
		path == "/setup" ||
		path == "/logout" ||
		path == "/totp-login" ||
		strings.HasPrefix(path, "/static/") ||
		path == apiDocsPath || path == apiSpecPath ||
		path == "/healthz" || path == "/readyz"
}

// isPublicRoute allows note-view and burn-reveal requests through the auth middleware.
// namedRoutes is the set of single-segment path names registered as named page routes
// in NewRouter; they must not be treated as public note IDs.
// The handler checks the per-note private flag and requires auth when the note is private.
func isPublicRoute(r *http.Request, namedRoutes map[string]struct{}) bool {
	// GET note view and POST burn-reveal are the only public note routes; every other method
	// (create/update/delete) requires auth.
	switch r.Method {
	case http.MethodGet, http.MethodPost:
		return isNoteIDPath(strings.TrimPrefix(r.URL.Path, "/"), namedRoutes)
	default:
		return false
	}
}

// isNoteIDPath reports whether trimmed (the request path without its leading slash) addresses a
// note for public viewing/revealing — the root catch-all /{id...} or the API-style /notes/{id...},
// where the slug may span multiple segments (e.g. project/GUIDE.md). A path whose first segment
// names a built-in route (namedRoutes) is never a public note: those keep requiring auth, so
// /edit/{id}, /admin/... and friends are not opened up by the multi-segment slug support.
// Any other single-segment path is a candidate public note by design — slugs and route names
// share one flat namespace, kept apart only by reservedSlugPrefixes (internal/usecases/notes) —
// and whether the matched note is actually served publicly is decided downstream, per-note, by
// handlePrivateAuth.
func isNoteIDPath(trimmed string, namedRoutes map[string]struct{}) bool {
	if trimmed == "" {
		return false
	}

	// /notes/{id...} — strip the API prefix, then apply the same first-segment guard to the rest.
	if after, ok := strings.CutPrefix(trimmed, "notes/"); ok {
		trimmed = after
		if trimmed == "" {
			return false
		}
	}

	first, _, _ := strings.Cut(trimmed, "/")

	_, isNamed := namedRoutes[first]

	return !isNamed
}

// loginHandler validates the bearer token from a form POST and sets a session cookie.
//
// Deprecated: this is the legacy bearer-token login (POST /login backed by PADMARK_AUTH_TOKENS),
// superseded by the TOTP account system (POST /totp-login, --enable-accounts). Will be removed
// in a future release.
func loginHandler(tokens map[string]struct{}, cookieMaxAge int, trustedProxies []*net.IPNet) http.HandlerFunc {
	const maxLoginBody = 1024

	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)

		token := r.FormValue("token")
		next := safeNextURL(r.FormValue("next"))

		// Token lookup is a map access, not a constant-time compare. This is acceptable: bearer
		// tokens are high-entropy secrets, so a timing side-channel leaks no practically useful
		// information (an attacker cannot iterate the keyspace). Login is also rate-limited upstream.
		if _, ok := tokens[token]; !ok || token == "" {
			dest := "/login?error=1"
			if next != "" {
				dest += "&next=" + url.QueryEscape(next)
			}

			http.Redirect(w, r, dest, http.StatusSeeOther)

			return
		}

		http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows TLS detection
			Name:     tokenCookieName,
			Value:    token,
			Path:     "/",
			MaxAge:   cookieMaxAge,
			HttpOnly: true,
			Secure:   isHTTPS(r, trustedProxies),
			SameSite: http.SameSiteStrictMode,
		})

		dest := "/"
		if next != "" {
			dest = next
		}

		http.Redirect(w, r, dest, http.StatusSeeOther)
	}
}

// safeNextURL validates that the redirect target is a local path to prevent open redirects.
// Returns empty string if the value is absent or unsafe.
func safeNextURL(next string) string {
	if next == "" {
		return ""
	}

	// Reject backslash: browsers treat /\evil.com as //evil.com (open redirect).
	if strings.ContainsRune(next, '\\') {
		return ""
	}

	// Reject protocol-relative //evil.com (belt-and-suspenders; url.Parse also catches host).
	if strings.HasPrefix(next, "//") {
		return ""
	}

	parsed, err := url.Parse(next)
	if err != nil || parsed.Host != "" || parsed.Scheme != "" || !strings.HasPrefix(next, "/") {
		return ""
	}

	return next
}
