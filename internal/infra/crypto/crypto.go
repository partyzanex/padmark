package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const aesKeySize = 32

const hkdfInfo = "padmark-content-v1"

// Encryptor encrypts and decrypts note content using AES-256-GCM.
// The key is derived from the note slug via HKDF-SHA256.
type Encryptor struct{}

// New returns a new Encryptor.
func New() *Encryptor { return &Encryptor{} }

// Encrypt encrypts plaintext with a key derived from slug. Returns base64-encoded ciphertext.
func (e *Encryptor) Encrypt(plaintext, slug string) (string, error) {
	gcm, err := newGCM(slug)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())

	_, err = io.ReadFull(rand.Reader, nonce)
	if err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}

	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	return base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt decrypts base64-encoded ciphertext with a key derived from slug.
func (e *Encryptor) Decrypt(ciphertext, slug string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	gcm, err := newGCM(slug)
	if err != nil {
		return "", err
	}

	if len(data) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}

	nonce, ct := data[:gcm.NonceSize()], data[gcm.NonceSize():]

	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("gcm open: %w", err)
	}

	return string(plaintext), nil
}

//nolint:ireturn // stdlib cipher.AEAD; callers hold it only as cipher.AEAD
func newGCM(slug string) (cipher.AEAD, error) {
	key, err := deriveKey(slug)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}

	return gcm, nil
}

// HashSlug returns the SHA-256 hex digest of slug used as the database primary key.
// The plaintext slug (which is also the AES key material) is never stored at rest;
// only this hash is persisted so a DB exfil alone cannot derive the decryption key.
func HashSlug(slug string) string {
	sum := sha256.Sum256([]byte(slug))

	return hex.EncodeToString(sum[:])
}

func deriveKey(slug string) ([]byte, error) {
	reader := hkdf.New(sha256.New, []byte(slug), nil, []byte(hkdfInfo))

	key := make([]byte, aesKeySize)

	_, err := io.ReadFull(reader, key)
	if err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}

	return key, nil
}
