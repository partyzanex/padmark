package http

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
)

const (
	csrfCookieName = "padmark_csrf"
	csrfFieldName  = "csrf_token"
	csrfNonceBytes = 32
)

// generateCSRFToken creates a signed CSRF token: base64url(nonce) + "." + base64url(HMAC(nonce, secret)).
// Both the cookie and the hidden form field carry the full signed value so forged cookies fail HMAC check.
func generateCSRFToken(secret []byte) (string, error) {
	nonce := make([]byte, csrfNonceBytes)

	_, err := rand.Read(nonce)
	if err != nil {
		return "", err //nolint:wrapcheck // rand.Read error is self-explanatory
	}

	nonceB64 := base64.RawURLEncoding.EncodeToString(nonce)
	mac := csrfHMAC(nonceB64, secret)

	return nonceB64 + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

func csrfHMAC(nonceB64 string, secret []byte) []byte {
	hm := hmac.New(sha256.New, secret)
	hm.Write([]byte(nonceB64))

	return hm.Sum(nil)
}

// verifyCSRFToken checks the HMAC signature of a full signed token string.
func verifyCSRFToken(token string, secret []byte) bool {
	nonceB64, macB64, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}

	gotMAC, err := base64.RawURLEncoding.DecodeString(macB64)
	if err != nil {
		return false
	}

	expected := csrfHMAC(nonceB64, secret)

	return hmac.Equal(gotMAC, expected)
}

func csrfFromContext(ctx context.Context) string {
	v, _ := ctx.Value(keyCSRF).(string)

	return v
}

func setCSRFCookie(w http.ResponseWriter, r *http.Request, token string, trustedProxies []*net.IPNet) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r, trustedProxies),
		SameSite: http.SameSiteStrictMode,
	})
}

// rotateCSRFToken generates a fresh signed token, sets it as the cookie, and returns the token
// for embedding in any form rendered in the same response.
func rotateCSRFToken(w http.ResponseWriter, r *http.Request, secret []byte, trustedProxies []*net.IPNet) string {
	token, err := generateCSRFToken(secret)
	if err != nil {
		return ""
	}

	setCSRFCookie(w, r, token, trustedProxies)

	return token
}

// clearCSRFCookie removes the CSRF cookie on logout.
func clearCSRFCookie(w http.ResponseWriter, r *http.Request, trustedProxies []*net.IPNet) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r, trustedProxies),
		SameSite: http.SameSiteStrictMode,
	})
}

// withCSRFToken reads the CSRF cookie if it is present and carries a valid HMAC signature;
// otherwise generates a fresh signed token and sets the cookie.
// The token is injected into the request context so handlers can embed it in forms.
func withCSRFToken(secret []byte, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := func() string {
			cookie, err := r.Cookie(csrfCookieName)
			if err == nil && cookie.Value != "" && verifyCSRFToken(cookie.Value, secret) {
				return cookie.Value
			}

			return ""
		}()

		if token == "" {
			token = rotateCSRFToken(w, r, secret, trustedProxies)
			if token == "" {
				http.Error(w, "internal server error", http.StatusInternalServerError)

				return
			}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyCSRF, token)))
	})
}

// csrfGuard wraps a POST handler. For POST requests it validates that:
//  1. The padmark_csrf cookie and the csrf_token form field carry the same signed value.
//  2. The signature is valid (HMAC with server secret).
//
// Returns 403 on any failure.
func csrfGuard(secret []byte, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			cookie, cookieErr := r.Cookie(csrfCookieName)
			if cookieErr != nil || cookie.Value == "" {
				http.Error(w, "forbidden", http.StatusForbidden)

				return
			}

			// Body size is already capped by withBodyLimit middleware upstream.
			field := r.FormValue(csrfFieldName) //nolint:gosec // G120: body limited by withBodyLimit

			if !hmac.Equal([]byte(cookie.Value), []byte(field)) {
				http.Error(w, "forbidden", http.StatusForbidden)

				return
			}

			if !verifyCSRFToken(cookie.Value, secret) {
				http.Error(w, "forbidden", http.StatusForbidden)

				return
			}
		}

		next(w, r)
	}
}
