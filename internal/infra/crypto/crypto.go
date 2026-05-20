package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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

func newGCM(slug string) (cipher.AEAD, error) { //nolint:ireturn
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

func deriveKey(slug string) ([]byte, error) {
	reader := hkdf.New(sha256.New, []byte(slug), nil, []byte(hkdfInfo))

	key := make([]byte, aesKeySize)

	_, err := io.ReadFull(reader, key)
	if err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}

	return key, nil
}
