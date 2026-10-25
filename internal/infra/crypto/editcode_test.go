package crypto_test

import (
	"strings"
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

// TestEditCodeHasher_UsesFastHashFormat locks in that new edit codes use the fast salted-SHA-256
// scheme ("s1$…"), not memory-hard argon2 — the change that removed argon2's per-write memory cost.
func TestEditCodeHasher_UsesFastHashFormat(t *testing.T) {
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

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

	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

	assert.True(t, hasher.Verify(legacy, "legacy-code-123"), "legacy argon2 edit code must still verify")
	assert.False(t, hasher.Verify(legacy, "wrong-code"))
}

// TestEditCodeHasher_UserProvidedRoundtrip pins that caller-supplied codes take the
// memory-hard argon2id path and remain verifiable through the same Verify dispatcher.
func TestEditCodeHasher_UserProvidedRoundtrip(t *testing.T) {
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

	hash, err := hasher.HashUserProvided("MySecretCode12")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "v1$"), "user-supplied codes must use the argon2id format, got %q", hash)
	assert.True(t, hasher.Verify(hash, "MySecretCode12"))
	assert.False(t, hasher.Verify(hash, "wrong-code"))
}

// TestEditCodeHasher_UserProvidedSaltsDiffer ensures two hashes of the same caller-supplied
// code differ (random salt), so identical weak codes are not visible in a DB dump.
func TestEditCodeHasher_UserProvidedSaltsDiffer(t *testing.T) {
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

	hash1, err := hasher.HashUserProvided("password123")
	require.NoError(t, err)

	hash2, err := hasher.HashUserProvided("password123")
	require.NoError(t, err)

	assert.NotEqual(t, hash1, hash2)
	assert.True(t, hasher.Verify(hash1, "password123"))
	assert.True(t, hasher.Verify(hash2, "password123"))
}

// TestEditCodeHasher_WrongCodeFails verifies the negative branch of the fast path.
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

// TestEditCodeHasher_HashUserProvidedUsesConfiguredParams pins that caller-chosen edit codes are
// hashed with the operator's --argon2-* cost, not a hard-coded 64 MiB default.
func TestEditCodeHasher_HashUserProvidedUsesConfiguredParams(t *testing.T) {
	custom := crypto.Argon2Params{Memory: 8 * 1024, Time: 3, Threads: 2}
	hasher := crypto.NewEditCodeHasher(custom)

	hash, err := hasher.HashUserProvided("password123")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "v1$8192$3$2$"), "embedded params must match config, got %q", hash)
	assert.True(t, hasher.Verify(hash, "password123"))
	assert.False(t, hasher.Verify(hash, "password124"))
}

// TestEditCodeHasher_VerifiesCodesHashedWithOtherParams checks that changing the configured cost
// does not invalidate stored codes: Verify reads the cost from the stored hash.
func TestEditCodeHasher_VerifiesCodesHashedWithOtherParams(t *testing.T) {
	old := crypto.NewEditCodeHasher(crypto.Argon2Params{Memory: 8 * 1024, Time: 2, Threads: 1})
	current := crypto.NewEditCodeHasher(crypto.Argon2Params{Memory: 16 * 1024, Time: 3, Threads: 1})

	hash, err := old.HashUserProvided("chosen-by-user")
	require.NoError(t, err)

	assert.True(t, current.Verify(hash, "chosen-by-user"))
}

// TestEditCodeHasher_IsStoredHash pins the exact stored-hash shape: real hashes of both formats
// match, while v0.1.6 caller-chosen codes that only start like a hash do not (review R1).
func TestEditCodeHasher_IsStoredHash(t *testing.T) {
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())

	fast, err := hasher.Hash("code12345678")
	require.NoError(t, err)
	argon, err := hasher.HashUserProvided("caller-chosen-code")
	require.NoError(t, err)

	assert.True(t, hasher.IsStoredHash(fast), "s1$ hash %q", fast)
	assert.True(t, hasher.IsStoredHash(argon), "v1$ hash %q", argon)

	salt, digest := strings.Repeat("A", 22), strings.Repeat("B", 43)
	for _, value := range []string{
		"", "hunter2", "v1$old-user-code", "s1$old-user-code",
		"s1$" + salt + "$" + digest[1:], "s1$" + salt[1:] + "$" + digest + "B",
		"s1$" + salt + "$" + digest + "=", "x1$" + salt + "$" + digest,
		"v1$1$1$$" + salt + "$" + digest, "v1$a$1$1$" + salt + "$" + digest, "v1$1$1$1$1$" + salt + "$" + digest,
	} {
		assert.False(t, hasher.IsStoredHash(value), "%q must not count as a stored hash", value)
	}
}
