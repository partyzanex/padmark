package http

// GenerateCSRFTokenForTest creates a valid HMAC-signed CSRF token for use in external test packages.
// Panics on error (test helper).
func GenerateCSRFTokenForTest(secret []byte) string {
	tok, err := generateCSRFToken(secret)
	if err != nil {
		panic("GenerateCSRFTokenForTest: " + err.Error())
	}

	return tok
}
