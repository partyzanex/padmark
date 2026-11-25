package clientauth_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/clientauth"
)

// TestCheckInsecure covers the cleartext-transport policy of a credential: HTTPS is silent,
// loopback HTTP only warns, non-loopback HTTP is refused unless the operator opted in with
// --allow-insecure-http.
func TestCheckInsecure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		serverURL string
		allow     clientauth.InsecureHTTP
		wantWarn  bool
		wantErr   bool
	}{
		{"remote http is refused", "http://example.com", false, false, true},
		{"no scheme is refused", "example.com:8080", false, false, true},
		{"remote http with opt-in warns", "http://example.com/setup", true, true, false},
		{"localhost http warns", "http://localhost:8080", false, true, false},
		{"127.0.0.1 http warns", "http://127.0.0.1:8080/setup", false, true, false},
		{"127.5.5.5 http warns", "http://127.5.5.5", false, true, false},
		{"ipv6 loopback http warns", "http://[::1]:8080", false, true, false},
		{"lookalike host is refused", "http://localhost.evil.com", false, false, true},
		{"https is silent", "https://example.com", false, false, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			err := clientauth.CheckInsecure(&buf, testCase.serverURL, "the secret", testCase.allow)

			if testCase.wantErr {
				require.ErrorIs(t, err, clientauth.ErrInsecureTransport)
				assert.Contains(t, err.Error(), clientauth.FlagAllowInsecureHTTP)
				assert.Contains(t, err.Error(), "the secret to "+testCase.serverURL)
				assert.Empty(t, buf.String())

				return
			}

			require.NoError(t, err)

			if testCase.wantWarn {
				assert.Equal(t, "warning: the secret will be sent in cleartext over a non-HTTPS URL ("+
					testCase.serverURL+"); use an https:// server URL\n", buf.String())
			} else {
				assert.Empty(t, buf.String())
			}
		})
	}
}

// TestCheckInsecure_UnwritableWarningRefuses: when the cleartext warning cannot be written, the
// credential is not sent: the warning is the only notice to the user.
func TestCheckInsecure_UnwritableWarningRefuses(t *testing.T) {
	t.Parallel()

	err := clientauth.CheckInsecure(brokenWriter{}, "http://127.0.0.1:8080", "the secret", clientauth.InsecureHTTPDenied)

	require.ErrorIs(t, err, errBrokenWriter)
	assert.ErrorContains(t, err, "write cleartext warning")
}

var errBrokenWriter = errors.New("broken writer")

// brokenWriter fails each write.
type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errBrokenWriter }
