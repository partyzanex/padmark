package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MaxFileNameLength is the maximum length of a file name in bytes. Most file systems have the
// same limit, so a longer name cannot be saved on the reader's computer.
const MaxFileNameLength = 255

// File is a published file. ID is HashSlug(slug); the plaintext slug is not persisted.
// The file content is in object storage under ObjectKey, encrypted with ContentKey, a random
// key that is unique to the file. Storage keeps Name and ContentKey only inside SealedMeta,
// which the installation keyring encrypts; Name and ContentKey are set after decryption.
type File struct {
	CreatedAt         time.Time
	ExpiresAt         *time.Time
	Privacy           *Privacy
	OwnerID           *uuid.UUID
	SealedMeta        string
	ObjectKey         string
	ID                string
	Name              string
	EditCode          string
	ContentKey        []byte
	Size              int64
	Downloads         int
	BurnAfterDownload bool
}

// Validate checks that the file fields satisfy business rules.
func (f *File) Validate() error {
	if f.Privacy != nil && !f.Privacy.Valid() {
		return ErrInvalidPrivacy
	}

	return nil
}

// ValidateOwnership checks that PrivacyOwner is used only on a file that has an owner.
// Otherwise nobody, the uploader included, could download it.
func (f *File) ValidateOwnership() error {
	if f.EffectivePrivacy() == PrivacyOwner && f.OwnerID == nil {
		return ErrOwnerPrivacyRequiresOwner
	}

	return nil
}

// OwnedBy reports whether userID is the authenticated user who uploaded f.
func (f *File) OwnedBy(userID uuid.UUID) bool {
	return userID != uuid.Nil && f.OwnerID != nil && *f.OwnerID == userID
}

// EffectivePrivacy returns f.Privacy, or PrivacyPublic when it is not set.
func (f *File) EffectivePrivacy() Privacy {
	if f.Privacy == nil {
		return PrivacyPublic
	}

	return *f.Privacy
}

// Expired reports whether the TTL of f has elapsed at now. A nil ExpiresAt never expires.
func (f *File) Expired(now time.Time) bool {
	return f.ExpiresAt != nil && now.After(*f.ExpiresAt)
}

// VisibleTo reports whether a caller may download f. It has the same semantics as
// Note.VisibleTo, including the fail-closed default for an unknown stored value.
func (f *File) VisibleTo(callerID uuid.UUID, authenticated bool) bool {
	switch f.EffectivePrivacy() {
	case PrivacyPublic:
		return true
	case PrivacyAuthenticated:
		return authenticated
	case PrivacyOwner:
		return f.OwnedBy(callerID)
	default:
		return false
	}
}

// CleanFileName returns the base name of an uploaded file name. It removes the directory part
// that some clients send, and control characters, because the name goes into a
// Content-Disposition header. It returns ErrInvalidFileName for an empty or too long result.
func CleanFileName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", ErrInvalidFileName
	}

	base := name
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}

	base = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return -1
		}

		return char
	}, base)
	base = strings.TrimSpace(base)

	if base == "" || base == "." || base == ".." || len(base) > MaxFileNameLength {
		return "", ErrInvalidFileName
	}

	return base, nil
}
