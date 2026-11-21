package crypto

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"testing"
	"testing/iotest"

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

func TestFileCipher_EncryptStopsOnBrokenSource(t *testing.T) {
	t.Parallel()

	fileCipher := NewFileCipher()
	key, err := fileCipher.NewKey()
	require.NoError(t, err)

	errReset := errors.New("connection reset")
	data := bytes.Repeat([]byte("x"), 5000)

	cases := map[string]struct {
		src  io.Reader
		want error
	}{
		"unexpected EOF after data": {
			src:  io.MultiReader(bytes.NewReader(data), iotest.ErrReader(io.ErrUnexpectedEOF)),
			want: io.ErrUnexpectedEOF,
		},
		"unexpected EOF after a full chunk": {
			src: io.MultiReader(
				bytes.NewReader(bytes.Repeat([]byte("y"), 2*fileChunkSize)),
				iotest.ErrReader(io.ErrUnexpectedEOF),
			),
			want: io.ErrUnexpectedEOF,
		},
		"custom error mid-stream": {
			src:  io.MultiReader(bytes.NewReader(data), iotest.ErrReader(errReset)),
			want: errReset,
		},
		"wrapped EOF": {
			src:  io.MultiReader(bytes.NewReader(data), iotest.ErrReader(fmt.Errorf("proxy: %w", io.EOF))),
			want: io.EOF,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			err := fileCipher.Encrypt(key, &out, testCase.src)
			require.ErrorIs(t, err, testCase.want)
			require.ErrorContains(t, err, "read file stream")
		})
	}
}

func TestFileCipher_EncryptStopsOnMultipartWithoutClosingBoundary(t *testing.T) {
	t.Parallel()

	var body bytes.Buffer

	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "a.bin")
	require.NoError(t, err)

	_, err = part.Write(bytes.Repeat([]byte("z"), 5000))
	require.NoError(t, err)
	// No writer.Close(): the body has no closing boundary, as a cut upload.

	reader := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary())
	filePart, err := reader.NextPart()
	require.NoError(t, err)

	fileCipher := NewFileCipher()
	key, err := fileCipher.NewKey()
	require.NoError(t, err)

	err = fileCipher.Encrypt(key, io.Discard, filePart)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestFileCipher_EncryptAcceptsDataWithEOF(t *testing.T) {
	t.Parallel()

	fileCipher := NewFileCipher()

	sizes := []int{0, 1, fileChunkSize, fileChunkSize + 1, 2 * fileChunkSize, 2*fileChunkSize + 3}
	readers := map[string]func([]byte) io.Reader{
		"data with EOF in one call": func(plain []byte) io.Reader { return &dataEOFReader{data: plain} },
		"data error reader":         func(plain []byte) io.Reader { return iotest.DataErrReader(bytes.NewReader(plain)) },
		"one byte reader":           func(plain []byte) io.Reader { return iotest.OneByteReader(bytes.NewReader(plain)) },
	}

	for name, newReader := range readers {
		for _, size := range sizes {
			key, err := fileCipher.NewKey()
			require.NoError(t, err)

			plain := make([]byte, size)
			_, err = rand.Read(plain)
			require.NoError(t, err)

			var out bytes.Buffer

			require.NoError(t, fileCipher.Encrypt(key, &out, newReader(plain)), "%s, size %d", name, size)

			chunks := max(1, (size+fileChunkSize-1)/fileChunkSize)
			assert.Len(t, out.Bytes(), len(fileStreamMagic)+size+chunks*fileTagSize, "%s, size %d", name, size)

			got, err := decryptAll(fileCipher, key, out.Bytes())
			require.NoError(t, err, "%s, size %d", name, size)
			assert.Equal(t, plain, got, "%s, size %d", name, size)
		}
	}
}

func TestFileCipher_DecryptStopsOnBrokenSource(t *testing.T) {
	t.Parallel()

	fileCipher := NewFileCipher()
	key, err := fileCipher.NewKey()
	require.NoError(t, err)

	ciphertext := encryptAll(t, fileCipher, key, bytes.Repeat([]byte("x"), fileChunkSize+10))
	src := io.MultiReader(bytes.NewReader(ciphertext[:len(ciphertext)/2]), iotest.ErrReader(io.ErrUnexpectedEOF))

	err = fileCipher.Decrypt(key, io.Discard, src)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

// --- helpers ---

// dataEOFReader returns io.EOF together with the last bytes, as the io.Reader contract permits.
type dataEOFReader struct {
	data []byte
}

func (r *dataEOFReader) Read(dst []byte) (int, error) {
	n := copy(dst, r.data)
	r.data = r.data[n:]

	if len(r.data) == 0 {
		return n, io.EOF
	}

	return n, nil
}
