package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

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
	BurnTTL          int64      `bun:"burn_ttl"`
	BurnAfterReading bool       `bun:"burn_after_reading"`
	Private          bool       `bun:"private"`
}

// Repository implements notes.Storage using SQLite.
type Repository struct {
	db *bun.DB
}

// NewRepository creates a new SQLite-backed Repository.
func NewRepository(db *bun.DB) *Repository {
	return &Repository{db: db}
}

func toDomain(dbNote *note) *domain.Note {
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
		BurnTTL:          dbNote.BurnTTL,
		BurnAfterReading: dbNote.BurnAfterReading,
		Private:          dbNote.Private,
	}
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
		BurnTTL:          domNote.BurnTTL,
		BurnAfterReading: domNote.BurnAfterReading,
		Private:          domNote.Private,
	}

	_, err := r.db.NewInsert().Model(dbNote).Exec(ctx)
	if err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) &&
			(sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE ||
				sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
			return domain.ErrSlugConflict
		}

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

	return toDomain(&dbNote), nil
}

// Consume atomically deletes a note and returns it, but only if the note is eligible for deletion:
// burn_after_reading is set (with no grace period), or expires_at has passed.
// Returns domain.ErrNotFound otherwise.
func (r *Repository) Consume(ctx context.Context, id string) (*domain.Note, error) {
	var dbNote note

	err := r.db.NewDelete().
		Model(&dbNote).
		Where("id = ?", id).
		Where("burn_after_reading OR (expires_at IS NOT NULL AND expires_at <= ?)", time.Now()).
		Returning("*").
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("sqlite consume: %w", err)
	}

	return toDomain(&dbNote), nil
}

// SetBurnExpiry atomically sets expires_at and clears burn_after_reading on first read
// for notes with a burn grace period. Returns domain.ErrNotFound if no eligible row is found.
func (r *Repository) SetBurnExpiry(ctx context.Context, id string, expiresAt time.Time) (*domain.Note, error) {
	var dbNote note

	err := r.db.NewUpdate().
		Model(&dbNote).
		Set("expires_at = ?", expiresAt).
		Set("burn_after_reading = 0").
		Where("id = ?", id).
		Where("burn_after_reading").
		Returning("*").
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("sqlite set burn expiry: %w", err)
	}

	return toDomain(&dbNote), nil
}

// Update modifies an existing note.
func (r *Repository) Update(ctx context.Context, id string, domNote *domain.Note) error {
	dbNote := &note{
		ID:               id,
		Title:            domNote.Title,
		Content:          domNote.Content,
		ContentType:      string(domNote.ContentType),
		ExpiresAt:        domNote.ExpiresAt,
		BurnTTL:          domNote.BurnTTL,
		BurnAfterReading: domNote.BurnAfterReading,
		Private:          domNote.Private,
		CreatedAt:        domNote.CreatedAt,
		UpdatedAt:        domNote.UpdatedAt,
	}

	result, err := r.db.NewUpdate().Model(dbNote).
		Column("title", "content", "content_type", "expires_at", "burn_ttl", "burn_after_reading", "private", "updated_at").
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

// IncrementViews atomically increments the view counter for a note.
func (r *Repository) IncrementViews(ctx context.Context, id string) error {
	result, err := r.db.NewUpdate().
		TableExpr("notes").
		Set("views = views + 1").
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("sqlite increment views: %w", err)
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
