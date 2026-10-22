package crypto_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

func TestArgon2Params_Default(t *testing.T) {
	def := crypto.DefaultArgon2Params()

	require.Equal(t, uint32(64*1024), def.Memory)
	require.Equal(t, uint32(2), def.Time)
	require.Equal(t, uint8(1), def.Threads)
}

func TestPasswordHasher_EmbedsConfiguredParams(t *testing.T) {
	hasher := crypto.NewPasswordHasher(crypto.Argon2Params{Memory: 8 * 1024, Time: 2, Threads: 1})

	hash, err := hasher.Hash("correct horse battery staple")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hash, "v1$8192$2$1$"), "configured params must be embedded: %s", hash)
	require.True(t, hasher.Verify(hash, "correct horse battery staple"))
	require.False(t, hasher.Verify(hash, "wrong"))
}

func TestPasswordHasher_ZeroParamsFallBackToDefaults(t *testing.T) {
	hasher := crypto.NewPasswordHasher(crypto.Argon2Params{}) // all-zero → defaults

	hash, err := hasher.Hash("xyz")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hash, "v1$65536$2$1$"), "zero params must default to 64 MiB / t=2: %s", hash)
}

// TestArgon2_VerifyAcrossParamChanges proves cost can be lowered without breaking existing
// hashes: a hash made with strong (default) cost still verifies via a weak-cost hasher,
// because Verify reads the params embedded in the stored hash.
func TestArgon2_VerifyAcrossParamChanges(t *testing.T) {
	strong := crypto.NewPasswordHasher(crypto.DefaultArgon2Params())

	hash, err := strong.Hash("secret123")
	require.NoError(t, err)

	weak := crypto.NewPasswordHasher(crypto.Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1})
	require.True(t, weak.Verify(hash, "secret123"))
}

func TestStoredArgon2CostsFailClosed(t *testing.T) {
	// Salt and digest are correctly shaped, so rejection must happen on cost bounds.
	salt := base64.RawURLEncoding.EncodeToString(make([]byte, 16))

	digest := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, cost := range []string{
		"4294967295$1$1", "262145$1$1", "8192$4294967295$1", "8192$11$1",
		"8192$1$255", "8192$1$17", "0$1$1", "8192$0$1", "8192$1$0", "7$1$1", "8$1$2",
	} {
		stored := "v1$" + cost + "$" + salt + "$" + digest
		require.False(t, crypto.VerifyPassword(stored, "password"), cost)
		require.False(t, crypto.NewEditCodeHasher().Verify(stored, "edit-code"), cost)
		_, _, err := crypto.NewSealer(sealTestParams).Open("v1$"+cost+"$AAAA", []byte("password"), sealTestSalt)
		require.Error(t, err, cost)
	}

	for _, encoded := range []string{strings.Repeat("A", 4000), salt} {
		require.False(t, crypto.VerifyPassword("v1$8192$1$1$"+salt+"$"+encoded, "password"))
	}
}

func TestInvalidConfiguredArgon2CostCannotWrite(t *testing.T) {
	for _, params := range []crypto.Argon2Params{
		{Memory: crypto.MaxArgon2Memory + 1, Time: 1, Threads: 1},
		{Memory: 8192, Time: crypto.MaxArgon2Time + 1, Threads: 1},
		{Memory: 8192, Time: 1, Threads: crypto.MaxArgon2Threads + 1},
	} {
		_, err := crypto.NewPasswordHasher(params).Hash("password")
		require.Error(t, err)
		_, err = crypto.NewSealer(params).Seal("secret", []byte("password"), sealTestSalt)
		require.Error(t, err)
	}
}
