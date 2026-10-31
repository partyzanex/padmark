package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	csrfCookieName = "padmark_csrf"
	csrfFieldName  = "csrf_token"
	csrfNonceBytes = 32
	// maxCSRFFormBytes caps the body parsed by CSRFGuard. The guarded endpoints are small auth
	// forms, so 1 MiB is generous; the explicit cap satisfies gosec G120 (the global body limit
	// already applies upstream, but gosec wants a bound at the parse site).
	maxCSRFFormBytes = 1 << 20
)

// csrfAnonymousBinding is the binding of tokens issued while no session cookie is present.
const csrfAnonymousBinding = "anonymous"

// csrfSessionBinding derives the state a CSRF token is bound to: the SHA-256 of the session
// cookie value (never the raw value, which is a credential), or csrfAnonymousBinding when there
// is no session. A token minted for one state does not verify in another, so a cookie/field pair
// planted by an attacker (cookie tossing from a sibling subdomain, a pre-login token) is useless
// against a signed-in victim, who holds a session cookie the attacker cannot read.
func csrfSessionBinding(sessionID string) string {
	if sessionID == "" {
		return csrfAnonymousBinding
	}

	sum := sha256.Sum256([]byte(sessionID))

	return "session:" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateCSRFToken creates a signed CSRF token:
// base64url(nonce) + "." + base64url(HMAC(secret, domain || binding || nonce)), where the binding
// is derived from sessionID (empty = anonymous). Both the cookie and the hidden form field carry
// the full signed value so forged cookies fail the HMAC check.
func GenerateCSRFToken(secret []byte, sessionID string) (string, error) {
	nonce := make([]byte, csrfNonceBytes)

	_, err := rand.Read(nonce)
	if err != nil {
		return "", fmt.Errorf("read csrf nonce: %w", err)
	}

	nonceB64 := base64.RawURLEncoding.EncodeToString(nonce)
	mac := csrfHMAC(nonceB64, csrfSessionBinding(sessionID), secret)

	return nonceB64 + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// csrfHMAC signs nonceB64 under secret, scoped to binding. The NUL separators keep the three
// fields unambiguous.
func csrfHMAC(nonceB64, binding string, secret []byte) []byte {
	hm := hmac.New(sha256.New, secret)
	hm.Write([]byte("padmark-csrf-v2\x00" + binding + "\x00" + nonceB64))

	return hm.Sum(nil)
}

// VerifyCSRFToken checks the HMAC signature for the state implied by sessionID (empty =
// anonymous) and rejects an unconfigured secret.
func VerifyCSRFToken(token string, secret []byte, sessionID string) bool {
	if len(secret) == 0 {
		return false
	}

	nonceB64, macB64, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}

	gotMAC, err := base64.RawURLEncoding.DecodeString(macB64)
	if err != nil {
		return false
	}

	expected := csrfHMAC(nonceB64, csrfSessionBinding(sessionID), secret)

	return hmac.Equal(gotMAC, expected)
}

// CSRFFromContext returns the CSRF token issued for this request, or "" when there is none.
func CSRFFromContext(ctx context.Context) string {
	v, _ := ctx.Value(keyCSRF).(string)

	return v
}

// setCSRFCookie writes the CSRF cookie; forcedScheme and trustedProxies feed IsHTTPS for the
// Secure attribute.
func setCSRFCookie(
	w http.ResponseWriter, r *http.Request, token, forcedScheme string, trustedProxies []*net.IPNet,
) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows IsHTTPS
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   IsHTTPS(r, forcedScheme, trustedProxies),
		SameSite: http.SameSiteStrictMode,
		// Unused optional fields zeroed: no Raw, RawExpires, Unparsed, Quoted, Domain, Expires,
		// Partitioned. MaxAge=0 means session cookie (cleared on browser close).
		Raw:         "",
		RawExpires:  "",
		Unparsed:    nil,
		Quoted:      false,
		Domain:      "",
		Expires:     time.Time{},
		MaxAge:      0,
		Partitioned: false,
	})
}

// RotateCSRFToken generates a fresh signed token bound to sessionID (empty = anonymous), sets it
// as the cookie, and returns the token for embedding in any form rendered in the same response.
// Callers that have just issued or replaced the session cookie pass the NEW session id: the
// request still carries the old (or no) cookie.
func RotateCSRFToken(
	w http.ResponseWriter, r *http.Request, secret []byte, sessionID, forcedScheme string, trustedProxies []*net.IPNet,
) string {
	token, err := GenerateCSRFToken(secret, sessionID)
	if err != nil {
		return ""
	}

	setCSRFCookie(w, r, token, forcedScheme, trustedProxies)

	return token
}

// ClearCSRFCookie removes the CSRF cookie on logout.
func ClearCSRFCookie(w http.ResponseWriter, r *http.Request, forcedScheme string, trustedProxies []*net.IPNet) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows IsHTTPS
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   IsHTTPS(r, forcedScheme, trustedProxies),
		SameSite: http.SameSiteStrictMode,
		// Unused optional fields zeroed: no Raw, RawExpires, Unparsed, Quoted, Domain, Expires, Partitioned.
		Raw:         "",
		RawExpires:  "",
		Unparsed:    nil,
		Quoted:      false,
		Domain:      "",
		Expires:     time.Time{},
		Partitioned: false,
	})
}

// WithCSRFToken reads the CSRF cookie if it is present and carries a valid HMAC signature for
// the request's current session state; otherwise generates a fresh signed token bound to that
// session and sets the cookie, so the token follows login, logout and session replacement.
// The token is injected into the request context so handlers can embed it in forms.
func WithCSRFToken(secret []byte, forcedScheme string, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := ExtractSessionID(r)

		token := func() string {
			value, ok := SingleCookieValue(r, csrfCookieName)
			if ok && value != "" && VerifyCSRFToken(value, secret, sessionID) {
				return value
			}

			return ""
		}()

		if token == "" {
			token = RotateCSRFToken(w, r, secret, sessionID, forcedScheme, trustedProxies)
			if token == "" {
				http.Error(w, "internal server error", http.StatusInternalServerError)

				return
			}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyCSRF, token)))
	})
}

// crossSiteRequest reports whether the browser says a request was initiated from another origin
// or site: an Origin header that is not this server's origin, or Sec-Fetch-Site other than
// same-origin/none. Machine clients send neither header and pass, as in RequireSameOrigin and the
// JSON /setup path.
func crossSiteRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" && !SameOrigin(r, origin) {
		return true
	}

	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return false
	default:
		return true
	}
}

// CSRFGuard wraps a POST handler. For POST requests it validates that:
//  1. The request is not announced as cross-origin (Origin / Sec-Fetch-Site), defence in depth
//     against a cookie planted from a sibling subdomain.
//  2. The padmark_csrf cookie and the csrf_token form field carry the same signed value.
//  3. The signature is valid for the current session state (HMAC with the server secret, bound
//     to the padmark_session cookie, or to "anonymous" when there is none).
//
// Returns 403 on any failure.
func CSRFGuard(secret []byte, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !csrfPostAllowed(w, r, secret) {
			http.Error(w, "forbidden", http.StatusForbidden)

			return
		}

		next(w, r)
	}
}

// csrfPostAllowed applies the CSRFGuard checks to a POST request. It caps r.Body before it parses
// the form, so the handler that runs next reads the same capped body.
func csrfPostAllowed(w http.ResponseWriter, r *http.Request, secret []byte) bool {
	if crossSiteRequest(r) {
		return false
	}

	// Several same-name cookies are ambiguous (cookie tossing from a sibling subdomain):
	// Request.Cookie would return the first, which the attacker can order, so refuse.
	cookieValue, cookieOK := SingleCookieValue(r, csrfCookieName)
	if !cookieOK || cookieValue == "" {
		return false
	}

	// Cap the body before parsing the form (gosec G120); the global limit also applies
	// upstream, and guarded forms are tiny.
	r.Body = http.MaxBytesReader(w, r.Body, maxCSRFFormBytes)
	field := r.FormValue(csrfFieldName)

	if !hmac.Equal([]byte(cookieValue), []byte(field)) {
		return false
	}

	return VerifyCSRFToken(cookieValue, secret, ExtractSessionID(r))
}
