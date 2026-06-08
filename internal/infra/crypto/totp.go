package crypto

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"image/png"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	totpWindow  = 1   // allow ±1 period (30s each side)
	totpPeriod  = 30  // RFC 6238 period in seconds
	qrImageSize = 256 // QR code PNG size in pixels
)

// GenerateTOTPSecret returns a new random base32-encoded TOTP secret (20 bytes).
func GenerateTOTPSecret() (string, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "padmark",
		AccountName: "setup",
	})
	if err != nil {
		return "", fmt.Errorf("generate totp key: %w", err)
	}

	return key.Secret(), nil
}

// ValidateTOTP checks code against secret (base32) using RFC 6238, ±1 period window.
// An invalid secret or malformed code returns false.
func ValidateTOTP(secret, code string) bool {
	valid, err := totp.ValidateCustom(code, secret, time.Now(), totp.ValidateOpts{
		Period:    totpPeriod,
		Skew:      totpWindow,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})

	return valid && err == nil
}

// ValidateTOTPWithCounter validates code and returns the TOTP counter index that matched.
// counter is the raw time-step value floor(unix/period); it is zero when the code is invalid.
// Callers must reject codes whose counter ≤ the last accepted counter to prevent replay attacks.
func ValidateTOTPWithCounter(secret, code string) (valid bool, counter int64) {
	now := time.Now()
	timeStep := now.Unix() / totpPeriod

	opts := totp.ValidateOpts{
		Period:    totpPeriod,
		Skew:      0,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	}

	for delta := int64(-totpWindow); delta <= int64(totpWindow); delta++ {
		candidate := timeStep + delta
		t := time.Unix(candidate*totpPeriod, 0)

		expected, err := totp.GenerateCodeCustom(secret, t, opts)
		if err != nil {
			continue
		}

		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return true, candidate
		}
	}

	return false, 0
}

// GenerateQRCodeDataURL returns a data URL (image/png;base64) for the otpauth:// URI.
// issuer is the service name shown in the authenticator app.
func GenerateQRCodeDataURL(issuer, username, secret string) (string, error) {
	key, err := otp.NewKeyFromURL(
		fmt.Sprintf(
			"otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=%d",
			issuer, username, secret, issuer, totpPeriod,
		),
	)
	if err != nil {
		return "", fmt.Errorf("parse otpauth url: %w", err)
	}

	img, err := key.Image(qrImageSize, qrImageSize)
	if err != nil {
		return "", fmt.Errorf("generate qr image: %w", err)
	}

	var buf bytes.Buffer

	encErr := png.Encode(&buf, img)
	if encErr != nil {
		return "", fmt.Errorf("encode qr png: %w", encErr)
	}

	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
