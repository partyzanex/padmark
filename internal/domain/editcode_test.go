package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidateEditCode pins the caller-chosen edit-code policy (M-8): at least
// MinEditCodeLength and at most MaxEditCodeLength runes, no surrounding whitespace and no
// control characters. Length is counted in runes, so an 8-rune code made of multi-byte
// characters passes even though it is longer than 8 bytes, and a 7-rune one fails.
func TestValidateEditCode(t *testing.T) {
	cases := []struct {
		name string
		code string
		want error
	}{
		{"one char", "1", ErrEditCodeTooShort},
		{"seven chars", "1234567", ErrEditCodeTooShort},
		{"seven multibyte runes", strings.Repeat("ж", MinEditCodeLength-1), ErrEditCodeTooShort},
		{"eight chars", "12345678", nil},
		{"eight multibyte runes", strings.Repeat("ж", MinEditCodeLength), nil},
		{"interior space", "pass word", nil},
		{"max length", strings.Repeat("a", MaxEditCodeLength), nil},
		{"over max length", strings.Repeat("a", MaxEditCodeLength+1), ErrEditCodeMalformed},
		{"leading space", " 12345678", ErrEditCodeMalformed},
		{"trailing tab", "12345678\t", ErrEditCodeMalformed},
		{"embedded newline", "1234\n5678", ErrEditCodeMalformed},
		{"whitespace padding cannot satisfy the minimum", "   1234   ", ErrEditCodeMalformed},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateEditCode(testCase.code)
			if testCase.want == nil {
				assert.NoError(t, err)

				return
			}

			assert.ErrorIs(t, err, testCase.want)
		})
	}
}
