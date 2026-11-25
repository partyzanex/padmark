package web

import "strings"

// attachmentDisposition returns the Content-Disposition value for name, as RFC 6266 recommends:
// an ASCII filename for old clients, then filename* with the full UTF-8 name. Clients that know
// filename* use it. Both values contain only safe characters, so a name with a quote, a
// semicolon, a backslash or a line break cannot add a parameter or a header.
func attachmentDisposition(name string) string {
	return `attachment; filename="` + asciiFileName(name) + `"; filename*=UTF-8''` + encodeExtValue(name)
}

// asciiFileName replaces each character that is not printable ASCII, and each quote and
// backslash, with an underscore. The result is safe in a quoted-string without escapes.
func asciiFileName(name string) string {
	return strings.Map(func(char rune) rune {
		if char < ' ' || char > '~' || char == '"' || char == '\\' {
			return '_'
		}

		return char
	}, name)
}

// encodeExtValue percent-encodes each byte of value that is not an attr-char of RFC 8187.
func encodeExtValue(value string) string {
	const (
		hexDigits  = "0123456789ABCDEF"
		nibbleBits = 4
		nibbleMask = 0x0f
	)

	out := make([]byte, 0, len(value))

	for idx := range len(value) {
		char := value[idx]
		if isAttrChar(char) {
			out = append(out, char)

			continue
		}

		out = append(out, '%', hexDigits[char>>nibbleBits], hexDigits[char&nibbleMask])
	}

	return string(out)
}

// isAttrChar reports whether char is an attr-char of RFC 8187: a letter, a digit or one of
// "!#$&+-.^_`|~". Each other byte must be percent-encoded.
func isAttrChar(char byte) bool {
	switch {
	case 'a' <= char && char <= 'z', 'A' <= char && char <= 'Z', '0' <= char && char <= '9':
		return true
	default:
		return strings.IndexByte("!#$&+-.^_`|~", char) >= 0
	}
}
