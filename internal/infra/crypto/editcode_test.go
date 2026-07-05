package crypto_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

func TestEditCodeHasher_Roundtrip(t *testing.T) {
	hasher := crypto.NewEditCodeHasher()

	hash, err := hasher.Hash("MySecretCode12")
	require.NoError(t, err)
	assert.True(t, hasher.Verify(hash, "MySecretCode12"))
}

// TestEditCodeHasher_UsesFastHashFormat locks in that new edit codes use the fast salted-SHA-256
// scheme ("s1$…"), not memory-hard argon2 — the change that removed argon2's per-write memory cost.
func TestEditCodeHasher_UsesFastHashFormat(t *testing.T) {
	hasher := crypto.NewEditCodeHasher()

	hash, err := hasher.Hash("code12345678")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "s1$"), "got %q", hash)
}

// TestEditCodeHasher_VerifiesLegacyArgon2Hash ensures edit codes hashed with the old argon2id
// scheme (same "v1$…" format HashPassword produces) still verify after the switch to a fast hash.
func TestEditCodeHasher_VerifiesLegacyArgon2Hash(t *testing.T) {
	legacy, err := crypto.HashPassword("legacy-code-123")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(legacy, "v1$"))

	hasher := crypto.NewEditCodeHasher()

	assert.True(t, hasher.Verify(legacy, "legacy-code-123"), "legacy argon2 edit code must still verify")
	assert.False(t, hasher.Verify(legacy, "wrong-code"))
}

func TestEditCodeHasher_WrongCodeFails(t *testing.T) {
	hasher := crypto.NewEditCodeHasher()

	hash, err := hasher.Hash("correct-code")
	require.NoError(t, err)
	assert.False(t, hasher.Verify(hash, "wrong-code"))
}

func TestEditCodeHasher_DifferentSaltsPerHash(t *testing.T) {
	hasher := crypto.NewEditCodeHasher()

	hash1, err := hasher.Hash("same-code")
	require.NoError(t, err)

	hash2, err := hasher.Hash("same-code")
	require.NoError(t, err)

	// different salts → different stored hashes, but both verify
	assert.NotEqual(t, hash1, hash2)
	assert.True(t, hasher.Verify(hash1, "same-code"))
	assert.True(t, hasher.Verify(hash2, "same-code"))
}

func TestEditCodeHasher_InvalidFormatFails(t *testing.T) {
	hasher := crypto.NewEditCodeHasher()

	for _, bad := range []string{
		"",
		"nodollar",
		"v1$a$b$c",       // too few parts
		"v1$a$b$c$d$e$f", // too many parts
		"v2$a$b$c$d$e",   // wrong version
		"!!invalid!!",
	} {
		assert.False(t, hasher.Verify(bad, "code"), "expected false for %q", bad)
	}
}

func TestEditCodeHasher_TamperedHashFails(t *testing.T) {
	hasher := crypto.NewEditCodeHasher()

	hash, err := hasher.Hash("code")
	require.NoError(t, err)

	tampered := []byte(hash)
	// XOR 0x08 flips a data bit, not a base64url padding bit (the last 2 bits of a 43-char
	// base64url-encoded 32-byte value are padding and ignored by the decoder).
	tampered[len(tampered)-1] ^= 0x08
	assert.False(t, hasher.Verify(string(tampered), "code"))
}
