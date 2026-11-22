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
