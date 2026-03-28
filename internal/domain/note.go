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

type Note struct {
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   *time.Time // nil means the note never expires
	ID          string
	Title       string
	Content     string
	ContentType ContentType
}
