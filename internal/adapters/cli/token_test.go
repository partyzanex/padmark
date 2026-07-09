package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTokenFile points XDG_CONFIG_HOME at a temp dir and writes the given token file contents,
// returning nothing — the token then resolves from ~/.config/padmark/token under that dir.
func writeTokenFile(t *testing.T, contents string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "padmark"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "padmark", "token"), []byte(contents), 0o600))
	t.Setenv("XDG_CONFIG_HOME", dir)
}

func TestReadTokenFile_ReadsAndTrimsWhitespace(t *testing.T) {
	writeTokenFile(t, "  secret-key\n\n")

	assert.Equal(t, "secret-key", readTokenFile())
}

func TestReadTokenFile_Missing_ReturnsEmpty(t *testing.T) {
	// A config dir with no token file present.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	assert.Empty(t, readTokenFile())
}

func TestTokenFilePath_XDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config")

	path, err := tokenFilePath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/tmp/xdg-config", "padmark", "token"), path)
}

func TestTokenFilePath_HomeFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/tester")

	path, err := tokenFilePath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/home/tester", ".config", "padmark", "token"), path)
}

// captureAuthServer returns a test server that records the Authorization header of the last
// request and responds 404, so the CLI command fails after the header has been observed.
func captureAuthServer(gotAuth *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")

		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestResolveToken_ConfigFile_SentAsBearer(t *testing.T) {
	t.Setenv("PADMARK_TOKEN", "") // neutralise any ambient env token
	writeTokenFile(t, "file-token\n")

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	_ = runCLI(context.Background(), "--url", srv.URL, cmdGet, "some-id")

	assert.Equal(t, "Bearer file-token", gotAuth, "token from config file is sent as Bearer")
}

func TestResolveToken_FlagOverridesConfigFile(t *testing.T) {
	t.Setenv("PADMARK_TOKEN", "")
	writeTokenFile(t, "file-token\n")

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	_ = runCLI(context.Background(), "--url", srv.URL, "--token", "flag-token", cmdGet, "some-id")

	assert.Equal(t, "Bearer flag-token", gotAuth, "--token wins over the config file")
}

func TestResolveToken_NoTokenAnywhere_NoAuthHeader(t *testing.T) {
	t.Setenv("PADMARK_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no token file

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	_ = runCLI(context.Background(), "--url", srv.URL, cmdGet, "some-id")

	assert.Empty(t, gotAuth, "no token configured ⇒ no Authorization header")
}
