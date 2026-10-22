package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

const (
	pwKeyLen uint32 = 32
	pwKDFLen int    = 16
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

// GenerateKDFSalt returns 16 random bytes to use as the argon2 salt for user key derivation.
func GenerateKDFSalt() ([]byte, error) {
	salt := make([]byte, pwKDFLen)

	_, err := rand.Read(salt)
	if err != nil {
		return nil, fmt.Errorf("read kdf salt: %w", err)
	}

	return salt, nil
}

// DeriveUserKey derives a 32-byte base64url-encoded key from password and kdfSalt via
// argon2id at the given cost. The result is the AES-GCM key used to wrap a user's TOTP
// secret. Cost params must match the PasswordHasher config: a stolen DB then forces the
// attacker through the same memory-hard work as the password column, instead of a
// single-pass SHA-256 (HKDF) that would yield SHA-256 brute-force speeds.
//
// Use GenerateKDFSalt to produce a fresh salt; do not reuse salts across users.
func DeriveUserKey(password, kdfSalt []byte, params Argon2Params) (string, error) {
	params = params.withDefaults()

	err := params.Validate()
	if err != nil {
		return "", err
	}

	key, err := deriveArgon2Key(password, kdfSalt, params, pwKeyLen)
	if err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(key), nil
}
