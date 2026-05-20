package http

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/partyzanex/padmark/internal/domain"
)

const tokenCookieName = "padmark_token"

// sessionChecker resolves a session ID to a User; used by the auth middleware.
type sessionChecker interface {
	GetSession(ctx context.Context, sessionID string) (*domain.User, error)
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
	if isPublicPath(r.URL.Path) || isPublicRoute(r, am.namedRoutes) {
		am.next.ServeHTTP(w, r)

		return
	}

	// TOTP session cookie check — puts user in context on success.
	if am.checker != nil {
		if sessID := extractSessionID(r); sessID != "" {
			usr, err := am.checker.GetSession(r.Context(), sessID)
			if err == nil {
				ctx := context.WithValue(r.Context(), keyUser, usr)
				am.next.ServeHTTP(w, r.WithContext(ctx))

				return
			}
		}
	}

	// Bearer token / padmark_token cookie check.
	if len(am.allowed) > 0 {
		token := extractToken(r)
		if _, ok := am.allowed[token]; ok {
			am.next.ServeHTTP(w, r)

			return
		}
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

	http.Error(w, "unauthorized", http.StatusUnauthorized)
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

func isPublicPath(path string) bool {
	return path == "/login" ||
		path == "/setup" ||
		path == "/logout" ||
		strings.HasPrefix(path, "/static/") ||
		path == "/api" || path == "/api/openapi.yaml" ||
		path == "/healthz" || path == "/readyz"
}

// isPublicRoute allows note-view and burn-reveal requests through the auth middleware.
// namedRoutes is the set of single-segment path names registered as named page routes
// in NewRouter; they must not be treated as public note IDs.
// The handler checks the per-note private flag and requires auth when the note is private.
func isPublicRoute(r *http.Request, namedRoutes map[string]struct{}) bool {
	path := r.URL.Path
	trimmed := strings.TrimPrefix(path, "/")

	// POST /{id} and POST /notes/{id} — burn-after-reading confirmation
	if r.Method == http.MethodPost {
		return isNoteIDPath(trimmed, namedRoutes)
	}

	if r.Method != http.MethodGet {
		return false
	}

	// GET /notes/{id} — single segment after "notes/"
	if after, ok := strings.CutPrefix(trimmed, "notes/"); ok {
		return after != "" && !strings.Contains(after, "/")
	}

	// Catch-all GET /{id} — single path segment that is not a named page route
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return false
	}

	_, isNamed := namedRoutes[trimmed]

	return !isNamed
}

// isNoteIDPath reports whether trimmed (path without leading slash) matches
// /{id} or /notes/{id} for POST requests.
func isNoteIDPath(trimmed string, namedRoutes map[string]struct{}) bool {
	// /notes/{id}
	if after, ok := strings.CutPrefix(trimmed, "notes/"); ok {
		return after != "" && !strings.Contains(after, "/")
	}

	// /{id}
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return false
	}

	_, isNamed := namedRoutes[trimmed]

	return !isNamed
}

// loginHandler validates the token from a form POST and sets a session cookie.
func loginHandler(tokens map[string]struct{}, cookieMaxAge int, trustedProxies []*net.IPNet) http.HandlerFunc {
	const maxLoginBody = 1024

	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)

		token := r.FormValue("token")
		next := safeNextURL(r.FormValue("next"))

		if _, ok := tokens[token]; !ok || token == "" {
			dest := "/login?error=1"
			if next != "" {
				dest += "&next=" + url.QueryEscape(next)
			}

			http.Redirect(w, r, dest, http.StatusSeeOther)

			return
		}

		http.SetCookie(w, &http.Cookie{
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
