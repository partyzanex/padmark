package crypto

// This file exposes the package-level KDF and TOTP helpers as small method sets so that the
// auth usecase can depend on interfaces it owns (DIP) instead of importing this infra package
// directly. The structural interfaces live next to their consumer in usecases/auth.

// KDF derives encryption keys from passwords. It implements the auth usecase's KeyDeriver.
type KDF struct{}

// NewKDF returns a KDF adapter.
func NewKDF() *KDF { return &KDF{} }

// GenerateSalt returns a fresh random HKDF salt.
func (KDF) GenerateSalt() ([]byte, error) { return GenerateKDFSalt() }

// DeriveKey derives the AES key used to encrypt a user's TOTP secret.
func (KDF) DeriveKey(password, salt []byte) (string, error) { return DeriveUserKey(password, salt) }

// TOTP generates and validates TOTP secrets/codes. It implements the auth usecase's TOTPManager.
type TOTP struct{}

// NewTOTP returns a TOTP adapter.
func NewTOTP() *TOTP { return &TOTP{} }

// GenerateSecret returns a fresh random base32 TOTP secret.
func (TOTP) GenerateSecret() (string, error) { return GenerateTOTPSecret() }

// ValidateWithCounter validates code against secret, returning the accepted time-step counter
// (used by the caller for replay protection).
func (TOTP) ValidateWithCounter(secret, code string) (valid bool, counter int64) {
	return ValidateTOTPWithCounter(secret, code)
}

// GenerateQRCode renders the otpauth enrollment QR code as a data URL.
func (TOTP) GenerateQRCode(issuer, account, secret string) (string, error) {
	return GenerateQRCodeDataURL(issuer, account, secret)
}
