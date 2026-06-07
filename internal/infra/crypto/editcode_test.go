package crypto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

func TestEditCodeHasher_Roundtrip(t *testing.T) {
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

	hash, err := hasher.Hash("MySecretCode12")
	require.NoError(t, err)
	assert.True(t, hasher.Verify(hash, "MySecretCode12"))
}

func TestEditCodeHasher_WrongCodeFails(t *testing.T) {
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

	hash, err := hasher.Hash("correct-code")
	require.NoError(t, err)
	assert.False(t, hasher.Verify(hash, "wrong-code"))
}

func TestEditCodeHasher_DifferentSaltsPerHash(t *testing.T) {
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

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
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

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
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

	hash, err := hasher.Hash("code")
	require.NoError(t, err)

	tampered := []byte(hash)
	// XOR 0x08 flips a data bit, not a base64url padding bit (the last 2 bits of a 43-char
	// base64url-encoded 32-byte value are padding and ignored by the decoder).
	tampered[len(tampered)-1] ^= 0x08
	assert.False(t, hasher.Verify(string(tampered), "code"))
}
