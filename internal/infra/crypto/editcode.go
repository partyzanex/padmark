package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Memory  uint32 = 64 * 1024 // 64 MiB
	argon2Time    uint32 = 1
	argon2Threads uint8  = 1
	argon2KeyLen  uint32 = 32
	argon2SaltLen        = 16
)

// EditCodeHasher hashes and verifies note edit codes using argon2id.
type EditCodeHasher struct{}

// NewEditCodeHasher returns a new EditCodeHasher.
func NewEditCodeHasher() *EditCodeHasher { return &EditCodeHasher{} }

// Hash derives an argon2id hash from code.
// Stored format: "v1$<memory>$<time>$<threads>$<base64url_salt>$<base64url_key>"
// Parameters are embedded so future changes don't break existing hashes.
func (h *EditCodeHasher) Hash(code string) (string, error) {
	salt := make([]byte, argon2SaltLen)

	_, err := rand.Read(salt)
	if err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}

	key := argon2.IDKey([]byte(code), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	enc := base64.RawURLEncoding

	return fmt.Sprintf("v1$%d$%d$%d$%s$%s",
		argon2Memory, argon2Time, uint32(argon2Threads),
		enc.EncodeToString(salt),
		enc.EncodeToString(key),
	), nil
}

// Verify reports whether code matches storedHash.
// Reads parameters from the stored hash to support future param rotation.
// Uses constant-time comparison to prevent timing attacks.
func (h *EditCodeHasher) Verify(storedHash, code string) bool {
	parts := strings.Split(storedHash, "$")
	if len(parts) != 6 || parts[0] != "v1" {
		return false
	}

	memory, err := parseU32(parts[1])
	if err != nil {
		return false
	}

	time, err := parseU32(parts[2])
	if err != nil {
		return false
	}

	threads, err := parseU32(parts[3])
	if err != nil || threads > math.MaxUint8 {
		return false
	}

	enc := base64.RawURLEncoding

	salt, err := enc.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}

	expected, err := enc.DecodeString(parts[5])
	if err != nil || len(expected) == 0 {
		return false
	}

	//nolint:gosec // argon2 key length fits uint32 by design
	actual := argon2.IDKey([]byte(code), salt, time, memory, uint8(threads), uint32(len(expected)))

	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func parseU32(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse u32: %w", err)
	}

	return uint32(v), nil
}
