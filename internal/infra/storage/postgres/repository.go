package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

// note is the database model for a note.
type note struct {
	bun.BaseModel `bun:"table:notes"`

	CreatedAt        time.Time  `bun:"created_at"`
	UpdatedAt        time.Time  `bun:"updated_at"`
	ExpiresAt        *time.Time `bun:"expires_at"`
	ID               string     `bun:"id,pk"`
	Title            string     `bun:"title"`
	Content          string     `bun:"content"`
	ContentType      string     `bun:"content_type"`
	EditCode         string     `bun:"edit_code"`
	Views            int        `bun:"views"`
	BurnAfterReading bool       `bun:"burn_after_reading"`
}

// Repository implements notes.Storage using PostgreSQL.
type Repository struct {
	db *bun.DB
}

// NewRepository creates a new PostgreSQL-backed Repository.
func NewRepository(db *bun.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a new note.
func (r *Repository) Create(ctx context.Context, domNote *domain.Note) error {
	dbNote := &note{
		ID:               domNote.ID,
		Title:            domNote.Title,
		Content:          domNote.Content,
		ContentType:      string(domNote.ContentType),
		EditCode:         domNote.EditCode,
		CreatedAt:        domNote.CreatedAt,
		UpdatedAt:        domNote.UpdatedAt,
		ExpiresAt:        domNote.ExpiresAt,
		BurnAfterReading: domNote.BurnAfterReading,
	}

	_, err := r.db.NewInsert().Model(dbNote).Exec(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return domain.ErrSlugConflict
		}

		return fmt.Errorf("postgres create: %w", err)
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

		return nil, fmt.Errorf("postgres get: %w", err)
	}

	return &domain.Note{
		ID:               dbNote.ID,
		CreatedAt:        dbNote.CreatedAt,
		UpdatedAt:        dbNote.UpdatedAt,
		ExpiresAt:        dbNote.ExpiresAt,
		Title:            dbNote.Title,
		Content:          dbNote.Content,
		ContentType:      domain.ContentType(dbNote.ContentType),
		EditCode:         dbNote.EditCode,
		Views:            dbNote.Views,
		BurnAfterReading: dbNote.BurnAfterReading,
	}, nil
}

// Update modifies an existing note.
func (r *Repository) Update(ctx context.Context, id string, domNote *domain.Note) error {
	dbNote := &note{
		ID:               id,
		Title:            domNote.Title,
		Content:          domNote.Content,
		ContentType:      string(domNote.ContentType),
		ExpiresAt:        domNote.ExpiresAt,
		BurnAfterReading: domNote.BurnAfterReading,
		CreatedAt:        domNote.CreatedAt,
		UpdatedAt:        domNote.UpdatedAt,
	}

	result, err := r.db.NewUpdate().Model(dbNote).
		Column("title", "content", "content_type", "expires_at", "burn_after_reading", "updated_at").
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres update: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres rowsaffected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// IncrementViews atomically increments the view counter for a note.
func (r *Repository) IncrementViews(ctx context.Context, id string) error {
	result, err := r.db.NewUpdate().
		TableExpr("notes").
		Set("views = views + 1").
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres increment views: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres rowsaffected: %w", err)
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
		return fmt.Errorf("postgres delete: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres rowsaffected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}
