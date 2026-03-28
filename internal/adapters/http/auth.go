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
	if isPublicPath(r.URL.Path) {
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
		strings.HasPrefix(path, "/api") ||
		path == "/healthz" || path == "/readyz"
}

// loginHandler validates the token from a form POST and sets a session cookie.
func loginHandler(tokens map[string]struct{}) http.HandlerFunc {
	const maxLoginBody = 1024

	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)

		token := r.FormValue("token")

		if _, ok := tokens[token]; !ok || token == "" {
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		const tenYears = 10 * 365 * 24 * 60 * 60

		http.SetCookie(w, &http.Cookie{
			Name:     tokenCookieName,
			Value:    token,
			Path:     "/",
			MaxAge:   tenYears,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}
