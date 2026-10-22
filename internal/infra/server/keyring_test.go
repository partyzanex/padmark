package server

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

func writeSharedTestKeyring(t *testing.T, mode os.FileMode) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "keys.json")
	ring := &crypto.ContentKeyring{Active: "key", Keys: map[string][]byte{"key": bytes.Repeat([]byte{3}, 32)}}
	require.NoError(t, crypto.WriteContentKeyring(path, ring))
	require.NoError(t, os.Chmod(path, mode))

	return path
}

func TestAllowSharedKeyring(t *testing.T) {
	for _, item := range []struct {
		path string
		flag bool
		want bool
	}{
		{"/run/secrets/padmark_content_keys", false, true},
		{"/run/secrets/nested/keys.json", false, true},
		{"/run/secrets//padmark_content_keys", false, true},
		{"/run/secrets", false, false},
		{"/run/secrets/", false, false},
		{"/run/secretsevil/keys.json", false, false},
		{"/run/secrets/../../etc/keys.json", false, false},
		{"run/secrets/keys.json", false, false},
		{"/home/padmark/keys.json", false, false},
		{"/home/padmark/keys.json", true, true},
		{"", true, true},
	} {
		require.Equal(t, item.want, allowSharedKeyring(item.path, item.flag), "%q flag=%v", item.path, item.flag)
	}
}

func TestLoadContentKeyringRefusesSharedModeWithoutOverride(t *testing.T) {
	path := writeSharedTestKeyring(t, 0o644)
	_, err := loadContentKeyring(path, false, slog.New(slog.DiscardHandler))
	require.ErrorIs(t, err, crypto.ErrKeyringPermissions)
	require.ErrorContains(t, err, "0644")
	require.ErrorContains(t, err, "0600 or 0400")
	require.ErrorContains(t, err, "--"+FlagAllowSharedKeyring)
}

func TestLoadContentKeyringOverrideWarnsThroughServerLogger(t *testing.T) {
	var output bytes.Buffer

	path := writeSharedTestKeyring(t, 0o640)
	ring, err := loadContentKeyring(path, true, slog.New(slog.NewJSONHandler(&output, nil)))
	require.NoError(t, err)
	require.Equal(t, "key", ring.Active)
	require.Contains(t, output.String(), `"level":"WARN"`)
	require.Contains(t, output.String(), "readable by other users")
	require.Contains(t, output.String(), "0640")
}

func TestServeRefusesSharedKeyringBeforeOpeningDatabase(t *testing.T) {
	t.Setenv(EnvAllowSharedKeyring, "")

	dsn := filepath.Join(t.TempDir(), "test.db")
	path := writeSharedTestKeyring(t, 0o644)
	err := NewApp().Run(t.Context(), []string{"padmark", "serve", "--storage", "sqlite",
		"--dsn", dsn, "--content-keyring-file", path, "--addr", "invalid-listen-address"})
	require.ErrorIs(t, err, crypto.ErrKeyringPermissions)
	require.ErrorContains(t, err, "--"+FlagAllowSharedKeyring)
	require.NoFileExists(t, dsn, "a refused keyring must stop startup before the database is opened")
}

func TestEncryptionCommandRefusesSharedKeyringWithoutOverride(t *testing.T) {
	t.Setenv(EnvAllowSharedKeyring, "")

	dsn := filepath.Join(t.TempDir(), "test.db")
	path := writeSharedTestKeyring(t, 0o644)
	_, err := runEncryptionTestCommand(t, dsn, path, "status")
	require.ErrorIs(t, err, crypto.ErrKeyringPermissions)
	require.ErrorContains(t, err, "--"+FlagAllowSharedKeyring)
}

func TestAllowSharedKeyringFlagAndEnv(t *testing.T) {
	for _, item := range []struct {
		env  string
		args []string
		want bool
	}{
		{"", []string{"padmark"}, false},
		{"true", []string{"padmark"}, true},
		{"", []string{"padmark", "--" + FlagAllowSharedKeyring}, true},
	} {
		t.Setenv(EnvAllowSharedKeyring, item.env)

		app := &cli.Command{Flags: appFlags(), Action: func(_ context.Context, cmd *cli.Command) error {
			require.Equal(t, item.want, cmd.Bool(FlagAllowSharedKeyring))

			return nil
		}}
		require.NoError(t, app.Run(t.Context(), item.args))
	}
}
