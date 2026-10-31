package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MinEditCodeLength is the minimum length, in runes, of a caller-chosen edit code. Edit
	// codes are the only credential for anonymous notes and links, and online guessing is
	// throttled only per bucket (see the HTTP fail-lockout middleware), so a caller-chosen
	// code must carry enough entropy on its own.
	MinEditCodeLength = 8
	// MaxEditCodeLength is the maximum length, in runes, of a caller-chosen edit code. It bounds
	// the input to the memory-hard hash; server-generated codes are far shorter.
	MaxEditCodeLength = 128
)

// ValidateEditCode enforces the policy for a caller-chosen edit code: between
// MinEditCodeLength and MaxEditCodeLength runes (counted as runes, not bytes), with no leading
// or trailing whitespace and no control characters. The code is validated exactly as it will be
// hashed and later compared — it is never trimmed — so a code that passes here can always be
// presented again: surrounding whitespace would be stripped from the X-Edit-Code header by HTTP
// header parsing, and control characters cannot be sent in a header at all.
//
// It applies only when a code is chosen (note/link creation). Server-generated codes
// (NewEditCode) already satisfy it, and stored codes are never re-validated: existing notes and
// links created with a shorter code before this policy keep verifying unchanged.
func ValidateEditCode(code string) error {
	if strings.TrimSpace(code) != code || strings.ContainsFunc(code, unicode.IsControl) {
		return ErrEditCodeMalformed
	}

	length := utf8.RuneCountInString(code)
	if length < MinEditCodeLength {
		return ErrEditCodeTooShort
	}

	if length > MaxEditCodeLength {
		return ErrEditCodeMalformed
	}

	return nil
}
