package crypto_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

func TestGenerateTOTPSecret_ReturnsNonEmptyBase32(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()

	require.NoError(t, err)
	assert.NotEmpty(t, secret)
	// base32 alphabet: A-Z and 2-7 only
	assert.True(t, isBase32(secret), "secret must be valid base32: %q", secret)
}

func TestGenerateTOTPSecret_UniqueEachCall(t *testing.T) {
	secret1, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	secret2, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	assert.NotEqual(t, secret1, secret2)
}

func TestValidateTOTP_ValidCode(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	assert.True(t, crypto.ValidateTOTP(secret, code))
}

func TestValidateTOTP_WrongCode(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	assert.False(t, crypto.ValidateTOTP(secret, "000000"))
}

func TestValidateTOTP_EmptyCode(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	assert.False(t, crypto.ValidateTOTP(secret, ""))
}

func TestValidateTOTP_WrongSecret(t *testing.T) {
	_, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	code, err := totp.GenerateCode("JBSWY3DPEHPK3PXP", time.Now()) // different secret
	require.NoError(t, err)

	assert.False(t, crypto.ValidateTOTP("DIFFERENTSECRETSECRETKEY", code))
}

func TestValidateTOTP_PreviousPeriodAccepted(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	// Code from 30s ago should be accepted by ±1 window
	past := time.Now().Add(-30 * time.Second)
	code, err := totp.GenerateCode(secret, past)
	require.NoError(t, err)

	assert.True(t, crypto.ValidateTOTP(secret, code))
}

func TestValidateTOTP_TwoPeriodsAgoRejected(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	// Code from 61s ago is outside the ±1 window
	past := time.Now().Add(-61 * time.Second)
	code, err := totp.GenerateCode(secret, past)
	require.NoError(t, err)

	assert.False(t, crypto.ValidateTOTP(secret, code))
}

func TestGenerateQRCodeDataURL_ReturnsDataURL(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	url, err := crypto.GenerateQRCodeDataURL("padmark", "alice", secret)

	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(url, "data:image/png;base64,"), "must be a PNG data URL")
}

func TestGenerateQRCodeDataURL_DifferentUsersProduceDifferentURLs(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	url1, err := crypto.GenerateQRCodeDataURL("padmark", "alice", secret)
	require.NoError(t, err)

	url2, err := crypto.GenerateQRCodeDataURL("padmark", "bob", secret)
	require.NoError(t, err)

	assert.NotEqual(t, url1, url2)
}

// ValidateTOTPWithCounter

func TestValidateTOTPWithCounter_ValidCode_ReturnsCounterAndTrue(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	now := time.Now()
	code, err := totp.GenerateCode(secret, now)
	require.NoError(t, err)

	valid, counter := crypto.ValidateTOTPWithCounter(secret, code)

	assert.True(t, valid)
	// Counter must equal floor(unix/30) ± 1.
	expected := now.Unix() / 30
	assert.InDelta(t, expected, counter, 1)
}

// TestValidateTOTPWithCounter_SameCode_StableCounter verifies the counter returned for a given
// code is stable across calls — the property the replay protection relies on (the same code
// must yield the same counter so a re-use is detected as counter <= lastCounter).
func TestValidateTOTPWithCounter_SameCode_StableCounter(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	valid1, c1 := crypto.ValidateTOTPWithCounter(secret, code)
	valid2, c2 := crypto.ValidateTOTPWithCounter(secret, code)

	require.True(t, valid1)
	require.True(t, valid2)
	assert.Equal(t, c1, c2, "same code must yield the same counter")
}

func TestValidateTOTPWithCounter_WrongCode_ReturnsFalseZeroCounter(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	valid, counter := crypto.ValidateTOTPWithCounter(secret, "000000")

	assert.False(t, valid)
	assert.Zero(t, counter)
}

func TestValidateTOTPWithCounter_PreviousPeriod_ReturnsLowerCounter(t *testing.T) {
	secret, err := crypto.GenerateTOTPSecret()
	require.NoError(t, err)

	past := time.Now().Add(-30 * time.Second)
	code, err := totp.GenerateCode(secret, past)
	require.NoError(t, err)

	valid, counter := crypto.ValidateTOTPWithCounter(secret, code)

	assert.True(t, valid)
	assert.Equal(t, past.Unix()/30, counter)
}

// isBase32 returns true when all characters in str belong to the base32 alphabet (A-Z, 2-7).
func isBase32(str string) bool {
	for _, ch := range strings.ToUpper(str) {
		if (ch < 'A' || ch > 'Z') && (ch < '2' || ch > '7') {
			return false
		}
	}

	return len(str) > 0
}
