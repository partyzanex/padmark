package crypto_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// writeTestKeyring publishes an owner-only keyring and then applies mode, so each test
// controls the permissions the loader observes.
func writeTestKeyring(t *testing.T, mode os.FileMode) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "keys.json")
	ring := &crypto.ContentKeyring{Active: "key", Keys: map[string][]byte{"key": bytes.Repeat([]byte{7}, 32)}}
	require.NoError(t, crypto.WriteContentKeyring(path, ring))
	require.NoError(t, os.Chmod(path, mode))

	return path
}

// captureDefaultLogger redirects the process-wide slog default into a buffer so tests can
// prove the loader never writes there: production pipelines only see the injected logger.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()

	var output bytes.Buffer

	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return &output
}

func strictLoad() crypto.KeyringLoadOptions {
	return crypto.KeyringLoadOptions{AllowSharedPermissions: false}
}

func TestLoadContentKeyring_AcceptsOwnerOnlyModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			var output bytes.Buffer

			path := writeTestKeyring(t, mode)
			_, err := crypto.LoadContentKeyring(path, slog.New(slog.NewTextHandler(&output, nil)), strictLoad())
			require.NoError(t, err)
			require.Empty(t, output.String(), "an owner-only keyring needs no warning")
		})
	}
}

func TestLoadContentKeyring_RejectsSharedPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o444} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			path := writeTestKeyring(t, mode)
			_, err := crypto.LoadContentKeyring(path, slog.New(slog.DiscardHandler), strictLoad())
			require.ErrorIs(t, err, crypto.ErrKeyringPermissions)
			require.ErrorContains(t, err, fmt.Sprintf("%04o", mode))
			require.ErrorContains(t, err, "0600 or 0400")

			info, statErr := os.Stat(path)
			require.NoError(t, statErr)
			require.Equal(t, mode, info.Mode().Perm(), "the loader must never chmod the operator's file")
		})
	}
}

func TestLoadContentKeyring_AllowSharedPermissionsWarnsThroughInjectedLogger(t *testing.T) {
	global := captureDefaultLogger(t)

	var output bytes.Buffer

	path := writeTestKeyring(t, 0o444)
	_, err := crypto.LoadContentKeyring(path, slog.New(slog.NewTextHandler(&output, nil)),
		crypto.KeyringLoadOptions{AllowSharedPermissions: true})
	require.NoError(t, err, "managed secret mounts may only offer read-only shared modes")
	require.Contains(t, output.String(), "level=WARN")
	require.Contains(t, output.String(), "readable by other users")
	require.Contains(t, output.String(), "0444")
	require.Contains(t, output.String(), path)
	require.Empty(t, global.String(), "warnings must not bypass the injected logger")

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o444), info.Mode().Perm(), "the loader must never chmod the operator's file")
}

func TestLoadContentKeyring_WarnsOnSymlinkThroughInjectedLogger(t *testing.T) {
	global := captureDefaultLogger(t)

	var output bytes.Buffer

	path := writeTestKeyring(t, 0o400)
	link := filepath.Join(t.TempDir(), "managed-secret")
	require.NoError(t, os.Symlink(path, link))
	_, err := crypto.LoadContentKeyring(link, slog.New(slog.NewTextHandler(&output, nil)), strictLoad())
	require.NoError(t, err)
	require.Contains(t, output.String(), "symlink")
	require.NotContains(t, output.String(), "BwcHBwcH", "no key material may be logged")
	require.Empty(t, global.String(), "warnings must not bypass the injected logger")
}
