package http

import (
	"net/http"
	"net/url"
	"strings"
)

const tokenCookieName = "padmark_token"

// newAuthMiddleware wraps the entire handler tree with token-based auth.
// If tokenSet is empty, auth is disabled and all requests pass through.
// Browser requests without a valid cookie are redirected to /login.
// API requests without a valid Bearer token receive 401.
// namedRoutes is the set of single-segment path names that are named page routes,
// not note IDs — maintained in NewRouter alongside route registrations.
func newAuthMiddleware(tokenSet, namedRoutes map[string]struct{}, next http.Handler) http.Handler {
	if len(tokenSet) == 0 {
		return next
	}

	return &authMiddleware{allowed: tokenSet, namedRoutes: namedRoutes, next: next}
}

type authMiddleware struct {
	allowed     map[string]struct{}
	namedRoutes map[string]struct{}
	next        http.Handler
}

func (am *authMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isPublicPath(r.URL.Path) || isPublicRoute(r, am.namedRoutes) {
		am.next.ServeHTTP(w, r)

		return
	}

	token := extractToken(r)
	if _, ok := am.allowed[token]; ok {
		am.next.ServeHTTP(w, r)

		return
	}

	accept := r.Header.Get("Accept")
	if accept == "" || strings.Contains(accept, "text/html") {
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
		strings.HasPrefix(path, "/static/") ||
		path == "/api" || path == "/api/openapi.yaml" ||
		path == "/healthz" || path == "/readyz"
}

// isPublicRoute allows GET requests for note view paths through the auth middleware.
// namedRoutes is the set of single-segment path names registered as named page routes
// in NewRouter; they must not be treated as public note IDs.
// The handler checks the per-note private flag and requires auth when the note is private.
func isPublicRoute(r *http.Request, namedRoutes map[string]struct{}) bool {
	if r.Method != http.MethodGet {
		return false
	}

	path := r.URL.Path
	trimmed := strings.TrimPrefix(path, "/")

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

// loginHandler validates the token from a form POST and sets a session cookie.
func loginHandler(tokens map[string]struct{}, cookieMaxAge int) http.HandlerFunc {
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
			Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == protoHTTPS,
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

	parsed, err := url.Parse(next)
	if err != nil || parsed.Host != "" || parsed.Scheme != "" || !strings.HasPrefix(next, "/") {
		return ""
	}

	return next
}
