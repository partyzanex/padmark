package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/partyzanex/padmark/internal/adapters/http/response"
	"github.com/partyzanex/padmark/internal/domain"
)

// SessionChecker resolves a session ID or an API-token bearer key to a User; used by the
// auth middleware.
type SessionChecker interface {
	GetSession(ctx context.Context, sessionID string) (*domain.User, error)
	// ResolveAPIToken maps a bearer API key to its owning user and the key's public ID,
	// recording last-used. Returns domain.ErrNotFound when the key is unknown, revoked, or expired.
	ResolveAPIToken(ctx context.Context, plainToken string) (*domain.User, string, error)
}

// NewAuthMiddleware wraps the entire handler tree with session auth.
// If checker is nil, auth is disabled and all requests pass through.
// Browser requests without a valid credential are redirected to /login.
// API requests without a valid Bearer token receive 401.
// namedRoutes is the set of single-segment path names that are named page routes,
// not note IDs — maintained in NewRouter alongside route registrations.
func NewAuthMiddleware(namedRoutes map[string]struct{}, checker SessionChecker, next http.Handler) http.Handler {
	if checker == nil {
		return next
	}

	return &authMiddleware{namedRoutes: namedRoutes, checker: checker, next: next}
}

type authMiddleware struct {
	checker     SessionChecker
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

	WriteUnauthorized(w)
}

// WriteUnauthorized answers a non-browser request with the same JSON shape as every other API
// error (ErrorJSON, matching the OpenAPI ErrorResponse schema) instead of the plain-text body
// http.Error would produce — the generated ogen client (used by the CLI) decodes API errors as
// JSON, so a plain-text 401 here surfaces as an opaque decode failure instead of a clean message.
func WriteUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)

	// Encoding a fixed two-field struct only fails on a broken connection, which is
	// unactionable here — not worth threading a logger into this otherwise stateless
	// middleware just to report it.
	err := json.NewEncoder(w).Encode(response.ErrorJSON{Message: MsgUnauthorized, Code: ""})
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

	sessID := ExtractSessionID(r)
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

	usr, tokenHash, err := am.checker.ResolveAPIToken(r.Context(), token)
	if err != nil {
		return r, false
	}

	setLogUser(r.Context(), usr.Username, usr.IsAdmin)

	// The key's ID travels with the user: issuing operations demand a step-up from a bearer
	// and record it as the issuer, and the admin panel refuses bearers outright.
	ctx := context.WithValue(r.Context(), keyUser, usr)
	ctx = context.WithValue(ctx, keyAPIToken, tokenHash)

	return r.WithContext(ctx), true
}

// apiDocsPath, apiSpecPath and llmsTxtPath are always public (see isPublicPath) and are also
// part of the REST/JSON API surface that --disable-api turns off (see isDisabledAPIPath).
const (
	apiDocsPath = "/api"
	apiSpecPath = "/api/openapi.yaml"

	// llmsTxtPath is always public (see isPublicPath) and is also part of the
	// REST/JSON API surface that --disable-api turns off (see isDisabledAPIPath).
	llmsTxtPath = "/llms.txt"

	// FaviconPath is always public: browsers request it on their own for pages without an icon.
	FaviconPath = "/favicon.ico"

	// linkCreatePath and notesCreatePath are the collection roots (POST create); isPublicRoute
	// admits them alongside the resource paths below. editPathPrefix covers GET /edit/{id},
	// whose per-note privacy check lives in EditPage.
	linkCreatePath  = "links"
	editPathPrefix  = "edit/"
	notesCreatePath = "notes"

	// MsgUnauthorized is the shared client-facing message for every 401 the adapter writes.
	MsgUnauthorized = "unauthorized"
)

func isPublicPath(path string) bool {
	return path == "/login" ||
		path == "/setup" ||
		path == "/logout" ||
		path == "/totp-login" ||
		strings.HasPrefix(path, "/static/") ||
		path == apiDocsPath || path == apiSpecPath || path == llmsTxtPath || path == FaviconPath ||
		path == "/healthz" || path == "/readyz"
}

// isPublicRoute lets note and link resource requests through the auth middleware — reads
// (GET view, POST burn-reveal, GET /edit) and edit-code mutations (POST create, PUT/DELETE)
// alike, regardless of method. Whether the matched resource may actually be served to this
// caller is decided downstream, per resource: read privacy by handlePrivateAuth (with the
// mutation counterparts in the ogen/native write handlers), and mutation ownership by the
// edit-code/OwnedBy check inside the usecases. The auth middleware only enforces identity for
// the named page routes (namedRoutes — /admin, /login, change-password, ...), which keep
// requiring a signed-in session. Restoring the documented edit_code workflow
// ("no login required"): blocking these paths at the middleware would 401 an anonymous caller
// before the usecase ever gets to verify the code.
func isPublicRoute(r *http.Request, namedRoutes map[string]struct{}) bool {
	path := strings.TrimPrefix(r.URL.Path, "/")

	switch {
	case isLinkPath(path) || path == linkCreatePath:
		// GET /r/{id}, GET/PUT/DELETE /links/{id}, POST /links, POST /links/bulk — per-link
		// privacy and per-link ownership are enforced downstream. Unlike note paths these
		// have a fixed, unambiguous prefix, so no namedRoutes disambiguation is needed.
		return true
	case r.Method == http.MethodGet && strings.HasPrefix(path, editPathPrefix):
		// GET /edit/{id} (the only method the route serves): the editor re-runs the same
		// per-note privacy check as GET view (EditPage → handlePrivateAuth), so anonymous
		// access to a public note's edit form is safe — saving still requires the edit code
		// on the PUT.
		return true
	case path == notesCreatePath || isNoteIDPath(path, namedRoutes):
		// Note resource routes, any method: GET view, POST reveal, PUT/DELETE /notes/{id...}
		// and multi-segment /{id...}. POST /notes (create) is anonymous on purpose — a new
		// note has no privacy to check yet, and privacy=owner for an anonymous caller is
		// rejected by domain validation (ValidateOwnership).
		return true
	default:
		return false
	}
}

// isLinkPath reports whether trimmed (the request path without its leading slash) addresses a
// link for public GET access — GET /r/{id} or GET /links/{id}. Whether the matched link is
// actually served to this caller is decided downstream, per-link, by VisibleTo.
func isLinkPath(trimmed string) bool {
	if after, ok := strings.CutPrefix(trimmed, "r/"); ok {
		return after != ""
	}

	after, ok := strings.CutPrefix(trimmed, "links/")

	return ok && after != ""
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

// SafeNextURL validates that the redirect target is a local path to prevent open redirects.
// Returns empty string if the value is absent or unsafe.
func SafeNextURL(next string) string {
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
