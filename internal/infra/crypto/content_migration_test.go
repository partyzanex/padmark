package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
)

func TestRewrap_PayloadStableAndAuthenticated(t *testing.T) {
	keys := map[string][]byte{"old": bytes.Repeat([]byte{1}, 32), "new": bytes.Repeat([]byte{2}, 32)}
	old, err := NewContentCipher("old", keys, "note")
	require.NoError(t, err)
	next, err := NewContentCipher("new", keys, "note")
	require.NoError(t, err)

	id := domain.HashSlug("slug")
	legacy, err := New().Encrypt("secret", "slug")
	require.NoError(t, err)

	for _, kind := range []string{"p", "l"} {
		t.Run(kind, func(t *testing.T) {
			value, err := old.Encrypt("secret", id)
			require.NoError(t, err)

			if kind == "l" {
				value, err = old.WrapLegacy(legacy, id)
				require.NoError(t, err)
			}

			rotated, err := next.Rewrap(value, id)
			require.NoError(t, err)
			require.NotEqual(t, value, rotated)
			require.Equal(t, strings.Split(value, "$")[4], strings.Split(rotated, "$")[4])

			reader := &ContentEncryptor{ContentCipher: next}
			plain, err := reader.Decrypt(rotated, id, "slug")
			require.NoError(t, err)
			require.Equal(t, "secret", plain)

			unchanged, err := next.Rewrap(rotated, id)
			require.NoError(t, err)
			require.Equal(t, rotated, unchanged)

			_, err = next.Rewrap(value, domain.HashSlug("other"))
			require.Error(t, err)
			_, err = next.Rewrap(rotated+"x", id)
			require.Error(t, err)

			if kind == "l" {
				_, err = reader.Decrypt(rotated, id, "wrong")
				require.Error(t, err)
				_, err = next.Decrypt(rotated, id)
				require.Error(t, err, "native decryption must not return legacy ciphertext as content")
			}
		})
	}
}

func TestLegacyMigration_RejectMalformedAndVersionedInputs(t *testing.T) {
	enc := contentTestCipher(t, "note")

	id := domain.HashSlug("slug")
	for _, value := range []string{"", "Zg==", "not base64", "pmc2$l$test$bad$bad", "pmc3$p$test$bad$bad"} {
		_, err := enc.WrapLegacy(value, id)
		require.Error(t, err)
	}
}

func TestKeyRegistryCheck_BindsInstallationAndKeyID(t *testing.T) {
	ring := &ContentKeyring{Active: "key", Keys: map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)}}
	check, err := ring.KeyCheck("installation-a", "key")
	require.NoError(t, err)
	require.NoError(t, ring.VerifyKeyCheck("installation-a", "key", check))
	require.Error(t, ring.VerifyKeyCheck("installation-b", "key", check))
	ring.Keys["alias"] = ring.Keys["key"]
	require.Error(t, ring.VerifyKeyCheck("installation-a", "alias", check))
	ring.Keys["key"] = bytes.Repeat([]byte{2}, 32)
	require.Error(t, ring.VerifyKeyCheck("installation-a", "key", check))
	delete(ring.Keys, "key")
	require.Error(t, ring.VerifyKeyCheck("installation-a", "key", check))
}

func TestKeyringCreation_ExclusiveAndPrivate(t *testing.T) {
	ring := &ContentKeyring{Active: "first"}
	require.NoError(t, ring.AddKey("first"))
	require.Len(t, ring.Keys["first"], 32)
	require.NoError(t, ring.AddKey("second"))
	require.NotEqual(t, ring.Keys["first"], ring.Keys["second"])
	require.Error(t, ring.AddKey("first"))
	require.Error(t, ring.AddKey("bad$key"))
	path := filepath.Join(t.TempDir(), "content-keys.json")
	require.NoError(t, WriteContentKeyring(path, ring))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	loaded, err := LoadContentKeyring(path)
	require.NoError(t, err)
	require.Equal(t, ring, loaded)
	require.Error(t, WriteContentKeyring(path, ring), "must not overwrite an existing keyring")
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".padmark-keyring-*"))
	require.NoError(t, err)
	require.Empty(t, files)
}
