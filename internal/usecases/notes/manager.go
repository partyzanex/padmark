//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=notes

package notes

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

const maxContentLength = 100_000

// Storage defines persistence operations for notes.
type Storage interface {
	Create(ctx context.Context, note *domain.Note) error
	List(ctx context.Context, limit, offset int) ([]domain.Note, int, error)
	Get(ctx context.Context, id int64) (*domain.Note, error)
	Update(ctx context.Context, id int64, note *domain.Note) error
	Delete(ctx context.Context, id int64) error
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

// List returns a paginated list of notes and total count.
func (m *Manager) List(ctx context.Context, limit, offset int) ([]domain.Note, int, error) {
	notes, total, err := m.storage.List(ctx, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list notes: %w", err)
	}

	return notes, total, nil
}

// Get returns a note by ID.
func (m *Manager) Get(ctx context.Context, id int64) (*domain.Note, error) {
	note, err := m.storage.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get note: %w", err)
	}

	return note, nil
}

// Update validates and updates an existing note.
func (m *Manager) Update(ctx context.Context, id int64, note *domain.Note) (*domain.Note, error) {
	err := m.validate(note)
	if err != nil {
		return nil, err
	}

	note.UpdatedAt = time.Now()

	err = m.storage.Update(ctx, id, note)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	note.ID = id
	m.log.DebugContext(ctx, "note updated", "id", id)

	return note, nil
}

// Delete removes a note by ID.
func (m *Manager) Delete(ctx context.Context, id int64) error {
	err := m.storage.Delete(ctx, id)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}

	return nil
}

// GetRendered fetches a note and returns its content rendered as HTML.
func (m *Manager) GetRendered(ctx context.Context, id int64) (string, error) {
	note, err := m.storage.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get note for render: %w", err)
	}

	html, err := m.renderer.Render(note.Content)
	if err != nil {
		return "", fmt.Errorf("render note %d: %w", id, err)
	}

	return html, nil
}

func (m *Manager) validate(note *domain.Note) error {
	if note.Title == "" {
		return domain.ErrTitleRequired
	}

	if len(note.Content) > maxContentLength {
		return domain.ErrContentTooLong
	}

	return nil
}
