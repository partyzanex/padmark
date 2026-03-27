package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

// note is the database model for a note.
type note struct {
	bun.BaseModel `bun:"table:notes"`

	CreatedAt   time.Time `bun:"created_at"`
	UpdatedAt   time.Time `bun:"updated_at"`
	ID          string    `bun:"id,pk"`
	Title       string    `bun:"title"`
	Content     string    `bun:"content"`
	ContentType string    `bun:"content_type"`
}

// Repository implements notes.Storage using SQLite.
type Repository struct {
	db *bun.DB
}

// NewRepository creates a new SQLite-backed Repository.
func NewRepository(db *bun.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a new note.
func (r *Repository) Create(ctx context.Context, domNote *domain.Note) error {
	dbNote := &note{
		ID:          domNote.ID,
		Title:       domNote.Title,
		Content:     domNote.Content,
		ContentType: string(domNote.ContentType),
		CreatedAt:   domNote.CreatedAt,
		UpdatedAt:   domNote.UpdatedAt,
	}

	_, err := r.db.NewInsert().Model(dbNote).Exec(ctx)
	if err != nil {
		return fmt.Errorf("sqlite create: %w", err)
	}

	return nil
}

// Get retrieves a note by ID.
func (r *Repository) Get(ctx context.Context, id string) (*domain.Note, error) {
	var dbNote note

	err := r.db.NewSelect().Model(&dbNote).Where("id = ?", id).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("sqlite get: %w", err)
	}

	return &domain.Note{
		ID:          dbNote.ID,
		CreatedAt:   dbNote.CreatedAt,
		UpdatedAt:   dbNote.UpdatedAt,
		Title:       dbNote.Title,
		Content:     dbNote.Content,
		ContentType: domain.ContentType(dbNote.ContentType),
	}, nil
}

// Update modifies an existing note.
func (r *Repository) Update(ctx context.Context, id string, domNote *domain.Note) error {
	dbNote := &note{
		ID:          id,
		Title:       domNote.Title,
		Content:     domNote.Content,
		ContentType: string(domNote.ContentType),
		CreatedAt:   domNote.CreatedAt,
		UpdatedAt:   domNote.UpdatedAt,
	}

	result, err := r.db.NewUpdate().Model(dbNote).
		Column("title", "content", "content_type", "updated_at").
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("sqlite update: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite rowsaffected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// Delete removes a note by ID.
func (r *Repository) Delete(ctx context.Context, id string) error {
	result, err := r.db.NewDelete().Model((*note)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("sqlite delete: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite rowsaffected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}
