package http

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyCSRFToken_RejectsEmptySecret(t *testing.T) {
	// This signature is known to anyone without a server secret. It matched the
	// verifier's deterministic empty-key HMAC before the fail-closed check.
	nonce := "chosen-nonce"
	forged := nonce + "." + base64.RawURLEncoding.EncodeToString(csrfHMAC(nonce, csrfAnonymousBinding, nil))
	require.False(t, verifyCSRFToken(forged, nil, ""))
	require.False(t, verifyCSRFToken(forged, []byte{}, ""))

	secret := []byte("padmark-test-csrf-secret-32bytes")
	valid, err := generateCSRFToken(secret, "")
	require.NoError(t, err)
	require.True(t, verifyCSRFToken(valid, secret, ""))
	require.False(t, verifyCSRFToken(valid, nil, ""))
	require.False(t, verifyCSRFToken(valid, []byte("different-secret"), ""))
	require.False(t, verifyCSRFToken(forged, secret, ""))
}

func TestCSRFGuard_EmptySecretCannotAuthorizePOST(t *testing.T) {
	for _, secret := range [][]byte{nil, {}} {
		nonce := "attacker-nonce"
		forged := nonce + "." + base64.RawURLEncoding.EncodeToString(csrfHMAC(nonce, csrfAnonymousBinding, secret))
		form := url.Values{csrfFieldName: {forged}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: forged}) //nolint:gosec // G124: test cookie

		called := false
		guard := (&common{csrfSecret: secret}).guard(func(w http.ResponseWriter, _ *http.Request) {
			called = true

			w.WriteHeader(http.StatusNoContent)
		})
		rec := httptest.NewRecorder()
		guard(rec, req)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.False(t, called, "an unconfigured handler must reject even a matching forged cookie and form")
	}
}
