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
	password := []byte("correct horse battery staple")
	salt := []byte("saltsaltsalt1234")

	key1, err := crypto.DeriveUserKey(password, salt)
	require.NoError(t, err)
	require.Len(t, key1, 64, "32-byte key, hex-encoded")

	key2, err := crypto.DeriveUserKey(password, salt)
	require.NoError(t, err)
	require.Equal(t, key1, key2, "same password+salt → same key")

	keyOtherSalt, err := crypto.DeriveUserKey(password, []byte("differentsalt456"))
	require.NoError(t, err)
	require.NotEqual(t, key1, keyOtherSalt, "different salt → different key")

	keyOtherPw, err := crypto.DeriveUserKey([]byte("another password!!"), salt)
	require.NoError(t, err)
	require.NotEqual(t, key1, keyOtherPw, "different password → different key")
}
