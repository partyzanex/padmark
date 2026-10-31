package crypto_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

func TestGenerateKDFSalt_LengthAndRandomness(t *testing.T) {
	salt1, err := crypto.GenerateKDFSalt()
	require.NoError(t, err)
	require.Len(t, salt1, 16)

	salt2, err := crypto.GenerateKDFSalt()
	require.NoError(t, err)
	require.NotEqual(t, salt1, salt2, "two salts must (almost always) differ")
}

func TestDeriveUserKey_DeterministicAndSensitive(t *testing.T) {
	params := crypto.DefaultArgon2Params()
	password := []byte("correct horse battery staple")
	salt := []byte("saltsaltsalt1234")

	key1, err := crypto.DeriveUserKey(password, salt, params)
	require.NoError(t, err)
	require.Len(t, key1, 43, "32-byte key, base64url-encoded")

	key2, err := crypto.DeriveUserKey(password, salt, params)
	require.NoError(t, err)
	require.Equal(t, key1, key2, "same password+salt → same key")

	keyOtherSalt, err := crypto.DeriveUserKey(password, []byte("differentsalt456"), params)
	require.NoError(t, err)
	require.NotEqual(t, key1, keyOtherSalt, "different salt → different key")

	keyOtherPw, err := crypto.DeriveUserKey([]byte("another password!!"), salt, params)
	require.NoError(t, err)
	require.NotEqual(t, key1, keyOtherPw, "different password → different key")
}
