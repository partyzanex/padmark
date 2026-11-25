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

	"github.com/partyzanex/padmark/internal/clifactory"
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

// captureAuthServer returns a test server that records the Authorization header of the last
// request and responds 404, so the CLI command fails after the header has been observed.
func captureAuthServer(gotAuth *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")

		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestResolveToken_ConfigFile_SentAsBearer(t *testing.T) {
	isolateEnv(t)
	writeTokenFile(t, "file-token\n")

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	_ = runCLI(t.Context(), "--url", srv.URL, cmdGet, "some-id")

	assert.Equal(t, "Bearer file-token", gotAuth, "token from config file is sent as Bearer")
}

func TestResolveToken_FlagOverridesConfigFile(t *testing.T) {
	isolateEnv(t)
	writeTokenFile(t, "file-token\n")

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	_ = runCLI(t.Context(), "--url", srv.URL, "--token", "flag-token", cmdGet, "some-id")

	assert.Equal(t, "Bearer flag-token", gotAuth, "--token wins over the config file")
}

func TestResolveToken_NoTokenAnywhere_NoAuthHeader(t *testing.T) {
	isolateEnv(t)

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	_ = runCLI(t.Context(), "--url", srv.URL, cmdGet, "some-id")

	assert.Empty(t, gotAuth, "no token configured ⇒ no Authorization header")
}

// resolvedURLFor runs the root command with the given args and returns the server URL the client
// would use, exercising the same flag/token resolution as a real invocation.
func resolvedURLFor(t *testing.T, args ...string) string {
	t.Helper()

	var got string

	app := clifactory.Command(func(command *urcli.Command) {
		command.Flags = globalFlags()
		command.Action = func(_ context.Context, cmd *urcli.Command) error {
			var err error

			got, err = resolveServerURL(cmd)

			return err
		}
	})
	require.NoError(t, app.Run(t.Context(), append([]string{"padmark-cli"}, args...)))

	return got
}

func TestResolveServerURL_EmbeddedFromToken(t *testing.T) {
	isolateEnv(t)

	envelope, err := domain.EncodeAPITokenEnvelope("http://embedded:4000", "k")
	require.NoError(t, err)

	assert.Equal(t, "http://embedded:4000", resolvedURLFor(t, "--token", envelope),
		"the URL from the envelope token is used for display when --url is not set")
}

func TestResolveServerURL_ExplicitFlagWins(t *testing.T) {
	isolateEnv(t)

	envelope, err := domain.EncodeAPITokenEnvelope("http://embedded:4000", "k")
	require.NoError(t, err)

	assert.Equal(t, "http://explicit:9999",
		resolvedURLFor(t, "--url", "http://explicit:9999", "--token", envelope),
		"an explicit --url overrides the URL embedded in the token")
}

func TestResolveServerURL_DefaultWhenNoToken(t *testing.T) {
	isolateEnv(t)

	assert.Equal(t, DefaultURL, resolvedURLFor(t),
		"with no token and no --url, the default server URL is used")
}

func TestEnvelopeToken_EmbeddedURLUsed_KeySentAsBearer(t *testing.T) {
	isolateEnv(t)

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	// The envelope carries the server URL and the command runs WITHOUT --url: reaching the test
	// server at all proves the embedded URL was used, and the header proves the key was unpacked.
	// The pinned origin is recorded as trusted first — the trust-on-first-use gate (trust.go)
	// would otherwise stop the request before it leaves the machine.
	envelope, err := domain.EncodeAPITokenEnvelope(srv.URL, "envelope-key")
	require.NoError(t, err)

	require.NoError(t, runCLI(t.Context(), "--token", envelope, cmdTrust))

	_ = runCLI(t.Context(), "--token", envelope, cmdGet, "some-id")

	assert.Equal(t, "Bearer envelope-key", gotAuth,
		"the embedded URL is used and the packed key is sent as Bearer")
}

func TestEnvelopeToken_ExplicitURLOverridesEmbedded(t *testing.T) {
	isolateEnv(t)

	var gotAuth string

	srv := captureAuthServer(&gotAuth)
	defer srv.Close()

	// The embedded URL points nowhere reachable; an explicit --url must win and hit the server.
	envelope, err := domain.EncodeAPITokenEnvelope("http://127.0.0.1:1", "envelope-key")
	require.NoError(t, err)

	_ = runCLI(t.Context(), "--url", srv.URL, "--token", envelope, cmdGet, "some-id")

	assert.Equal(t, "Bearer envelope-key", gotAuth,
		"--url overrides the embedded URL while the packed key is still sent as Bearer")
}
