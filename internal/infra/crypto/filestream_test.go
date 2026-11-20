package crypto

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
)

func encryptAll(t *testing.T, fileCipher *FileCipher, key, plain []byte) []byte {
	t.Helper()

	var out bytes.Buffer

	require.NoError(t, fileCipher.Encrypt(key, &out, bytes.NewReader(plain)))

	return out.Bytes()
}

func decryptAll(fileCipher *FileCipher, key, ciphertext []byte) ([]byte, error) {
	var out bytes.Buffer

	err := fileCipher.Decrypt(key, &out, bytes.NewReader(ciphertext))

	return out.Bytes(), err
}

func TestFileCipher_RoundTrip(t *testing.T) {
	t.Parallel()

	fileCipher := NewFileCipher()

	sizes := []int{0, 1, fileChunkSize - 1, fileChunkSize, fileChunkSize + 1, 3*fileChunkSize + 5}
	for _, size := range sizes {
		key, err := fileCipher.NewKey()
		require.NoError(t, err)

		plain := make([]byte, size)
		_, err = rand.Read(plain)
		require.NoError(t, err)

		ciphertext := encryptAll(t, fileCipher, key, plain)
		chunks := max(1, (size+fileChunkSize-1)/fileChunkSize)
		assert.Len(t, ciphertext, len(fileStreamMagic)+size+chunks*fileTagSize, "size %d", size)

		got, err := decryptAll(fileCipher, key, ciphertext)
		require.NoError(t, err, "size %d", size)
		assert.Equal(t, plain, got, "size %d", size)
	}
}

func TestFileCipher_RejectsChangedStreams(t *testing.T) {
	t.Parallel()

	fileCipher := NewFileCipher()
	key, err := fileCipher.NewKey()
	require.NoError(t, err)

	plain := bytes.Repeat([]byte("x"), 2*fileChunkSize+10)
	ciphertext := encryptAll(t, fileCipher, key, plain)
	firstChunkEnd := len(fileStreamMagic) + fileChunkSize + fileTagSize

	t.Run("modified byte", func(t *testing.T) {
		changed := bytes.Clone(ciphertext)
		changed[len(changed)-1] ^= 1

		_, err := decryptAll(fileCipher, key, changed)
		require.ErrorIs(t, err, domain.ErrDecryptionFailed)
	})

	t.Run("truncated at a chunk boundary", func(t *testing.T) {
		_, err := decryptAll(fileCipher, key, ciphertext[:firstChunkEnd])
		require.ErrorIs(t, err, domain.ErrDecryptionFailed)
	})

	t.Run("extended after the last chunk", func(t *testing.T) {
		_, err := decryptAll(fileCipher, key, append(bytes.Clone(ciphertext), 0))
		require.ErrorIs(t, err, domain.ErrDecryptionFailed)
	})

	t.Run("chunk too short for a tag", func(t *testing.T) {
		_, err := decryptAll(fileCipher, key, append([]byte(fileStreamMagic), 1, 2, 3))
		require.ErrorIs(t, err, domain.ErrMalformedCiphertext)
	})

	t.Run("bad header", func(t *testing.T) {
		changed := bytes.Clone(ciphertext)
		changed[0] = 'x'

		_, err := decryptAll(fileCipher, key, changed)
		require.ErrorIs(t, err, domain.ErrMalformedCiphertext)
	})

	t.Run("wrong key", func(t *testing.T) {
		other, err := fileCipher.NewKey()
		require.NoError(t, err)

		_, err = decryptAll(fileCipher, other, ciphertext)
		require.ErrorIs(t, err, domain.ErrDecryptionFailed)
	})
}

func TestFileCipher_RejectsBadKey(t *testing.T) {
	t.Parallel()

	fileCipher := NewFileCipher()

	require.ErrorIs(t, fileCipher.Encrypt([]byte("short"), io.Discard, bytes.NewReader(nil)), errFileKeySize)
	require.ErrorIs(t, fileCipher.Decrypt([]byte("short"), io.Discard, bytes.NewReader(nil)), errFileKeySize)
}
