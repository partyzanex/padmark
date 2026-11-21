package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Shared test fixtures: the binary name and command names are defined once here so test files
// reference these constants instead of repeating string literals (which would trip goconst).
const (
	testBin   = "padmark-cli"
	cmdCreate = "create"
	cmdEdit   = "edit"
	cmdPing   = "ping"
	cmdGet    = "get"
	cmdSetup  = "setup"
	cmdTrust  = "trust"
)

// runCLI runs the root command with the binary name prepended, mirroring real argv.
func runCLI(ctx context.Context, args ...string) error {
	return NewApp().Run(ctx, append([]string{testBin}, args...))
}

// isolateEnv detaches a test from the developer's environment: the PADMARK_* variables the
// CLI reads are unset (not merely emptied — urfave treats an empty env var as an explicit
// value) and HOME/XDG_CONFIG_HOME point at a fresh temp dir, so a command can never read or
// chmod the real token file or known_hosts. It returns the isolated XDG config dir.
// t.Setenv makes the caller incompatible with t.Parallel, which is the point.
func isolateEnv(t *testing.T) string {
	t.Helper()

	for _, key := range []string{EnvURL, EnvToken, EnvTimeout, EnvEditCode, EnvBootstrapToken} {
		t.Setenv(key, "") // registers the restore of the original value.
		require.NoError(t, os.Unsetenv(key))
	}

	home := t.TempDir()
	configDir := filepath.Join(home, ".config")

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	return configDir
}

// runCLIOutput runs the root command and returns what the command wrote to its standard output.
func runCLIOutput(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer

	app := NewApp()
	app.Writer = &out
	app.ErrWriter = io.Discard

	err := app.Run(ctx, append([]string{testBin}, args...))

	return out.String(), err
}

// fileInfoJSON is a GET /files/{id} response body for the tests. json.Marshal escapes the name
// as JSON; Go %q escapes such as \x01 are not valid JSON.
func fileInfoJSON(t *testing.T, id, name string) string {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"id": id, "url": "https://pad.example/" + id, "name": name, "size": 5, "privacy": "public",
		"downloads": 0, "burn_after_download": false, "created_at": "2026-11-20T10:00:00Z",
	})
	require.NoError(t, err)

	return string(body)
}

// writeTestResponse writes a response with a JSON body.
func writeTestResponse(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_, err := io.WriteString(w, body)
	if err != nil {
		t.Errorf("write test response: %v", err)
	}
}

// outputFields reads the "key: value" lines of a text result.
func outputFields(out string) map[string]string {
	fields := make(map[string]string)

	for line := range strings.SplitSeq(out, "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			fields[key] = strings.TrimSpace(value)
		}
	}

	return fields
}
