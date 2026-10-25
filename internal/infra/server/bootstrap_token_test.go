package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBootstrapToken(t *testing.T) {
	t.Run("empty keeps first-admin setup closed", func(t *testing.T) {
		got, err := parseBootstrapToken("")
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("minimum length is accepted", func(t *testing.T) {
		raw := strings.Repeat("k", minBootstrapTokenLen)

		got, err := parseBootstrapToken(raw)
		require.NoError(t, err)
		require.Equal(t, raw, got)
	})

	t.Run("short token is rejected without echoing it", func(t *testing.T) {
		raw := strings.Repeat("s", minBootstrapTokenLen-1)

		_, err := parseBootstrapToken(raw)
		require.ErrorContains(t, err, EnvBootstrapToken+" must be at least 32 characters")
		require.NotContains(t, err.Error(), raw, "the token value must never appear in errors")
	})
}

// TestServerRejectsShortBootstrapTokenBeforeOpeningDB: a guessable bootstrap token fails startup
// before the keyring or the database is touched.
func TestServerRejectsShortBootstrapTokenBeforeOpeningDB(t *testing.T) {
	t.Setenv(EnvBootstrapToken, "too-short")

	dir := t.TempDir()
	dsn := filepath.Join(dir, "padmark.db")

	err := NewApp().Run(t.Context(), []string{"padmark", "serve", "--dsn", dsn})

	require.ErrorContains(t, err, "validate bootstrap token")
	require.NotContains(t, err.Error(), "too-short")
	require.NotContains(t, err.Error(), "keyring", "configuration error must precede keyring startup")

	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Empty(t, entries, "no database (or journal) file may be created")
}
