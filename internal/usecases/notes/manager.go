//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=notes

package notes

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"html"
	"log/slog"
	"math/big"
	"regexp"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

var slugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}$`)

const slugChars = "abcdefghijklmnopqrstuvwxyz0123456789"

func newSlug() string {
	const length = 10

	charsetSize := big.NewInt(int64(len(slugChars)))
	buf := make([]byte, length)

	for idx := range length {
		nn, err := rand.Int(rand.Reader, charsetSize)
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}

		buf[idx] = slugChars[nn.Int64()]
	}

	return string(buf)
}

const editCodeChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func newEditCode() string {
	const length = 12

	charsetSize := big.NewInt(int64(len(editCodeChars)))
	buf := make([]byte, length)

	for idx := range length {
		nn, err := rand.Int(rand.Reader, charsetSize)
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}

		buf[idx] = editCodeChars[nn.Int64()]
	}

	return string(buf)
}

const (
	maxTitleLength   = 500
	maxContentLength = 100_000
)

// Storage defines persistence operations for notes.
type Storage interface {
	Create(ctx context.Context, note *domain.Note) error
	Get(ctx context.Context, id string) (*domain.Note, error)
	Consume(ctx context.Context, id string) (*domain.Note, error)
	Update(ctx context.Context, id string, note *domain.Note) error
	Delete(ctx context.Context, id string) error
	IncrementViews(ctx context.Context, id string) error
}

// Renderer defines markdown-to-HTML rendering.
type Renderer interface {
	Render(content string) (string, error)
}

// Manager implements note business logic.
type Manager struct {
	storage  Storage
	renderer Renderer
	log      *slog.Logger
}

// NewManager creates a new Manager with required dependencies.
func NewManager(storage Storage, renderer Renderer, log *slog.Logger) *Manager {
	return &Manager{storage: storage, renderer: renderer, log: log}
}

// Create validates and persists a new note.
func (m *Manager) Create(ctx context.Context, note *domain.Note) (*domain.Note, error) {
	if note.ContentType == "" {
		note.ContentType = domain.ContentTypeMarkdown
	}

	err := m.validate(note)
	if err != nil {
		return nil, err
	}

	if note.ID != "" {
		if !slugRe.MatchString(note.ID) {
			return nil, domain.ErrInvalidSlug
		}
	} else {
		note.ID = newSlug()
	}

	note.EditCode = newEditCode()

	now := time.Now()
	note.CreatedAt = now
	note.UpdatedAt = now

	err = m.storage.Create(ctx, note)
	if err != nil {
		return nil, fmt.Errorf("create note: %w", err)
	}

	m.log.DebugContext(ctx, "note created", "id", note.ID)

	return note, nil
}

// Peek fetches a note by ID without incrementing views or triggering burn-after-reading.
func (m *Manager) Peek(ctx context.Context, id string) (*domain.Note, error) {
	note, err := m.storage.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("peek note: %w", err)
	}

	return note, nil
}

// Get returns a note by ID. If the note has expired it is deleted and ErrExpired is returned.
// Burn-after-reading notes are atomically consumed (deleted and returned) to prevent races.
func (m *Manager) Get(ctx context.Context, id string) (*domain.Note, error) {
	note, err := m.storage.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get note: %w", err)
	}

	if note.ExpiresAt != nil && time.Now().After(*note.ExpiresAt) {
		delErr := m.storage.Delete(ctx, id)
		if delErr != nil {
			m.log.ErrorContext(ctx, "delete expired note", "id", id, "err", delErr)
		}

		return nil, domain.ErrExpired
	}

	if note.BurnAfterReading {
		consumed, consumeErr := m.storage.Consume(ctx, id)
		if consumeErr != nil {
			return nil, fmt.Errorf("burn after reading: %w", consumeErr)
		}

		m.log.DebugContext(ctx, "note burned", "id", id)

		return consumed, nil
	}

	return note, nil
}

// View returns a note by ID and increments its view counter.
// Burn-after-reading notes are not incremented because they are deleted on read.
func (m *Manager) View(ctx context.Context, id string) (*domain.Note, error) {
	note, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if !note.BurnAfterReading || note.ExpiresAt != nil {
		incErr := m.storage.IncrementViews(ctx, id)
		if incErr != nil {
			m.log.ErrorContext(ctx, "increment views", "id", id, "err", incErr)
		} else {
			note.Views++
		}
	}

	return note, nil
}

// Update validates and updates an existing note, preserving immutable metadata.
// The caller must supply the correct edit code.
func (m *Manager) Update(
	ctx context.Context, id, editCode string, note *domain.Note,
) (*domain.Note, error) {
	err := m.validate(note)
	if err != nil {
		return nil, err
	}

	existing, err := m.storage.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	if subtle.ConstantTimeCompare([]byte(existing.EditCode), []byte(editCode)) != 1 {
		return nil, domain.ErrForbidden
	}

	note.ID = id
	note.CreatedAt = existing.CreatedAt
	note.UpdatedAt = time.Now()
	note.EditCode = existing.EditCode

	if note.ExpiresAt == nil {
		note.ExpiresAt = existing.ExpiresAt
	}

	if note.ContentType == "" {
		note.ContentType = existing.ContentType
	}

	err = m.storage.Update(ctx, id, note)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	m.log.DebugContext(ctx, "note updated", "id", id)

	return note, nil
}

// Delete removes a note by ID after verifying the edit code.
func (m *Manager) Delete(ctx context.Context, id, editCode string) error {
	existing, err := m.storage.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}

	if subtle.ConstantTimeCompare([]byte(existing.EditCode), []byte(editCode)) != 1 {
		return domain.ErrForbidden
	}

	err = m.storage.Delete(ctx, id)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}

	return nil
}

// GetRendered fetches a note, increments its view counter, and returns it with content as safe HTML.
// Plain-text notes are HTML-escaped and wrapped in <pre>; markdown notes are rendered.
func (m *Manager) GetRendered(ctx context.Context, id string) (*domain.Note, string, error) {
	note, err := m.View(ctx, id)
	if err != nil {
		return nil, "", fmt.Errorf("get note for render: %w", err)
	}

	if note.ContentType == domain.ContentTypePlain {
		return note, "<pre>" + html.EscapeString(note.Content) + "</pre>", nil
	}

	rendered, err := m.renderer.Render(note.Content)
	if err != nil {
		return nil, "", fmt.Errorf("render note %s: %w", id, err)
	}

	return note, rendered, nil
}

func (m *Manager) validate(note *domain.Note) error {
	if note.Title == "" {
		return domain.ErrTitleRequired
	}

	if len([]rune(note.Title)) > maxTitleLength {
		return domain.ErrTitleTooLong
	}

	if len(note.Content) > maxContentLength {
		return domain.ErrContentTooLong
	}

	if note.ContentType != "" && !note.ContentType.Valid() {
		return domain.ErrInvalidContentType
	}

	return nil
}
