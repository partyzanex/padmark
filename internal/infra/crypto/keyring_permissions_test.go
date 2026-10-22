package crypto_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/stretchr/testify/require"
)

func TestContentKeyringWarnsOnPermissionsAndSymlinks(t *testing.T) {
	var output bytes.Buffer

	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	path := filepath.Join(t.TempDir(), "keys.json")
	ring := &crypto.ContentKeyring{Active: "key", Keys: map[string][]byte{"key": bytes.Repeat([]byte{7}, 32)}}
	require.NoError(t, crypto.WriteContentKeyring(path, ring))
	_, err := crypto.LoadContentKeyring(path)
	require.NoError(t, err)
	require.Empty(t, output.String())
	require.NoError(t, os.Chmod(path, 0o644)) //nolint:gosec // deliberately unsafe permissions under test
	_, err = crypto.LoadContentKeyring(path)
	require.NoError(t, err, "managed mounts may require read-only shared modes; warn rather than change permissions")
	require.Contains(t, output.String(), "readable by other users")
	output.Reset()
	require.NoError(t, os.Chmod(path, 0o400))
	link := filepath.Join(t.TempDir(), "managed-secret")
	require.NoError(t, os.Symlink(path, link))
	_, err = crypto.LoadContentKeyring(link)
	require.NoError(t, err)
	require.Contains(t, output.String(), "symlink")
	require.NotContains(t, output.String(), "BwcHBwcH", "no key material may be logged")
}
