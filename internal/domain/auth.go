package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// maxUsernameBytes bounds the byte length of a username, so that a long input is rejected
// before the rune count runs.
const maxUsernameBytes = 256

// maxUsernameRunes bounds the number of characters in a username.
const maxUsernameRunes = 64

// ValidateUsername applies to new accounts; existing names remain usable for login.
// Names are exact, case-sensitive identifiers, never silently trimmed or normalized.
func ValidateUsername(username string) error {
	if !utf8.ValidString(username) || len(username) > maxUsernameBytes ||
		utf8.RuneCountInString(username) > maxUsernameRunes || username == "" {
		return ErrInvalidUsername
	}

	for i, char := range username {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			continue
		}

		if i != 0 && strings.ContainsRune("._@-", char) {
			continue
		}

		return ErrInvalidUsername
	}

	return nil
}

const (
	minPasswordLen = 12
	// maxPasswordLen caps the password to keep hashing cheap and bounded. argon2 cost is driven
	// by its memory/time params, but the input is still pre-hashed, so a multi-megabyte password
	// is a cheap DoS vector. 1024 bytes is far longer than any human passphrase.
	maxPasswordLen = 1024
)

// ValidatePassword checks complexity: 12–1024 chars, 1 upper, 1 lower, 1 digit, 1 special
// (punctuation or symbol). Whitespace is allowed and counts toward the length, but does NOT
// satisfy the special-character requirement. Returns ErrWeakPassword on any violation,
// including the length bounds.
func ValidatePassword(password string) error {
	if len(password) < minPasswordLen || len(password) > maxPasswordLen {
		return ErrWeakPassword
	}

	if !hasRequiredCharClasses(password) {
		return ErrWeakPassword
	}

	return nil
}

// hasRequiredCharClasses reports whether password contains at least one upper-case, one
// lower-case, one digit, and one special (punctuation or symbol) character.
func hasRequiredCharClasses(password string) bool {
	var hasUpper, hasLower, hasDigit, hasSpecial bool

	for _, char := range password {
		switch {
		case unicode.IsUpper(char):
			hasUpper = true
		case unicode.IsLower(char):
			hasLower = true
		case unicode.IsDigit(char):
			hasDigit = true
		case unicode.IsPunct(char) || unicode.IsSymbol(char):
			hasSpecial = true
		default:
			// Spaces and other characters satisfy no class.
		}
	}

	return hasUpper && hasLower && hasDigit && hasSpecial
}

// User is a registered padmark user with TOTP-based authentication.
type User struct {
	CreatedAt       time.Time
	LastLoginAt     *time.Time
	Username        string
	TOTPSecret      string
	PasswordHash    string
	KDFSalt         []byte
	LastTOTPCounter int64
	ID              uuid.UUID
	IsAdmin         bool
}

// Invite contains the stored state of a single-use registration link. The raw
// bearer token is returned only when issued; it is never a persisted domain field.
type Invite struct {
	ExpiresAt time.Time
	UsedAt    *time.Time
	TokenHash string
	UsedBy    string
	CreatedBy uuid.UUID
}

// HashInviteToken derives the at-rest key for a high-entropy invitation bearer.
// The digest uses the same base64url SHA-256 representation as API/session keys.
func HashInviteToken(token string) string {
	return hashBearer(token)
}

// HashRevealToken derives the at-rest key for a burn-after-reading reveal bearer. Reveal tokens
// are 256-bit random values, so an unsalted SHA-256 is sufficient (no brute-force surface) and
// keeps lookups indexable, exactly like invite, session and API tokens.
func HashRevealToken(token string) string {
	return hashBearer(token)
}

// hashBearer is the shared base64url-encoded SHA-256 of a high-entropy bearer value.
func hashBearer(token string) string {
	digest := sha256.Sum256([]byte(token))

	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// Session is an authenticated browser session.
type Session struct {
	CreatedAt time.Time
	ExpiresAt time.Time
	// SessionHash is the primary key persisted at rest: the base64url-encoded SHA-256 of the
	// raw session id (mirrors APIToken.TokenHash). The raw id exists only in the client's
	// session cookie and in return values, so a database leak yields no replayable sessions.
	SessionHash string
	UserAgent   string
	IP          string
	UserID      uuid.UUID
}
