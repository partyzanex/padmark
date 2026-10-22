package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
)

const (
	editCodeSaltLen = 16
	// editCodeVersion tags the fast salted-SHA-256 edit-code format ("s1$salt$key").
	editCodeVersion = "s1"
	// legacyArgon2Prefix marks argon2id edit-code hashes ("v1$…"). The name is historical:
	// it is both the format of codes hashed before the switch to "s1$" and the current
	// format HashUserProvided writes for caller-supplied codes. Verify accepts it either way.
	legacyArgon2Prefix = "v1$"
	editCodeParts      = 3
)

// EditCodeHasher hashes and verifies note edit codes.
//
// There are two kinds of edit codes with opposite brute-force economics:
//
//   - generated codes (domain.NewEditCode): 12 chars of high-entropy random material, so a
//     fast salted SHA-256 ("s1$…") gives the same at-rest protection as a memory-hard KDF —
//     a DB leak still reveals no usable code — without paying argon2's per-call memory/CPU
//     cost on every create.
//   - user-supplied codes (caller-chosen via the API): potentially low-entropy, human-chosen
//     values ("password123"), so a leaked fast hash would be brute-forceable at billions of
//     guesses per second. HashUserProvided hashes these with argon2id instead.
//
// Verify accepts both stored formats (plus the legacy argon2id "v1$" hashes) so existing
// codes remain valid and no data migration is needed.
type EditCodeHasher struct {
	log *slog.Logger
	// params is the argon2id cost HashUserProvided applies, the same --argon2-* configuration
	// the password hasher uses. Verify derives with the cost embedded in each stored hash and
	// only consults ceiling, the most it will accept from a stored row.
	params  Argon2Params
	ceiling Argon2Params
}

// NewEditCodeHasher returns an EditCodeHasher whose HashUserProvided uses the given argon2id
// cost. The fast generated-code hash has no cost parameters. Zero-valued fields fall back to
// DefaultArgon2Params, like NewPasswordHasher.
func NewEditCodeHasher(params Argon2Params) *EditCodeHasher {
	return &EditCodeHasher{
		log:     slog.New(slog.DiscardHandler),
		params:  params.withDefaults(),
		ceiling: params.acceptedCeiling(),
	}
}

// WithLogger sets the logger that records stored hashes refused for an excessive cost (WARN).
// The default discards them.
func (h *EditCodeHasher) WithLogger(log *slog.Logger) *EditCodeHasher {
	h.log = log

	return h
}

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

// HashUserProvided hashes a caller-supplied edit code with argon2id at the configured cost.
// Unlike Hash, this path must assume low-entropy, human-chosen values, so a leaked hash must
// not be crackable at salted-SHA-256 speeds. The output keeps the "v1$" envelope that Verify
// already dispatches to the argon2 verifier, so verification needs no extra plumbing.
func (h *EditCodeHasher) HashUserProvided(code string) (string, error) {
	hashed, err := hashArgon2(code, h.params)
	if err != nil {
		return "", fmt.Errorf("hash user edit code: %w", err)
	}

	return hashed, nil
}

// Verify reports whether code matches storedHash using a constant-time comparison.
// Legacy argon2id hashes ("v1$…") are delegated to the argon2 verifier for backward compatibility.
func (h *EditCodeHasher) Verify(storedHash, code string) bool {
	if strings.HasPrefix(storedHash, legacyArgon2Prefix) {
		return verifyLogged(h.log, "edit code", storedHash, code, h.ceiling)
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
