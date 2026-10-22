package domain

import "errors"

var (
	ErrNotFound = errors.New("not found")
	// ErrExpired is shared by notes and links — both expire the same way (hard TTL, deleted on
	// next access) — so its message stays resource-neutral rather than saying "note".
	ErrExpired            = errors.New("expired")
	ErrTitleTooLong       = errors.New("title exceeds maximum length")
	ErrContentTooLarge    = errors.New("content exceeds maximum length")
	ErrInvalidContentType = errors.New("unsupported content type")
	ErrInvalidPrivacy     = errors.New("invalid privacy level")
	// ErrOwnerPrivacyRequiresOwner is returned when privacy=owner would be set on a note with no
	// OwnerID (created anonymously, or by a caller with no session/API-token identity) — such a
	// note could never be read back by anyone, including its own creator. See Note.ValidateOwnership.
	ErrOwnerPrivacyRequiresOwner = errors.New("owner-only privacy requires an authenticated owner")
	ErrInvalidSlug               = errors.New("invalid slug: 1-100 chars, up to 10 path-like " +
		"segments of alphanumeric/dot/hyphen/underscore joined by '/', each starting with " +
		"alphanumeric; must not start with a reserved route name")
	ErrSlugConflict = errors.New("slug is already taken")
	// ErrCustomSlugDisabled is returned when a create request supplies a custom slug but the
	// server has human-readable (custom) slugs turned off (--custom-slugs=false, the default).
	ErrCustomSlugDisabled = errors.New("custom slugs are disabled on this server")
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidEditCode    = errors.New("invalid edit code")
	ErrDecryptionFailed   = errors.New("content decryption failed")
	// ErrInvalidURL is returned when a link's target URL fails the scheme whitelist
	// (http/https only) — see links.Manager.Create.
	ErrInvalidURL = errors.New("invalid target url: only http and https schemes are allowed")
	// ErrHashCollision is returned when random slug generation exhausts its retry budget without
	// finding a free slug — see links.Manager.Create's maxHashRetries.
	ErrHashCollision = errors.New("could not generate a unique slug")
	// ErrMalformedCiphertext marks stored ciphertext that is structurally invalid (corrupt
	// base64 / truncated) rather than merely undecryptable with the supplied key. Lets the
	// usecase log genuine data corruption distinctly from an ordinary wrong-slug miss.
	ErrMalformedCiphertext = errors.New("malformed ciphertext")

	ErrInvalidTOTP     = errors.New("invalid or expired TOTP code")
	ErrInviteExpired   = errors.New("invite link has expired")
	ErrInviteUsed      = errors.New("invite link has already been used")
	ErrUserExists      = errors.New("username is already taken")
	ErrSessionExpired  = errors.New("session has expired")
	ErrInvalidPassword = errors.New("invalid password")
	ErrWeakPassword    = errors.New("password does not meet complexity requirements")
	ErrInvalidUsername = errors.New("username must be 1–64 characters: letters, digits, dot, underscore, @ or hyphen; " +
		"start with a letter or digit")
	ErrSessionRevocation   = errors.New("password changed, but older sessions could not be revoked")
	ErrFeatureNotSupported = errors.New("feature is not enabled")
	ErrAPITokenLimit       = errors.New("api token limit reached")
	// ErrBurnIntentRequired: a burn-after-click link (or burn-after-reading note) is destroyed by
	// the access that reads it, so an access without deliberate intent — a preview fetch, a
	// scanner, an embedded resource — must leave it intact instead.
	ErrBurnIntentRequired = errors.New("burn resource requires a deliberate access")
)
