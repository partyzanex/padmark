package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// ContentType represents the content format of a note.
type ContentType string

const (
	ContentTypeMarkdown ContentType = "text/markdown"
	ContentTypePlain    ContentType = "text/plain"
)

// Valid reports whether the content type is supported.
func (ct ContentType) Valid() bool {
	switch ct {
	case ContentTypeMarkdown, ContentTypePlain:
		return true
	}

	return false
}

const MaxTitleLength = 500

// Privacy controls who may read a note. The zero value is never used directly — nil on Note
// means "not set" (defaults to PrivacyPublic on create, keeps the existing value on update),
// mirroring the ContentType convention below.
type Privacy string

const (
	PrivacyPublic        Privacy = "public"
	PrivacyAuthenticated Privacy = "authenticated"
	PrivacyOwner         Privacy = "owner"
)

// Valid reports whether the privacy level is supported.
func (p Privacy) Valid() bool {
	switch p {
	case PrivacyPublic, PrivacyAuthenticated, PrivacyOwner:
		return true
	}

	return false
}

type Note struct {
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt *time.Time
	Privacy   *Privacy
	// OwnerID is the ID of the user who created the note while authenticated (session or API
	// token); nil for anonymous notes. Lets Update/Delete bypass EditCode for the exact creator
	// — see notes.Manager.Update/Delete.
	OwnerID          *uuid.UUID
	ID               string
	Title            string
	Content          string
	ContentType      *ContentType // nil means not set (keep existing on update); defaults to markdown on create
	EditCode         string
	Views            int
	BurnTTL          int64
	BurnAfterReading bool
}

// HashSlug returns the SHA-256 hex digest of slug, used as the database primary key (for notes)
// and as the reveal-token note identifier (for burn-after-reading confirmation) — anywhere the
// plaintext slug, which doubles as content-encryption key material, must not be persisted.
// It lives here rather than in internal/infra/crypto because both internal/usecases/notes and
// internal/adapters/http need it and neither may import infra (onion architecture); domain is
// the one layer both may import.
//
// Security note: this is a FAST hash, so a stolen database lets an attacker brute-force the
// slug offline against this column and then derive the content key. The slug is therefore
// the real strength parameter (~log2(62)*len bits; ~60 bits at the default 10-char length),
// not a substitute for it. A DB dump alone is breakable in roughly days-to-months on serious
// GPU hardware at 60 bits — raise the slug length for stronger at-rest protection, or
// introduce a server-side pepper if "DB exfil alone is useless" is a required property.
func HashSlug(slug string) string {
	sum := sha256.Sum256([]byte(slug))

	return hex.EncodeToString(sum[:])
}

// Validate checks that the note fields satisfy business rules.
func (n *Note) Validate() error {
	if len([]rune(n.Title)) > MaxTitleLength {
		return ErrTitleTooLong
	}

	if n.ContentType != nil && !n.ContentType.Valid() {
		return ErrInvalidContentType
	}

	if n.Privacy != nil && !n.Privacy.Valid() {
		return ErrInvalidPrivacy
	}

	return nil
}

// ValidateOwnership checks that PrivacyOwner is only used on a note that actually has an owner —
// otherwise no caller, including the creator, could ever read it back (see VisibleTo/OwnedBy).
// Deliberately NOT part of Validate(): OwnerID is only in its final state (a) immediately for
// Create, where the HTTP layer sets it before calling Manager.Create, and (b) in Manager.Update
// only after OwnerID is pinned from the existing note — Update's own upfront Validate() call runs
// before that pin, when the caller-supplied OwnerID is always nil regardless of the note's actual
// ownership, so folding this check into Validate() would wrongly reject legitimate updates.
func (n *Note) ValidateOwnership() error {
	if n.EffectivePrivacy() == PrivacyOwner && n.OwnerID == nil {
		return ErrOwnerPrivacyRequiresOwner
	}

	return nil
}

// OwnedBy reports whether userID is the authenticated user who created n — see OwnerID. A nil
// userID (anonymous caller, uuid.Nil) or an unowned note (created anonymously) never match.
func (n *Note) OwnedBy(userID uuid.UUID) bool {
	return userID != uuid.Nil && n.OwnerID != nil && *n.OwnerID == userID
}

// EffectivePrivacy returns n.Privacy, defaulting to PrivacyPublic when unset.
func (n *Note) EffectivePrivacy() Privacy {
	if n.Privacy == nil {
		return PrivacyPublic
	}

	return *n.Privacy
}

// VisibleTo reports whether a caller may read n given its privacy level: PrivacyOwner requires
// callerID to match OwnerID (see OwnedBy), PrivacyAuthenticated requires any authenticated
// caller, and PrivacyPublic (the default) always allows it.
func (n *Note) VisibleTo(callerID uuid.UUID, authenticated bool) bool {
	switch n.EffectivePrivacy() {
	case PrivacyOwner:
		return n.OwnedBy(callerID)
	case PrivacyAuthenticated:
		return authenticated
	default:
		return true
	}
}
