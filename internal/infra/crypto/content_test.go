package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
)

func contentTestCipher(tb testing.TB, resource string) *ContentCipher {
	tb.Helper()

	enc, err := NewContentCipher("test", map[string][]byte{"test": bytes.Repeat([]byte{42}, 32)}, resource)
	require.NoError(tb, err)

	return enc
}

func TestContentCipher_RoundTripWithoutSlug(t *testing.T) {
	// A stored ID is sufficient; the original slug is deliberately not supplied to either operation.
	id := strings.Repeat("ab", 32)
	for _, resource := range []string{"note", "link"} {
		for _, plaintext := range []string{"", "hello", "Привет 🌍", strings.Repeat("x", 65536)} {
			writer := contentTestCipher(t, resource)
			sealed, err := writer.Encrypt(plaintext, id)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(sealed, "pmc2$p$test$"))
			// Constructing a new instance models restart with the persisted keyring.
			reader := contentTestCipher(t, resource)
			opened, err := reader.Decrypt(sealed, id)
			require.NoError(t, err)
			require.Equal(t, plaintext, opened)

			adapter := &ContentEncryptor{ContentCipher: reader}
			opened, err = adapter.Decrypt(sealed, id, "this is not the original slug")
			require.NoError(t, err)
			require.Equal(t, plaintext, opened)
		}
	}
}

func TestContentCipher_FreshDEKsAndNonces(t *testing.T) {
	enc := contentTestCipher(t, "note")
	id := domain.HashSlug("oncall-runbook")
	aad, err := enc.contentAAD(id)
	require.NoError(t, err)

	var previousKey, previousPayload, previousWrapped []byte

	for range 3 {
		sealed, sealErr := enc.Encrypt("same content", id)
		require.NoError(t, sealErr)

		kid, wrapped, payload, parseErr := parseContentEnvelope(sealed)
		require.NoError(t, parseErr)

		dek, openErr := enc.keys[kid].Open(nil, wrapped[:12], wrapped[12:], wrappedKeyAAD(aad, kid, payload))
		require.NoError(t, openErr)
		require.Len(t, dek, 32)
		require.NotEqual(t, previousKey, dek)

		if previousPayload != nil {
			require.NotEqual(t, previousPayload[:12], payload[:12])
			require.NotEqual(t, previousWrapped[:12], wrapped[:12])
		}

		previousKey, previousPayload, previousWrapped = dek, payload, wrapped
		_, legacyErr := New().Decrypt(sealed, "oncall-runbook")
		require.Error(t, legacyErr, "knowing the slug must not decrypt a new record")
	}
}

func TestContentCipher_RejectsTampering(t *testing.T) {
	enc := contentTestCipher(t, "note")
	enc.keys["alias"] = enc.keys["test"] // Same bytes under another ID must still fail authentication.
	id := domain.HashSlug("known-slug")
	sealed, err := enc.Encrypt("secret", id)
	require.NoError(t, err)
	other, err := enc.Encrypt("other secret", id)
	require.NoError(t, err)

	parts := strings.Split(sealed, "$")
	otherParts := strings.Split(other, "$")

	cases := map[string]string{
		"version":       strings.Replace(sealed, "pmc2", "pmc3", 1),
		"kind":          strings.Replace(sealed, "$p$", "$l$", 1),
		"aliased key":   strings.Replace(sealed, "$test$", "$alias$", 1),
		"unknown key":   strings.Replace(sealed, "$test$", "$unknown$", 1),
		"extra field":   sealed + "$extra",
		"truncated":     strings.Join(parts[:4], "$"),
		"padded":        sealed + "=",
		"newline":       sealed + "\n",
		"mixed payload": strings.Join(append(append([]string{}, parts[:4]...), otherParts[4]), "$"),
		"mixed key":     strings.Join([]string{parts[0], parts[1], parts[2], otherParts[3], parts[4]}, "$"),
	}
	for _, field := range []int{3, 4} {
		changed := append([]string{}, parts...)
		raw, decodeErr := base64.RawURLEncoding.DecodeString(changed[field])
		require.NoError(t, decodeErr)

		raw[len(raw)-1] ^= 1
		changed[field] = base64.RawURLEncoding.EncodeToString(raw)
		cases[[]string{"wrapped tag", "payload tag"}[field-3]] = strings.Join(changed, "$")
	}

	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			adapter := &ContentEncryptor{ContentCipher: enc}
			opened, openErr := adapter.Decrypt(value, id, "known-slug")
			require.Error(t, openErr)
			require.Empty(t, opened)
		})
	}

	_, err = enc.Decrypt(sealed, domain.HashSlug("another-record"))
	require.Error(t, err)
	_, err = contentTestCipher(t, "link").Decrypt(sealed, id)
	require.Error(t, err, "note/link separation must hold even for the same ID")
	wrongKey, err := NewContentCipher("test", map[string][]byte{"test": bytes.Repeat([]byte{99}, 32)}, "note")
	require.NoError(t, err)
	_, err = wrongKey.Decrypt(sealed, id)
	require.Error(t, err)
}

func TestContentCipher_KeyringAndRandomFailures(t *testing.T) {
	for _, length := range []int{0, 16, 24, 31, 33} {
		_, err := NewContentCipher("test", map[string][]byte{"test": make([]byte, length)}, "note")
		require.Error(t, err)
	}

	for _, kid := range []string{"", "$", "space key", strings.Repeat("k", 65)} {
		_, err := NewContentCipher(kid, map[string][]byte{kid: make([]byte, 32)}, "note")
		require.Error(t, err)
	}

	_, err := NewContentCipher("missing", map[string][]byte{"test": make([]byte, 32)}, "note")
	require.Error(t, err)
	_, err = NewContentCipher("test", map[string][]byte{"test": make([]byte, 32)}, "user")
	require.Error(t, err)

	for _, available := range []int{0, 32, 44} {
		enc := contentTestCipher(t, "note")
		enc.random = bytes.NewReader(make([]byte, available))
		sealed, sealErr := enc.Encrypt("content", domain.HashSlug("id"))
		require.Error(t, sealErr)
		require.Empty(t, sealed)
	}

	for _, id := range []string{"slug", strings.Repeat("z", 64)} {
		_, err = contentTestCipher(t, "note").Encrypt("content", id)
		require.Error(t, err)
	}
}

func TestContentCipher_ActiveKeyChangeAndKeyCopy(t *testing.T) {
	keys := map[string][]byte{"old": bytes.Repeat([]byte{1}, 32), "new": bytes.Repeat([]byte{2}, 32)}
	old, err := NewContentCipher("old", keys, "note")
	require.NoError(t, err)
	next, err := NewContentCipher("new", keys, "note")
	require.NoError(t, err)
	clear(keys["old"])
	delete(keys, "new")

	id := domain.HashSlug("id")
	sealed, err := old.Encrypt("old content", id)
	require.NoError(t, err)
	opened, err := next.Decrypt(sealed, id)
	require.NoError(t, err)
	require.Equal(t, "old content", opened)

	newValue, err := next.Encrypt("new content", id)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(newValue, "pmc2$p$new$"))
	_, err = old.Decrypt(newValue, id)
	require.NoError(t, err, "replicas with both keys can read either active key")
}

func TestContentEncryptor_LegacyReadOnly(t *testing.T) {
	legacy, err := New().Encrypt("existing note", "legacy-slug")
	require.NoError(t, err)
	enc := &ContentEncryptor{ContentCipher: contentTestCipher(t, "note")}
	id := domain.HashSlug("legacy-slug")
	_, err = enc.Decrypt(legacy, id, "legacy-slug")
	require.Error(t, err, "normal reads must reject raw legacy")
	legacy, err = enc.WrapLegacy(legacy, id)
	require.NoError(t, err)
	opened, err := enc.Decrypt(legacy, id, "legacy-slug")
	require.NoError(t, err)
	require.Equal(t, "existing note", opened)

	_, err = enc.Decrypt(legacy, id, "wrong-slug")
	require.Error(t, err)
	_, err = enc.ContentCipher.Decrypt(legacy, id)
	require.Error(t, err, "the new cipher itself must never interpret legacy ciphertext")
	updated, err := enc.Encrypt(opened, id)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(updated, "pmc2$p$"))
}

func TestLoadContentKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	data, err := json.Marshal(ContentKeyring{Active: "key1", Keys: map[string][]byte{"key1": bytes.Repeat([]byte{7}, 32)}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	keyring, err := LoadContentKeyring(path)
	require.NoError(t, err)
	_, err = keyring.ForResource("link")
	require.NoError(t, err)

	for _, bad := range []string{
		"", "null", string(data) + " {}", string(data) + strings.Repeat(" ", maxKeyringSize),
		`{"active":"key1","keys":{"key1":"secret-should-not-appear-in-errors"}}`,
		`{"active":"missing","keys":{}}`, `{"active":"key1","keys":{},"typo":true}`,
	} {
		require.NoError(t, os.WriteFile(path, []byte(bad), 0o600))
		_, err = LoadContentKeyring(path)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret-should-not-appear-in-errors")
	}

	_, err = LoadContentKeyring("")
	require.Error(t, err)
	_, err = LoadContentKeyring(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func FuzzContentCipherDecrypt(f *testing.F) {
	enc := contentTestCipher(f, "note")
	id := domain.HashSlug("fuzz")
	valid, err := enc.Encrypt("content", id)
	require.NoError(f, err)
	f.Add(valid)
	f.Add("pmc2$p$test$$")
	f.Add("legacy")
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 4096 {
			t.Skip()
		}

		_, _ = enc.Decrypt(input, id)
	})
}
