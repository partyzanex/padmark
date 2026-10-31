package server

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultKeyringPath(t *testing.T) {
	cases := []struct {
		name, storage, dsn, want string
	}{
		{"bare relative", "sqlite", "padmark.db", "padmark-keys.json"},
		{"bare with query", "sqlite", "data/padmark.db?_pragma=foreign_keys(1)", filepath.Join("data", "padmark-keys.json")},
		{"absolute", "sqlite", "/var/lib/padmark/padmark.db", "/var/lib/padmark/padmark-keys.json"},
		{
			"file uri with query", "sqlite", "file:/var/lib/padmark/p.db?cache=shared&mode=rwc",
			"/var/lib/padmark/padmark-keys.json",
		},
		{"in-memory", "sqlite", ":memory:", ""},
		{"file memory uri", "sqlite", "file::memory:?cache=shared", ""},
		{"empty dsn", "sqlite", "", ""},
		{"postgres", "postgres", "postgres://u:p@h/db", "padmark-keys.json"},
	}

	for _, tc := range cases {
		require.Equal(t, tc.want, defaultKeyringPath(tc.storage, tc.dsn), tc.name)
	}
}

func TestResolveKeyringPathErrors(t *testing.T) {
	path, derived, err := resolveKeyringPath("", "postgres", "postgres://u:p@h/db")
	require.NoError(t, err)
	require.Equal(t, "padmark-keys.json", path)
	require.True(t, derived)

	_, _, err = resolveKeyringPath("", "sqlite", ":memory:")
	require.ErrorContains(t, err, "content keyring is required")

	path, derived, err = resolveKeyringPath("/x/keys.json", "postgres", "")
	require.NoError(t, err)
	require.Equal(t, "/x/keys.json", path)
	require.False(t, derived)
}

func TestLoadServeKeyringCreatesThenReuses(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "padmark.db")
	keyPath := filepath.Join(filepath.Dir(dsn), defaultKeyringName)

	var logs bytes.Buffer

	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn,
		AddSource: false, ReplaceAttr: nil,
	}))

	first, err := loadServeKeyring("", "sqlite", dsn, false, log)
	require.NoError(t, err)
	require.Equal(t, "k1", first.Active)
	require.Contains(t, logs.String(), "level=WARN")
	require.Contains(t, logs.String(), keyPath)
	require.Contains(t, logs.String(), "back it up")

	info, err := os.Stat(keyPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	before, err := os.ReadFile(keyPath) //nolint:gosec // test-controlled temp path
	require.NoError(t, err)

	logs.Reset()

	second, err := loadServeKeyring("", "sqlite", dsn, false, log)
	require.NoError(t, err)
	require.Equal(t, first.Active, second.Active)
	require.Equal(t, first.Keys["k1"], second.Keys["k1"])
	require.NotContains(t, logs.String(), "generated")

	after, err := os.ReadFile(keyPath) //nolint:gosec // test-controlled temp path
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestLoadServeKeyringExplicitMissingFails(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "missing.json")

	_, err := loadServeKeyring(explicit, "sqlite", filepath.Join(dir, "padmark.db"), false, slog.New(slog.DiscardHandler))
	require.Error(t, err)
	require.NoFileExists(t, explicit)
	require.NoFileExists(t, filepath.Join(dir, defaultKeyringName))
}

func TestLoadServeKeyringPostgresCreatesDefault(t *testing.T) {
	t.Chdir(t.TempDir())

	ring, err := loadServeKeyring("", "postgres", "postgres://h/db", false, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.Equal(t, "k1", ring.Active)
	require.FileExists(t, defaultKeyringName)
}

func TestLoadToolKeyringDoesNotCreate(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "padmark.db")

	_, err := loadToolKeyring("", "sqlite", dsn, false, slog.New(slog.DiscardHandler))
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(dir, defaultKeyringName))
}
