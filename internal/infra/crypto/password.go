package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
)

const (
	pwKeyLen uint32 = 32
	pwKDFLen int    = 16
)

// PasswordHasher hashes and verifies user passwords using argon2id with configurable cost.
type PasswordHasher struct {
	log *slog.Logger
	// params is the cost Hash writes; ceiling (derived from it) is the most a stored hash may
	// demand before Verify refuses to run the derivation.
	params  Argon2Params
	ceiling Argon2Params
}

// NewPasswordHasher returns a PasswordHasher using the given argon2 cost parameters.
// Zero-valued fields fall back to DefaultArgon2Params.
func NewPasswordHasher(params Argon2Params) *PasswordHasher {
	return &PasswordHasher{
		log:     slog.New(slog.DiscardHandler),
		params:  params.withDefaults(),
		ceiling: params.acceptedCeiling(),
	}
}

// WithLogger sets the logger that records stored hashes refused for an excessive cost (WARN).
// The default discards them.
func (h *PasswordHasher) WithLogger(log *slog.Logger) *PasswordHasher {
	h.log = log

	return h
}

// Hash derives an argon2id hash from password (cost parameters embedded in the result).
func (h *PasswordHasher) Hash(password string) (string, error) {
	operationResult, operationErr := hashArgon2(password, h.params)
	if operationErr != nil {
		return operationResult, fmt.Errorf("hash argon2: %w", operationErr)
	}

	return operationResult, nil
}

// Verify reports whether password matches storedHash, reading params from the stored hash.
// Fail-closed: a hash whose cost exceeds the accepted ceiling never verifies and is logged.
func (h *PasswordHasher) Verify(storedHash, password string) bool {
	return verifyLogged(h.log, "password", storedHash, password, h.ceiling)
}

// verifyLogged runs verifyArgon2 and logs a refused cost at WARN, naming what was being checked
// but never the stored hash itself.
func verifyLogged(log *slog.Logger, what, storedHash, secret string, ceiling Argon2Params) bool {
	matched, err := verifyArgon2(storedHash, secret, ceiling)
	if errors.Is(err, ErrArgon2CostTooHigh) {
		log.Warn("refusing stored argon2 hash above the accepted cost ceiling",
			"kind", what, "ceiling_memory_kib", ceiling.Memory, "ceiling_time", ceiling.Time,
			"ceiling_threads", ceiling.Threads)
	}

	return matched
}

// HashPassword hashes a password with the default argon2 cost. Convenience helper for
// callers that don't tune cost (e.g. tests); production wiring injects a PasswordHasher.
func HashPassword(password string) (string, error) {
	operationResult, operationErr := hashArgon2(password, DefaultArgon2Params())
	if operationErr != nil {
		return operationResult, fmt.Errorf("hash argon2: %w", operationErr)
	}

	return operationResult, nil
}

// VerifyPassword reports whether password matches storedHash (reads embedded params).
func VerifyPassword(storedHash, password string) bool {
	ceiling := DefaultArgon2Params().acceptedCeiling()

	matched, err := verifyArgon2(storedHash, password, ceiling)
	if err != nil {
		// A cost above the ceiling is just a non-match for this config-less helper.
		return false
	}

	return matched
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
		return "", fmt.Errorf("validate: %w", err)
	}

	key, err := deriveArgon2Key(password, kdfSalt, params, pwKeyLen)
	if err != nil {
		return "", fmt.Errorf("derive argon2 key: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(key), nil
}
