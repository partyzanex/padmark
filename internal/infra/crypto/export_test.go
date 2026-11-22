package crypto

// This file gives the external test package helpers that production code does not use.

import (
	"fmt"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

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

// ValidateTOTP checks code against secret (base32) using RFC 6238, ±1 period window.
// An invalid secret or malformed code returns false.
func ValidateTOTP(secret, code string) bool {
	valid, err := totp.ValidateCustom(code, secret, time.Now(), totp.ValidateOpts{
		Period:    totpPeriod,
		Skew:      totpWindow,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
		// Encoder zero = default base32 encoding (otp.EncoderDefault), matches GenerateTOTPSecret's output.
		Encoder: otp.EncoderDefault,
	})

	return valid && err == nil
}

// Argon2MemoryBudget returns the current process-wide argon2id workspace budget in KiB.
func Argon2MemoryBudget() uint32 {
	return currentArgon2Budget().kib
}

// tryAcquire is the non-blocking form of acquire; ok is false when kib KiB are not free now
// or earlier waiters are queued.
func (b *argon2Budget) tryAcquire(kib uint32) (func(), bool) {
	weight := b.weight(kib)

	if !b.sem.TryAcquire(weight) {
		return nil, false
	}

	return func() { b.sem.Release(weight) }, true
}

// contentAAD is the note AAD (additional authenticated data): a purpose/version prefix, kind,
// resource byte and the 32-byte database ID.
func (enc *ContentCipher) contentAAD(recordID string) ([]byte, error) {
	return enc.contentAADForKind(recordID, contentKind)
}
