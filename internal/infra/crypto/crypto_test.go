package crypto_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// TestDecrypt_MalformedCiphertext_IsTagged verifies structurally-invalid ciphertext is
// reported as domain.ErrMalformedCiphertext, so the usecase can log corruption distinctly.
func TestDecrypt_MalformedCiphertext_IsTagged(t *testing.T) {
	enc := crypto.New()

	// Invalid base64.
	_, err := enc.Decrypt("not valid base64 !!!", "slug")
	require.Error(t, err)
	require.ErrorIs(t, err, domain.ErrMalformedCiphertext)

	// Valid base64 but shorter than the GCM nonce.
	_, err = enc.Decrypt("YWI=", "slug") // "ab"
	require.Error(t, err)
	require.ErrorIs(t, err, domain.ErrMalformedCiphertext)
}

// TestDecrypt_WrongKey_NotMalformed verifies a wrong-slug (wrong key) decrypt failure is NOT
// tagged as malformed — it is an ordinary undecryptable miss, logged at warn level upstream.
func TestDecrypt_WrongKey_NotMalformed(t *testing.T) {
	enc := crypto.New()

	ciphertext, err := enc.Encrypt("secret", "slug-A")
	require.NoError(t, err)

	_, err = enc.Decrypt(ciphertext, "slug-B")
	require.Error(t, err)
	require.NotErrorIs(t, err, domain.ErrMalformedCiphertext,
		"wrong-key failure must not be tagged as malformed ciphertext")
}

func TestEncryptor_Roundtrip(t *testing.T) {
	enc := crypto.New()

	for _, testCase := range []struct {
		name      string
		plaintext string
		slug      string
	}{
		{"simple", "hello world", "my-slug-key"},
		{"empty content", "", "some-slug"},
		{"markdown", "# Hello\n\n**bold**", "abc123"},
		{"unicode", "привет мир 🌍", "unicode-slug"},
		{"long content", strings.Repeat("x", 65536), "long-slug"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ciphertext, err := enc.Encrypt(testCase.plaintext, testCase.slug)
			require.NoError(t, err)

			plaintext, err := enc.Decrypt(ciphertext, testCase.slug)
			require.NoError(t, err)
			assert.Equal(t, testCase.plaintext, plaintext)
		})
	}
}

func TestEncryptor_DifferentKeysProduceDifferentCiphertexts(t *testing.T) {
	enc := crypto.New()
	ct1, err := enc.Encrypt("secret", "slug-a")
	require.NoError(t, err)

	ct2, err := enc.Encrypt("secret", "slug-b")
	require.NoError(t, err)

	assert.NotEqual(t, ct1, ct2)
}

func TestEncryptor_SamePlaintextProducesDifferentCiphertexts(t *testing.T) {
	enc := crypto.New()
	ct1, err := enc.Encrypt("hello", "slug")
	require.NoError(t, err)

	ct2, err := enc.Encrypt("hello", "slug")
	require.NoError(t, err)

	// nonce is random — same plaintext must produce different ciphertexts
	assert.NotEqual(t, ct1, ct2)
}

func TestEncryptor_WrongKeyFails(t *testing.T) {
	enc := crypto.New()
	ct, err := enc.Encrypt("secret", "correct-slug")
	require.NoError(t, err)

	_, err = enc.Decrypt(ct, "wrong-slug")
	assert.Error(t, err)
}

func TestEncryptor_TamperedCiphertextFails(t *testing.T) {
	enc := crypto.New()
	ct, err := enc.Encrypt("secret", "slug")
	require.NoError(t, err)

	// flip last byte of base64-encoded ciphertext
	tampered := []byte(ct)
	tampered[len(tampered)-1] ^= 0x01
	_, err = enc.Decrypt(string(tampered), "slug")
	assert.Error(t, err)
}

func TestEncryptor_InvalidBase64Fails(t *testing.T) {
	enc := crypto.New()
	_, err := enc.Decrypt("not-valid-base64!!!", "slug")
	assert.Error(t, err)
}

func TestEncryptor_TooShortCiphertextFails(t *testing.T) {
	enc := crypto.New()
	// valid base64 but too short to contain nonce + tag
	_, err := enc.Decrypt("aGVsbG8=", "slug") // base64("hello")
	assert.Error(t, err)
}
