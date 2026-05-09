package domain

import "time"

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
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ExpiresAt        *time.Time
	Private          *bool
	ID               string
	Title            string
	Content          string
	ContentType      *ContentType // nil means not set (keep existing on update); defaults to markdown on create
	EditCode         string
	Views            int
	BurnTTL          int64
	BurnAfterReading bool
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
