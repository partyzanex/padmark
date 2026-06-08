package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWarnInsecureToken covers the advisory warning emitted when a bearer token would travel in
// cleartext: warn for non-HTTPS URLs when a token is set, stay silent otherwise.
func TestWarnInsecureToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		serverURL string
		token     string
		wantWarn  bool
	}{
		{"http with token warns", "http://example.com", "secret", true},
		{"no scheme with token warns", "example.com:8080", "secret", true},
		{"https with token is silent", "https://example.com", "secret", false},
		{"http without token is silent", "http://example.com", "", false},
		{"https without token is silent", "https://example.com", "", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			warnInsecureToken(&buf, testCase.serverURL, testCase.token)

			if testCase.wantWarn {
				assert.Contains(t, buf.String(), "cleartext")
			} else {
				assert.Empty(t, buf.String())
			}
		})
	}
}

// TestWarnInsecureToken_WiredIntoClient verifies the warning is actually emitted to the
// command's error writer when a real command builds its HTTP client with a token over http://.
func TestWarnInsecureToken_WiredIntoClient(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var errBuf bytes.Buffer

	app := NewApp()
	app.ErrWriter = &errBuf

	err := app.Run(context.Background(), []string{testBin, "--url", srv.URL, "--token", "secret", cmdPing})

	require.NoError(t, err)
	assert.Contains(t, errBuf.String(), "cleartext", "insecure-token warning must reach stderr")
}
