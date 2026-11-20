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

// Encrypt writes the encrypted form of src to dst. An error from src stops the stream and is
// returned in the chain, so that a caller can see its own size-limit error.
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
		out:     []byte(fileStreamMagic),
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
		out:     nil,
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

// fileNonce returns the nonce of chunk counter. lastFlag is 1 for the last chunk, else 0.
func fileNonce(counter uint64, lastFlag byte) []byte {
	nonce := make([]byte, fileNonceSize)
	binary.BigEndian.PutUint64(nonce, counter)
	nonce[fileNonceLastIdx] = lastFlag

	return nonce
}

// lastChunkFlag returns the nonce flag byte for a chunk read with readErr: 1 when the stream
// ended (io.EOF from fillChunk), else 0.
func lastChunkFlag(readErr error) byte {
	if errors.Is(readErr, io.EOF) {
		return 1
	}

	return 0
}

// fillChunk reads into buf until it holds limit bytes or src ends. It returns io.EOF only
// when src ended before buf was full.
func fillChunk(src io.Reader, buf []byte, limit int) ([]byte, error) {
	start := len(buf)
	chunk := buf[:limit]

	n, err := io.ReadFull(src, chunk[start:])
	chunk = chunk[:start+n]

	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return chunk, io.EOF
	}

	if err != nil {
		return chunk, fmt.Errorf("read file stream: %w", err)
	}

	return chunk, nil
}

type encryptingReader struct {
	src     io.Reader
	gcm     cipher.AEAD
	buf     []byte
	out     []byte
	counter uint64
	done    bool
}

func (r *encryptingReader) Read(dst []byte) (int, error) {
	for len(r.out) == 0 {
		if r.done {
			return 0, io.EOF
		}

		err := r.sealNext()
		if err != nil {
			return 0, err
		}
	}

	n := copy(dst, r.out)
	r.out = r.out[n:]

	return n, nil
}

// sealNext reads one byte more than a chunk. That byte tells if the current chunk is the last;
// it stays in buf as the start of the next chunk.
func (r *encryptingReader) sealNext() error {
	buf, err := fillChunk(r.src, r.buf, fileChunkSize+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	last := errors.Is(err, io.EOF)
	size := min(len(buf), fileChunkSize)

	if r.counter == ^uint64(0) {
		return errFileStreamTooLong
	}

	r.out = r.gcm.Seal(r.out[:0], fileNonce(r.counter, lastChunkFlag(err)), buf[:size], []byte(fileAAD))
	r.counter++

	rest := copy(buf, buf[size:])
	r.buf = buf[:rest]
	r.done = last

	return nil
}

type decryptingReader struct {
	src     io.Reader
	gcm     cipher.AEAD
	buf     []byte
	out     []byte
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

	for len(r.out) == 0 {
		if r.done {
			return 0, io.EOF
		}

		err := r.openNext()
		if err != nil {
			return 0, err
		}
	}

	n := copy(dst, r.out)
	r.out = r.out[n:]

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
	buf, err := fillChunk(r.src, r.buf, fileChunkSize+fileTagSize+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	last := errors.Is(err, io.EOF)
	size := min(len(buf), fileChunkSize+fileTagSize)

	if size < fileTagSize {
		return fmt.Errorf("file stream chunk: %w", domain.ErrMalformedCiphertext)
	}

	plain, openErr := r.gcm.Open(r.out[:0], fileNonce(r.counter, lastChunkFlag(err)), buf[:size], []byte(fileAAD))
	if openErr != nil {
		return fmt.Errorf("file stream chunk %d: %w", r.counter, domain.ErrDecryptionFailed)
	}

	r.out = plain
	r.counter++

	rest := copy(buf, buf[size:])
	r.buf = buf[:rest]
	r.done = last

	return nil
}
