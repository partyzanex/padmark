package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
)

const (
	pwMemory   uint32 = 64 * 1024
	pwTime     uint32 = 1
	pwThreads  uint8  = 1
	pwKeyLen   uint32 = 32
	pwSaltLen         = 16
	pwKDFLen          = 16
	pwHKDFInfo        = "padmark-user-key-v1"
)

// GenerateKDFSalt returns 16 random bytes to use as HKDF salt for user key derivation.
func GenerateKDFSalt() ([]byte, error) {
	salt := make([]byte, pwKDFLen)

	_, err := rand.Read(salt)
	if err != nil {
		return nil, fmt.Errorf("read kdf salt: %w", err)
	}

	return salt, nil
}

// HashPassword derives an argon2id hash from password.
// Format: "v1$<memory>$<time>$<threads>$<base64url_salt>$<base64url_key>"
// Parameters are embedded so future changes don't break existing hashes.
func HashPassword(password string) (string, error) {
	salt := make([]byte, pwSaltLen)

	_, err := rand.Read(salt)
	if err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, pwTime, pwMemory, pwThreads, pwKeyLen)
	enc := base64.RawURLEncoding

	return fmt.Sprintf("v1$%d$%d$%d$%s$%s",
		pwMemory, pwTime, uint32(pwThreads),
		enc.EncodeToString(salt),
		enc.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches storedHash.
// Reads parameters from the stored hash to support future param rotation.
func VerifyPassword(storedHash, password string) bool {
	return (&EditCodeHasher{}).Verify(storedHash, password)
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
