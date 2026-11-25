package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// referenceStream builds the pmf1 format without the stream readers: the magic, then each
// chunk sealed with the chunk counter and the last-chunk flag in the nonce. A change of the
// reader buffers must not change these bytes.
func referenceStream(t *testing.T, key, plain []byte) []byte {
	t.Helper()

	block, err := aes.NewCipher(key)
	require.NoError(t, err)

	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)

	out := []byte("pmf1")
	counter := uint64(0)

	for {
		size := min(len(plain), 64<<10)
		last := len(plain) <= 64<<10

		// The nonce is the 8-byte counter, three zero bytes and the last-chunk flag.
		flag := byte(0)
		if last {
			flag = 1
		}

		nonce := append(binary.BigEndian.AppendUint64(nil, counter), 0, 0, 0, flag)

		out = gcm.Seal(out, nonce, plain[:size], []byte("padmark-file-v1"))
		plain = plain[size:]
		counter++

		if last {
			return out
		}
	}
}

func TestFileCipher_FormatMatchesReference(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{7}, fileKeySize)

	for _, size := range []int{0, 1, fileChunkSize, fileChunkSize + 1, 3*fileChunkSize + 5} {
		plain := bytes.Repeat([]byte{'a'}, size)

		got := encryptAll(t, NewFileCipher(), key, plain)
		assert.Equal(t, referenceStream(t, key, plain), got, "size %d", size)

		opened, err := decryptAll(NewFileCipher(), key, got)
		require.NoError(t, err, "size %d", size)
		assert.Equal(t, plain, opened, "size %d", size)
	}
}

// TestFileCipher_ReusesOutputBuffer makes sure that the allocations do not grow with the number
// of chunks. Before the fix, each chunk allocated a new output buffer of 64 KiB.
func TestFileCipher_ReusesOutputBuffer(t *testing.T) {
	const chunks = 64

	key := bytes.Repeat([]byte{7}, fileKeySize)
	plain := make([]byte, chunks*fileChunkSize)
	fileCipher := NewFileCipher()

	var sealed bytes.Buffer

	require.NoError(t, fileCipher.Encrypt(key, &sealed, bytes.NewReader(plain)))

	encryptAllocs := testing.AllocsPerRun(3, func() {
		require.NoError(t, fileCipher.Encrypt(key, io.Discard, bytes.NewReader(plain)))
	})
	decryptAllocs := testing.AllocsPerRun(3, func() {
		require.NoError(t, fileCipher.Decrypt(key, io.Discard, bytes.NewReader(sealed.Bytes())))
	})

	assert.Less(t, encryptAllocs, float64(chunks/2), "encryption allocates for each chunk")
	assert.Less(t, decryptAllocs, float64(chunks/2), "decryption allocates for each chunk")
}
