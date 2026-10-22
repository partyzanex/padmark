package crypto

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// sealVersion is the envelope version tag written by Sealer.Seal.
const sealVersion = "v1"

// sealFieldCount is the number of "$"-separated fields in a versioned envelope:
// version, memory, time, threads, ciphertext.
const sealFieldCount = 5

const maxSealedSecretLen = 4096

// legacyHKDFInfo is the HKDF info string of the pre-argon2id user-key derivation. It survives
// only so Open can still read secrets written by that scheme; nothing seals with it.
const legacyHKDFInfo = "padmark-user-key-v1"

// Sealer seals and opens a user's TOTP secret under a key derived from their password.
//
// The envelope carries the argon2id cost it was produced with:
//
//	v1$<memory>$<time>$<threads>$<base64 ciphertext>
//
// so supported changes to configured cost (--argon2-memory and friends) preserve access: Open
// derives with the parameters recorded in the envelope rather than the current configuration,
// and reports stale so the caller can re-seal at the new cost. This mirrors how argon2 password
// hashes embed their own parameters (see hashArgon2) — without it, raising the cost on a running
// deployment would make every stored TOTP secret undecryptable and lock every account out.
//
// Two unversioned legacy layouts stay readable — a bare base64 ciphertext keyed by argon2id at
// the configured cost, and one keyed by HKDF-SHA256 — both reported stale so a successful login
// migrates them forward. Sealer never writes either.
type Sealer struct {
	enc    *Encryptor
	params Argon2Params
}

// NewSealer returns a Sealer that seals at the given argon2id cost; pass the same cost as the
// password hasher. Zero-valued fields fall back to DefaultArgon2Params.
func NewSealer(params Argon2Params) *Sealer {
	return &Sealer{enc: New(), params: params.withDefaults()}
}

// GenerateSalt returns a fresh random per-user KDF salt (16 bytes).
func (s *Sealer) GenerateSalt() ([]byte, error) {
	return GenerateKDFSalt()
}

// Seal encrypts secret under a key derived from password and salt, returning a versioned
// envelope that records the argon2id cost used.
func (s *Sealer) Seal(secret string, password, salt []byte) (string, error) {
	key, err := DeriveUserKey(password, salt, s.params)
	if err != nil {
		return "", err
	}

	ciphertext, err := s.enc.Encrypt(secret, key)
	if err != nil {
		return "", fmt.Errorf("encrypt secret: %w", err)
	}

	return fmt.Sprintf("%s$%d$%d$%d$%s",
		sealVersion, s.params.Memory, s.params.Time, s.params.Threads, ciphertext,
	), nil
}

// Open decrypts a sealed TOTP secret. stale reports that the envelope was written under a
// different argon2id cost or under a legacy scheme, so the caller should re-Seal it and persist
// the result; stale is meaningful only when err is nil.
func (s *Sealer) Open(sealed string, password, salt []byte) (secret string, stale bool, err error) {
	if len(sealed) > maxSealedSecretLen {
		return "", false, errors.New("sealed secret exceeds size limit")
	}

	params, ciphertext, ok := parseSealed(sealed)
	if !ok {
		if strings.Contains(sealed, "$") {
			return "", false, errors.New("invalid sealed secret envelope or unsupported argon2 cost")
		}

		return s.openLegacy(sealed, password, salt)
	}

	key, err := DeriveUserKey(password, salt, params)
	if err != nil {
		return "", false, err
	}

	plaintext, err := s.enc.Decrypt(ciphertext, key)
	if err != nil {
		return "", false, fmt.Errorf("decrypt secret: %w", err)
	}

	return plaintext, params != s.params, nil
}

// openLegacy reads the two pre-versioning layouts, both bare base64 ciphertexts: argon2id at the
// configured cost (written before the envelope carried its parameters), then HKDF-SHA256 (written
// before the move to argon2id). Order is by likelihood, and a wrong key fails AES-GCM
// authentication, so trying both is unambiguous. Every legacy open is stale by definition.
func (s *Sealer) openLegacy(sealed string, password, salt []byte) (secret string, stale bool, err error) {
	argonKey, err := DeriveUserKey(password, salt, s.params)
	if err != nil {
		return "", false, err
	}

	plaintext, argonErr := s.enc.Decrypt(sealed, argonKey)
	if argonErr == nil {
		return plaintext, true, nil
	}

	hkdfKey, err := deriveUserKeyHKDF(password, salt)
	if err != nil {
		return "", false, err
	}

	plaintext, hkdfErr := s.enc.Decrypt(sealed, hkdfKey)
	if hkdfErr == nil {
		return plaintext, true, nil
	}

	// Report the argon2id failure: a genuinely broken envelope is far likelier to be a current
	// one than a pre-argon2id leftover, and the HKDF attempt is only a migration fallback.
	return "", false, fmt.Errorf("decrypt legacy secret: %w", argonErr)
}

// parseSealed splits a versioned envelope into its argon2id cost and ciphertext. ok is false for
// anything that is not a well-formed versioned envelope; the caller then treats it as legacy.
// Splitting on "$" is safe: standard base64 never produces that byte.
func parseSealed(sealed string) (params Argon2Params, ciphertext string, ok bool) {
	// none is the zero cost returned with ok == false; the caller must not read it.
	var none Argon2Params

	parts := strings.SplitN(sealed, "$", sealFieldCount)
	if len(parts) != sealFieldCount || parts[0] != sealVersion {
		return none, "", false
	}

	params, err := parseArgon2Cost(parts[1], parts[2], parts[3])
	if err != nil {
		return none, "", false
	}

	return params, parts[4], true
}

// deriveUserKeyHKDF reproduces the pre-argon2id user-key derivation (HKDF-SHA256, hex-encoded).
// It exists only so secrets sealed by that scheme can be opened once and migrated forward.
// HKDF is a single SHA-256 pass, which is why it was replaced: a stolen users table otherwise
// yields TOTP secrets at SHA-256 brute-force speed. Never use it to seal.
func deriveUserKeyHKDF(password, kdfSalt []byte) (string, error) {
	reader := hkdf.New(sha256.New, password, kdfSalt, []byte(legacyHKDFInfo))

	key := make([]byte, pwKeyLen)

	_, err := io.ReadFull(reader, key)
	if err != nil {
		return "", fmt.Errorf("hkdf: %w", err)
	}

	return hex.EncodeToString(key), nil
}
