//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=notes

package notes

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

var slugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}$`)

const maxContentLength = 100_000

// Storage defines persistence operations for notes.
type Storage interface {
	Create(ctx context.Context, note *domain.Note) error
	Get(ctx context.Context, id string) (*domain.Note, error)
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
	err := m.validate(note)
	if err != nil {
		return nil, err
	}

	if note.ID != "" {
		if !slugRe.MatchString(note.ID) {
			return nil, domain.ErrInvalidSlug
		}
	} else {
		note.ID = uuid.NewString()
	}

	now := time.Now()
	note.CreatedAt = now
	note.UpdatedAt = now

	if note.ContentType == "" {
		note.ContentType = domain.ContentTypeMarkdown
	}

	err = m.storage.Create(ctx, note)
	if err != nil {
		return nil, fmt.Errorf("create note: %w", err)
	}

	m.log.DebugContext(ctx, "note created", "id", note.ID)

	return note, nil
}

// Get returns a note by ID. If the note has expired it is deleted and ErrExpired is returned.
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
		delErr := m.storage.Delete(ctx, id)
		if delErr != nil {
			m.log.ErrorContext(ctx, "burn after reading: delete note", "id", id, "err", delErr)
		} else {
			m.log.DebugContext(ctx, "note burned", "id", id)
		}
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

	if !note.BurnAfterReading {
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
func (m *Manager) Update(ctx context.Context, id string, note *domain.Note) (*domain.Note, error) {
	err := m.validate(note)
	if err != nil {
		return nil, err
	}

	existing, err := m.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	note.ID = id
	note.CreatedAt = existing.CreatedAt
	note.UpdatedAt = time.Now()
	note.ExpiresAt = existing.ExpiresAt
	note.BurnAfterReading = existing.BurnAfterReading

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

// Delete removes a note by ID.
func (m *Manager) Delete(ctx context.Context, id string) error {
	err := m.storage.Delete(ctx, id)
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

	if len(note.Content) > maxContentLength {
		return domain.ErrContentTooLong
	}

	if note.ContentType != "" && !note.ContentType.Valid() {
		return domain.ErrInvalidContentType
	}

	return nil
}
