package crypto

// This file exposes the package-level TOTP helpers as a small method set so that the auth
// usecase can depend on interfaces it owns (DIP) instead of importing this infra package
// directly. The structural interfaces live next to their consumer in usecases/auth.
// The key-derivation side of that seam is Sealer (sealer.go), which owns the envelope format.

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
