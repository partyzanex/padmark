package domain

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrExpired            = errors.New("note has expired")
	ErrTitleTooLong       = errors.New("title exceeds maximum length")
	ErrContentTooLong     = errors.New("content exceeds maximum length")
	ErrInvalidContentType = errors.New("unsupported content type")
	ErrInvalidSlug        = errors.New("invalid slug: use 1-100 alphanumeric/hyphen/underscore, start with alphanumeric")
	ErrSlugConflict       = errors.New("slug is already taken")
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidEditCode    = errors.New("invalid edit code")
	ErrDecryptionFailed   = errors.New("content decryption failed")
	// ErrMalformedCiphertext marks stored ciphertext that is structurally invalid (corrupt
	// base64 / truncated) rather than merely undecryptable with the supplied key. Lets the
	// usecase log genuine data corruption distinctly from an ordinary wrong-slug miss.
	ErrMalformedCiphertext = errors.New("malformed ciphertext")

	ErrInvalidTOTP         = errors.New("invalid or expired TOTP code")
	ErrInviteExpired       = errors.New("invite link has expired")
	ErrInviteUsed          = errors.New("invite link has already been used")
	ErrUserExists          = errors.New("username is already taken")
	ErrSessionExpired      = errors.New("session has expired")
	ErrInvalidPassword     = errors.New("invalid password")
	ErrWeakPassword        = errors.New("password does not meet complexity requirements")
	ErrLoginLinkInvalid    = errors.New("login link is invalid")
	ErrLoginLinkExpired    = errors.New("login link has expired")
	ErrFeatureNotSupported = errors.New("feature is not enabled")
)
