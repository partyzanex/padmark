package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatePassword_Valid(t *testing.T) {
	require.NoError(t, ValidatePassword("Aa1!bbbbbbbb"))
}

func TestValidatePassword_AtMaxLengthOK(t *testing.T) {
	// A complex password exactly at the maximum length must still validate.
	pw := "Aa1!" + strings.Repeat("b", maxPasswordLen-4)
	require.Len(t, pw, maxPasswordLen)
	require.NoError(t, ValidatePassword(pw))
}

func TestValidatePassword_TooLong(t *testing.T) {
	// Over the cap → rejected, bounding argon2 pre-hash work (cheap DoS guard).
	pw := "Aa1!" + strings.Repeat("b", maxPasswordLen)
	require.ErrorIs(t, ValidatePassword(pw), ErrWeakPassword)
}

// TestValidatePassword_SpaceIsNotSpecial locks the policy: a space is allowed and counts
// toward length, but does NOT satisfy the special-character requirement.
func TestValidatePassword_SpaceIsNotSpecial(t *testing.T) {
	// 12 chars, has upper/lower/digit; the only non-alphanumeric char is a space.
	require.ErrorIs(t, ValidatePassword("Aa1 bbbbbbbb"), ErrWeakPassword)
}

func TestValidatePassword_TooShort(t *testing.T) {
	require.ErrorIs(t, ValidatePassword("Aa1!bbb"), ErrWeakPassword)
}

func TestValidatePassword_MissingClasses(t *testing.T) {
	cases := map[string]string{
		"no upper":   "aa1!bbbbbbbb",
		"no lower":   "AA1!BBBBBBBB",
		"no digit":   "Aa!bbbbbbbbb",
		"no special": "Aa1bbbbbbbbbb",
	}

	for name, pw := range cases {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, ValidatePassword(pw), ErrWeakPassword)
		})
	}
}
