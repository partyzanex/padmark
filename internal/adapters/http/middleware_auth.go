package http

import (
	"net/http"
	"strings"
)

const tokenCookieName = "padmark_token"

// newAuthMiddleware wraps the entire handler tree with token-based auth.
// If tokens is empty, auth is disabled and all requests pass through.
// Browser requests without a valid cookie are redirected to /login.
// API requests without a valid Bearer token receive 401.
func newAuthMiddleware(tokens []string, next http.Handler) http.Handler {
	if len(tokens) == 0 {
		return next
	}

	allowed := make(map[string]struct{}, len(tokens))

	for _, tok := range tokens {
		allowed[tok] = struct{}{}
	}

	return &authMiddleware{allowed: allowed, next: next}
}

type authMiddleware struct {
	allowed map[string]struct{}
	next    http.Handler
}

func (am *authMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isPublicPath(r.URL.Path) || isPublicRoute(r) {
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
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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
// The handler checks the per-note private flag and requires auth when the note is private.
func isPublicRoute(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}

	path := r.URL.Path
	trimmed := strings.TrimPrefix(path, "/")

	// GET /notes/{id} — single segment after "notes/"
	if after, ok := strings.CutPrefix(trimmed, "notes/"); ok {
		return after != "" && !strings.Contains(after, "/")
	}

	// Catch-all GET /{id} — single path segment that isn't a known route
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return false
	}

	switch trimmed {
	case "login", "api", "success", "edit", "healthz", "readyz":
		return false
	}

	return true
}

// loginHandler validates the token from a form POST and sets a session cookie.
func loginHandler(tokens map[string]struct{}, cookieMaxAge int) http.HandlerFunc {
	const maxLoginBody = 1024

	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)

		token := r.FormValue("token")

		if _, ok := tokens[token]; !ok || token == "" {
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
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

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}
