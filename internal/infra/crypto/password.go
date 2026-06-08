package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	pwKeyLen   uint32 = 32
	pwKDFLen          = 16
	pwHKDFInfo        = "padmark-user-key-v1"
)

// PasswordHasher hashes and verifies user passwords using argon2id with configurable cost.
type PasswordHasher struct {
	params Argon2Params
}

// NewPasswordHasher returns a PasswordHasher using the given argon2 cost parameters.
// Zero-valued fields fall back to DefaultArgon2Params.
func NewPasswordHasher(params Argon2Params) *PasswordHasher {
	return &PasswordHasher{params: params.withDefaults()}
}

// Hash derives an argon2id hash from password (cost parameters embedded in the result).
func (h *PasswordHasher) Hash(password string) (string, error) {
	return hashArgon2(password, h.params)
}

// Verify reports whether password matches storedHash, reading params from the stored hash.
func (h *PasswordHasher) Verify(storedHash, password string) bool {
	return verifyArgon2(storedHash, password)
}

// HashPassword hashes a password with the default argon2 cost. Convenience helper for
// callers that don't tune cost (e.g. tests); production wiring injects a PasswordHasher.
func HashPassword(password string) (string, error) {
	return hashArgon2(password, DefaultArgon2Params())
}

// VerifyPassword reports whether password matches storedHash (reads embedded params).
func VerifyPassword(storedHash, password string) bool {
	return verifyArgon2(storedHash, password)
}

// GenerateKDFSalt returns 16 random bytes to use as HKDF salt for user key derivation.
func GenerateKDFSalt() ([]byte, error) {
	salt := make([]byte, pwKDFLen)

	_, err := rand.Read(salt)
	if err != nil {
		return nil, fmt.Errorf("read kdf salt: %w", err)
	}

	return salt, nil
}

// DeriveUserKey derives a 32-byte hex-encoded key from password and kdfSalt via HKDF-SHA256.
// Use this as the AES encryption key for the user's TOTP secret.
func DeriveUserKey(password, kdfSalt []byte) (string, error) {
	reader := hkdf.New(sha256.New, password, kdfSalt, []byte(pwHKDFInfo))

	key := make([]byte, pwKeyLen)

	_, err := io.ReadFull(reader, key)
	if err != nil {
		return "", fmt.Errorf("hkdf: %w", err)
	}

	return hex.EncodeToString(key), nil
}
