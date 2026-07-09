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
	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/domain"
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

// resolvedURLFor runs the root command with the given args and returns the server URL the client
// would use, exercising the same flag/token resolution as a real invocation.
func resolvedURLFor(t *testing.T, args ...string) string {
	t.Helper()

	var got string

	app := &urcli.Command{
		Flags: globalFlags(),
		Action: func(_ context.Context, cmd *urcli.Command) error {
			got = resolveServerURL(cmd)

			return nil
		},
	}
	require.NoError(t, app.Run(context.Background(), append([]string{"padmark-cli"}, args...)))

	return got
}

func TestResolveServerURL_EmbeddedFromToken(t *testing.T) {
	t.Setenv("PADMARK_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	envelope, err := domain.EncodeAPITokenEnvelope("http://embedded:4000", "k")
	require.NoError(t, err)

	assert.Equal(t, "http://embedded:4000", resolvedURLFor(t, "--token", envelope),
		"the URL from the envelope token is used for display when --url is not set")
}

func TestResolveServerURL_ExplicitFlagWins(t *testing.T) {
	t.Setenv("PADMARK_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	envelope, err := domain.EncodeAPITokenEnvelope("http://embedded:4000", "k")
	require.NoError(t, err)

	assert.Equal(t, "http://explicit:9999",
		resolvedURLFor(t, "--url", "http://explicit:9999", "--token", envelope),
		"an explicit --url overrides the URL embedded in the token")
}

func TestResolveServerURL_DefaultWhenNoToken(t *testing.T) {
	t.Setenv("PADMARK_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	assert.Equal(t, DefaultURL, resolvedURLFor(t),
		"with no token and no --url, the default server URL is used")
}

func TestSplitToken_Envelope(t *testing.T) {
	envelope, err := domain.EncodeAPITokenEnvelope("https://notes.example.com", "the-key")
	require.NoError(t, err)

	bearer, baseURL := splitToken(envelope)
	assert.Equal(t, "the-key", bearer)
	assert.Equal(t, "https://notes.example.com", baseURL)
}

func TestSplitToken_LegacyBareKey(t *testing.T) {
	bearer, baseURL := splitToken("legacy-plain-key")
	assert.Equal(t, "legacy-plain-key", bearer, "a bare key is used verbatim as the bearer")
	assert.Empty(t, baseURL, "a bare key carries no embedded URL")
}

func TestEnvelopeToken_EmbeddedURLUsed_KeySentAsBearer(t *testing.T) {
	t.Setenv("PADMARK_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no token file

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	// The envelope carries the server URL and the command runs WITHOUT --url: reaching the test
	// server at all proves the embedded URL was used, and the header proves the key was unpacked.
	envelope, err := domain.EncodeAPITokenEnvelope(srv.URL, "envelope-key")
	require.NoError(t, err)

	_ = runCLI(context.Background(), "--token", envelope, cmdGet, "some-id")

	assert.Equal(t, "Bearer envelope-key", gotAuth,
		"the embedded URL is used and the packed key is sent as Bearer")
}

func TestEnvelopeToken_ExplicitURLOverridesEmbedded(t *testing.T) {
	t.Setenv("PADMARK_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	// The embedded URL points nowhere reachable; an explicit --url must win and hit the server.
	envelope, err := domain.EncodeAPITokenEnvelope("http://127.0.0.1:1", "envelope-key")
	require.NoError(t, err)

	_ = runCLI(context.Background(), "--url", srv.URL, "--token", envelope, cmdGet, "some-id")

	assert.Equal(t, "Bearer envelope-key", gotAuth,
		"--url overrides the embedded URL while the packed key is still sent as Bearer")
}
