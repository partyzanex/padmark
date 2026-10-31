package cli

import (
	"context"
	"os"
	"path/filepath"
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
