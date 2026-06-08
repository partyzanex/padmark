package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	editCodeSaltLen = 16
	// editCodeVersion tags the fast salted-SHA-256 edit-code format ("s1$salt$key").
	editCodeVersion = "s1"
	// legacyArgon2Prefix marks edit codes hashed with the old argon2id scheme; Verify still
	// accepts them so notes created before the switch keep working.
	legacyArgon2Prefix = "v1$"
	editCodeParts      = 3
)

// EditCodeHasher hashes and verifies note edit codes.
//
// Edit codes are high-entropy random strings (see notes.newEditCode), so — unlike passwords —
// they cannot be brute-forced and do NOT need a memory-hard KDF. A fast salted SHA-256 gives the
// same at-rest protection (a DB leak still reveals no usable code) without argon2's per-call
// memory/CPU cost, which was being paid on every note create/update/delete. Verify additionally
// accepts the legacy argon2id format so existing hashes remain valid.
type EditCodeHasher struct{}

// NewEditCodeHasher returns an EditCodeHasher. It takes no cost parameters: the fast hash has none.
func NewEditCodeHasher() *EditCodeHasher { return &EditCodeHasher{} }

// Hash returns a salted SHA-256 of code in the format "s1$<base64url_salt>$<base64url_digest>".
func (h *EditCodeHasher) Hash(code string) (string, error) {
	salt := make([]byte, editCodeSaltLen)

	_, err := rand.Read(salt)
	if err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}

	enc := base64.RawURLEncoding

	return editCodeVersion + "$" + enc.EncodeToString(salt) + "$" +
		enc.EncodeToString(editCodeDigest(salt, code)), nil
}

// Verify reports whether code matches storedHash using a constant-time comparison.
// Legacy argon2id hashes ("v1$…") are delegated to the argon2 verifier for backward compatibility.
func (h *EditCodeHasher) Verify(storedHash, code string) bool {
	if strings.HasPrefix(storedHash, legacyArgon2Prefix) {
		return verifyArgon2(storedHash, code)
	}

	parts := strings.Split(storedHash, "$")
	if len(parts) != editCodeParts || parts[0] != editCodeVersion {
		return false
	}

	enc := base64.RawURLEncoding

	salt, err := enc.DecodeString(parts[1])
	if err != nil || len(salt) == 0 {
		return false
	}

	expected, err := enc.DecodeString(parts[2])
	if err != nil || len(expected) == 0 {
		return false
	}

	return subtle.ConstantTimeCompare(expected, editCodeDigest(salt, code)) == 1
}

// editCodeDigest computes SHA-256(salt || code).
func editCodeDigest(salt []byte, code string) []byte {
	sum := sha256.New()
	sum.Write(salt)
	sum.Write([]byte(code))

	return sum.Sum(nil)
}
