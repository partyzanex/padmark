package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckInsecureToken covers the cleartext-transport policy for a bearer token: HTTPS and
// token-less runs are silent, loopback HTTP only warns, non-loopback HTTP is refused unless the
// operator opted in with --allow-insecure-http.
func TestCheckInsecureToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		serverURL string
		token     string
		allow     bool
		wantWarn  bool
		wantErr   bool
	}{
		{"remote http with token is refused", "http://example.com", "secret", false, false, true},
		{"no scheme with token is refused", "example.com:8080", "secret", false, false, true},
		{"remote http with opt-in warns", "http://example.com", "secret", true, true, false},
		{"localhost http warns", "http://localhost:8080", "secret", false, true, false},
		{"127.0.0.1 http warns", "http://127.0.0.1:8080", "secret", false, true, false},
		{"127.5.5.5 http warns", "http://127.5.5.5", "secret", false, true, false},
		{"ipv6 loopback http warns", "http://[::1]:8080", "secret", false, true, false},
		{"lookalike host is refused", "http://localhost.evil.com", "secret", false, false, true},
		{"https with token is silent", "https://example.com", "secret", false, false, false},
		{"http without token is silent", "http://example.com", "", false, false, false},
		{"https without token is silent", "https://example.com", "", false, false, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			err := checkInsecureToken(&buf, testCase.serverURL, testCase.token, testCase.allow)

			if testCase.wantErr {
				require.ErrorIs(t, err, errInsecureTransport)
				assert.Contains(t, err.Error(), FlagAllowInsecureHTTP)
				assert.Empty(t, buf.String())

				return
			}

			require.NoError(t, err)

			if testCase.wantWarn {
				assert.Contains(t, buf.String(), "cleartext")
			} else {
				assert.Empty(t, buf.String())
			}
		})
	}
}

// TestCheckInsecureToken_WiredIntoClient verifies the warning is actually emitted to the
// command's error writer when a real command builds its HTTP client with a token over http://.
func TestCheckInsecureToken_WiredIntoClient(t *testing.T) {
	isolateEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var errBuf bytes.Buffer

	app := NewApp()
	app.ErrWriter = &errBuf

	err := app.Run(t.Context(), []string{testBin, "--url", srv.URL, "--token", "secret", cmdPing})

	require.NoError(t, err)
	assert.Contains(t, errBuf.String(), "cleartext", "insecure-token warning must reach stderr")
}

// TestCheckInsecureSetup covers the same policy for the initial admin's password: refused over
// non-loopback http:// unless opted in, warn-only on loopback, silent over https://.
func TestCheckInsecureSetup(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		serverURL string
		allow     bool
		wantWarn  bool
		wantErr   bool
	}{
		{"remote http is refused", "http://example.com/setup", false, false, true},
		{"no scheme is refused", "example.com:8080", false, false, true},
		{"remote http with opt-in warns", "http://example.com/setup", true, true, false},
		{"loopback http warns", "http://127.0.0.1:8080/setup", false, true, false},
		{"https is silent", "https://example.com/setup", false, false, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			err := checkInsecureSetup(&buf, testCase.serverURL, testCase.allow)

			if testCase.wantErr {
				require.ErrorIs(t, err, errInsecureTransport)

				return
			}

			require.NoError(t, err)

			if testCase.wantWarn {
				assert.Contains(t, buf.String(), "cleartext")
			} else {
				assert.Empty(t, buf.String())
			}
		})
	}
}

// TestCheckInsecureSetup_WiredIntoAction verifies the warning is emitted to the command's error
// writer when a real setup run posts the bootstrap credentials over http://.
func TestCheckInsecureSetup_WiredIntoAction(t *testing.T) {
	isolateEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"otp_auth_uri":"otpauth://totp/padmark:root"}`))
	}))
	defer srv.Close()

	var errBuf bytes.Buffer

	app := NewApp()
	app.ErrWriter = &errBuf

	err := app.Run(t.Context(), []string{
		testBin, "--url", srv.URL, cmdSetup, "--username", "root", "--password", "FlowP@ss123!",
	})

	require.NoError(t, err)
	assert.Contains(t, errBuf.String(), "cleartext", "insecure-setup warning must reach stderr")
}
