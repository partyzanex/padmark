package crypto

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	// fileStreamMagic starts each encrypted file. The digit is the format version.
	fileStreamMagic = "pmf1"
	// fileChunkSize is the plaintext size of each chunk except the last. A small chunk keeps
	// memory use low; a large chunk keeps the tag overhead low (16 bytes per 64 KiB).
	fileChunkSize = 64 << 10
	fileKeySize   = 32
	fileTagSize   = 16
	fileNonceSize = 12
	// fileNonceLastIdx is the nonce byte that marks the last chunk. The flag stops truncation at
	// a chunk boundary; the counter stops reordering and repetition of chunks.
	fileNonceLastIdx = fileNonceSize - 1
	fileAAD          = "padmark-file-v1"
)

var (
	errFileKeySize       = errors.New("file key must be 32 bytes")
	errFileStreamTooLong = errors.New("file stream has too many chunks")
)

// FileCipher encrypts file content as a sequence of AES-256-GCM chunks (the STREAM
// construction). Each file has its own random key, so a nonce is the chunk counter and a
// last-chunk flag; a nonce never repeats under one key. Decryption detects modified,
// reordered, truncated and extended streams.
type FileCipher struct {
	random io.Reader
}

// NewFileCipher returns a FileCipher that takes keys from crypto/rand.
func NewFileCipher() *FileCipher {
	return &FileCipher{random: rand.Reader}
}

// NewKey returns a new random file key.
func (c *FileCipher) NewKey() ([]byte, error) {
	key := make([]byte, fileKeySize)

	_, err := io.ReadFull(c.random, key)
	if err != nil {
		return nil, fmt.Errorf("read random file key: %w", err)
	}

	return key, nil
}

// Encrypt writes the encrypted form of src to dst. Only io.EOF from src ends the stream. Each
// other error from src stops the stream and is returned in the chain, so that a caller can see
// its own size-limit error or a cut request body.
func (*FileCipher) Encrypt(key []byte, dst io.Writer, src io.Reader) error {
	if len(key) != fileKeySize {
		return errFileKeySize
	}

	gcm, err := contentGCM(key)
	if err != nil {
		return err
	}

	reader := &encryptingReader{
		src:     src,
		gcm:     gcm,
		buf:     make([]byte, 0, fileChunkSize+1),
		out:     append(make([]byte, 0, fileChunkSize+fileTagSize), fileStreamMagic...),
		pos:     0,
		nonce:   make([]byte, fileNonceSize),
		aad:     []byte(fileAAD),
		counter: 0,
		done:    false,
	}

	_, err = io.Copy(dst, reader)
	if err != nil {
		return fmt.Errorf("encrypt file stream: %w", err)
	}

	return nil
}

// Decrypt writes the plaintext of src to dst. It returns an error that wraps
// domain.ErrDecryptionFailed or domain.ErrMalformedCiphertext when the stream is not authentic.
// The plaintext before the bad chunk is already written, so a caller must treat an error as a
// failed download.
func (*FileCipher) Decrypt(key []byte, dst io.Writer, src io.Reader) error {
	if len(key) != fileKeySize {
		return errFileKeySize
	}

	gcm, err := contentGCM(key)
	if err != nil {
		return err
	}

	reader := &decryptingReader{
		src:     src,
		gcm:     gcm,
		buf:     make([]byte, 0, fileChunkSize+fileTagSize+1),
		out:     make([]byte, 0, fileChunkSize),
		pos:     0,
		nonce:   make([]byte, fileNonceSize),
		aad:     []byte(fileAAD),
		counter: 0,
		header:  false,
		done:    false,
	}

	_, err = io.Copy(dst, reader)
	if err != nil {
		return fmt.Errorf("decrypt file stream: %w", err)
	}

	return nil
}

// fileNonce writes the nonce of chunk counter into nonce and returns it. lastFlag is 1 for the
// last chunk, else 0. The caller gives the same buffer for each chunk, so that a chunk does not
// allocate.
func fileNonce(nonce []byte, counter uint64, lastFlag byte) []byte {
	binary.BigEndian.PutUint64(nonce, counter)
	nonce[fileNonceLastIdx] = lastFlag

	return nonce
}

// streamChunk is the result of fillChunk.
type streamChunk struct {
	// data is the chunk with the one extra byte that starts the next chunk, if it exists.
	data []byte
	// last is true only when the source returned io.EOF itself before data was full.
	last bool
}

// nonceFlag returns the nonce flag byte of the chunk: 1 for the last chunk, else 0.
func (c streamChunk) nonceFlag() byte {
	if c.last {
		return 1
	}

	return 0
}

// fillChunk reads into buf until it holds limit bytes or src ends. Only io.EOF from src ends
// the stream. Each other src error is a broken stream, also io.ErrUnexpectedEOF from a
// multipart part without its closing boundary. fillChunk returns it wrapped, so that a cut
// stream is never sealed as a complete file. io.ReadFull cannot do this: it returns the same
// io.ErrUnexpectedEOF for a short last read and for a broken source.
func fillChunk(src io.Reader, buf []byte, limit int) (streamChunk, error) {
	chunk := buf[:limit]
	filled := len(buf)

	for filled < limit {
		n, err := src.Read(chunk[filled:])
		filled += n

		// Identity, not errors.Is: a wrapped io.EOF is an error of a wrapper, not the end.
		if err == io.EOF {
			// A full buffer is not the last chunk yet: the extra byte starts the next chunk,
			// and the next Read returns io.EOF again.
			return streamChunk{data: chunk[:filled], last: filled < limit}, nil
		}

		if err != nil {
			return streamChunk{data: chunk[:filled], last: false}, fmt.Errorf("read file stream: %w", err)
		}
	}

	return streamChunk{data: chunk, last: false}, nil
}

// encryptingReader seals one chunk at a time into out. out keeps its capacity for all chunks,
// and pos is the start of the bytes that Read did not return yet. Thus a stream of any size
// uses two buffers of one chunk each.
type encryptingReader struct {
	src     io.Reader
	gcm     cipher.AEAD
	buf     []byte
	out     []byte
	nonce   []byte
	aad     []byte
	pos     int
	counter uint64
	done    bool
}

func (r *encryptingReader) Read(dst []byte) (int, error) {
	for r.pos == len(r.out) {
		if r.done {
			return 0, io.EOF
		}

		err := r.sealNext()
		if err != nil {
			return 0, err
		}
	}

	n := copy(dst, r.out[r.pos:])
	r.pos += n

	return n, nil
}

// sealNext reads one byte more than a chunk. That byte tells if the current chunk is the last;
// it stays in buf as the start of the next chunk.
func (r *encryptingReader) sealNext() error {
	chunk, err := fillChunk(r.src, r.buf, fileChunkSize+1)
	if err != nil {
		return err
	}

	buf := chunk.data
	size := min(len(buf), fileChunkSize)

	if r.counter == ^uint64(0) {
		return errFileStreamTooLong
	}

	r.out = r.gcm.Seal(r.out[:0], fileNonce(r.nonce, r.counter, chunk.nonceFlag()), buf[:size], r.aad)
	r.pos = 0
	r.counter++

	rest := copy(buf, buf[size:])
	r.buf = buf[:rest]
	r.done = chunk.last

	return nil
}

// decryptingReader opens one chunk at a time into out. As in encryptingReader, out keeps its
// capacity and pos is the read offset.
type decryptingReader struct {
	src     io.Reader
	gcm     cipher.AEAD
	buf     []byte
	out     []byte
	nonce   []byte
	aad     []byte
	pos     int
	counter uint64
	header  bool
	done    bool
}

func (r *decryptingReader) Read(dst []byte) (int, error) {
	if !r.header {
		err := r.readHeader()
		if err != nil {
			return 0, err
		}
	}

	for r.pos == len(r.out) {
		if r.done {
			return 0, io.EOF
		}

		err := r.openNext()
		if err != nil {
			return 0, err
		}
	}

	n := copy(dst, r.out[r.pos:])
	r.pos += n

	return n, nil
}

func (r *decryptingReader) readHeader() error {
	magic := make([]byte, len(fileStreamMagic))

	_, err := io.ReadFull(r.src, magic)
	if err != nil || string(magic) != fileStreamMagic {
		return fmt.Errorf("file stream header: %w", domain.ErrMalformedCiphertext)
	}

	r.header = true

	return nil
}

func (r *decryptingReader) openNext() error {
	chunk, err := fillChunk(r.src, r.buf, fileChunkSize+fileTagSize+1)
	if err != nil {
		return err
	}

	buf := chunk.data
	size := min(len(buf), fileChunkSize+fileTagSize)

	if size < fileTagSize {
		return fmt.Errorf("file stream chunk: %w", domain.ErrMalformedCiphertext)
	}

	plain, openErr := r.gcm.Open(r.out[:0], fileNonce(r.nonce, r.counter, chunk.nonceFlag()), buf[:size], r.aad)
	if openErr != nil {
		return fmt.Errorf("file stream chunk %d: %w", r.counter, domain.ErrDecryptionFailed)
	}

	r.out = plain
	r.pos = 0
	r.counter++

	rest := copy(buf, buf[size:])
	r.buf = buf[:rest]
	r.done = chunk.last

	return nil
}
