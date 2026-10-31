package web

import (
	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
)

// GenerateCSRFTokenForTest creates a valid HMAC-signed CSRF token for use in external test packages.
// Panics on error (test helper).
// It is bound to the anonymous state; use GenerateSessionCSRFTokenForTest for a signed-in session.
func GenerateCSRFTokenForTest(secret []byte) string {
	return GenerateSessionCSRFTokenForTest(secret, "")
}

// GenerateSessionCSRFTokenForTest creates a valid CSRF token bound to the session cookie value
// sessionID (empty = anonymous). Panics on error (test helper).
func GenerateSessionCSRFTokenForTest(secret []byte, sessionID string) string {
	tok, err := middleware.GenerateCSRFToken(secret, sessionID)
	if err != nil {
		panic("GenerateCSRFTokenForTest: " + err.Error())
	}

	return tok
}

// VerifyCSRFTokenForTest reports whether token carries a valid HMAC signature under secret for
// the state implied by sessionID (empty = anonymous).
func VerifyCSRFTokenForTest(token string, secret []byte, sessionID string) bool {
	return middleware.VerifyCSRFToken(token, secret, sessionID)
}
