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

const (
	MaxTitleLength   = 500
	MaxContentLength = 100_000
)

type Note struct {
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ExpiresAt        *time.Time // nil means the note never expires; set after first read for burn notes with BurnTTL > 0
	ID               string
	Title            string
	Content          string
	ContentType      ContentType
	EditCode         string // secret token required to edit or delete the note
	Views            int
	BurnTTL          int64 // seconds the note lives after first read; 0 = deleted immediately on first read
	BurnAfterReading bool  // if true, the note is consumed on the first read (deleted or timer-started)
}

// Validate checks that the note fields satisfy business rules.
func (n *Note) Validate() error {
	if n.Title == "" {
		return ErrTitleRequired
	}

	if len([]rune(n.Title)) > MaxTitleLength {
		return ErrTitleTooLong
	}

	if len(n.Content) > MaxContentLength {
		return ErrContentTooLong
	}

	if n.ContentType != "" && !n.ContentType.Valid() {
		return ErrInvalidContentType
	}

	return nil
}
