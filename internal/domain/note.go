package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
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

type Note struct {
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt *time.Time
	Private   *bool
	// OwnerID is the ID of the user who created the note while authenticated (session or API
	// token); nil for anonymous notes. Lets Update/Delete bypass EditCode for the exact creator
	// — see notes.Manager.Update/Delete.
	OwnerID          *string
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

	return nil
}
