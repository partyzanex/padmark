package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
)

// note is the database model for a note.
type note struct {
	bun.BaseModel `bun:"table:notes"`

	CreatedAt        time.Time  `bun:"created_at"`
	UpdatedAt        time.Time  `bun:"updated_at"`
	ExpiresAt        *time.Time `bun:"expires_at"`
	OwnerID          *uuid.UUID `bun:"owner_id"`
	Title            string     `bun:"title"`
	Content          string     `bun:"content"`
	ContentType      string     `bun:"content_type"`
	EditCode         string     `bun:"edit_code"`
	ID               string     `bun:"id,pk"`
	Privacy          string     `bun:"privacy"`
	Views            int        `bun:"views"`
	BurnTTL          int64      `bun:"burn_ttl"`
	BurnAfterReading bool       `bun:"burn_after_reading"`
}

// NoteRepository implements notes.Storage using PostgreSQL.
type NoteRepository struct {
	db *bun.DB
}

// NewNoteRepository creates a new PostgreSQL-backed Repository.
func NewNoteRepository(db *bun.DB) *NoteRepository {
	return &NoteRepository{db: db}
}

func toDomain(dbNote *note) *domain.Note {
	return &domain.Note{
		ID:               dbNote.ID,
		CreatedAt:        dbNote.CreatedAt,
		UpdatedAt:        dbNote.UpdatedAt,
		ExpiresAt:        dbNote.ExpiresAt,
		Title:            dbNote.Title,
		Content:          dbNote.Content,
		ContentType:      contentTypePtr(dbNote.ContentType),
		EditCode:         dbNote.EditCode,
		Views:            dbNote.Views,
		BurnTTL:          dbNote.BurnTTL,
		BurnAfterReading: dbNote.BurnAfterReading,
		Privacy:          privacyPtr(dbNote.Privacy),
		OwnerID:          dbNote.OwnerID,
	}
}

func privacyPtr(s string) *domain.Privacy {
	p := domain.Privacy(s)

	return &p
}

func privacyVal(p *domain.Privacy) string {
	if p == nil {
		return string(domain.PrivacyPublic)
	}

	return string(*p)
}

func contentTypePtr(s string) *domain.ContentType {
	ct := domain.ContentType(s)

	return &ct
}

func contentTypeVal(ct *domain.ContentType) string {
	if ct == nil {
		return ""
	}

	return string(*ct)
}

// Create inserts a new note.
func (r *NoteRepository) Create(ctx context.Context, domNote *domain.Note) error {
	dbNote := &note{
		ID:               domNote.ID,
		Title:            domNote.Title,
		Content:          domNote.Content,
		ContentType:      contentTypeVal(domNote.ContentType),
		EditCode:         domNote.EditCode,
		CreatedAt:        domNote.CreatedAt,
		UpdatedAt:        domNote.UpdatedAt,
		ExpiresAt:        domNote.ExpiresAt,
		BurnTTL:          domNote.BurnTTL,
		BurnAfterReading: domNote.BurnAfterReading,
		Privacy:          privacyVal(domNote.Privacy),
		OwnerID:          domNote.OwnerID,
	}

	_, err := r.db.NewInsert().Model(dbNote).Exec(ctx)
	if err != nil {
		var pgErr pgdriver.Error
		if errors.As(err, &pgErr) && pgErr.Field('C') == "23505" {
			return domain.ErrSlugConflict
		}

		return fmt.Errorf("postgres create: %w", err)
	}

	return nil
}

// Get retrieves a note by ID.
func (r *NoteRepository) Get(ctx context.Context, id string) (*domain.Note, error) {
	var dbNote note

	err := r.db.NewSelect().Model(&dbNote).Where("id = ?", id).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("postgres get: %w", err)
	}

	return toDomain(&dbNote), nil
}

// Consume atomically deletes a note and returns it, but only if the note is eligible for deletion:
// burn_after_reading is set (with no grace period), or expires_at has passed.
// Returns domain.ErrNotFound otherwise.
func (r *NoteRepository) Consume(ctx context.Context, id string) (*domain.Note, error) {
	var dbNote note

	err := r.db.NewDelete().
		Model(&dbNote).
		Where("id = ?", id).
		Where("burn_after_reading OR (expires_at IS NOT NULL AND expires_at <= ?)", time.Now()).
		// Explicit column list, not "*": the notes table still has the deprecated `private`
		// column (see migrations/postgres/20260720000001_notes_privacy.sql), which the note
		// struct no longer maps — RETURNING * would try to scan it and fail.
		Returning("id, created_at, updated_at, expires_at, owner_id, content, title, " +
			"content_type, edit_code, views, burn_ttl, burn_after_reading, privacy").
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("postgres consume: %w", err)
	}

	return toDomain(&dbNote), nil
}

// SetBurnExpiry atomically sets expires_at and clears burn_after_reading on first read
// for notes with a burn grace period. Returns domain.ErrNotFound if no eligible row is found.
func (r *NoteRepository) SetBurnExpiry(ctx context.Context, id string, expiresAt time.Time) (*domain.Note, error) {
	var dbNote note

	err := r.db.NewUpdate().
		Model(&dbNote).
		Set("expires_at = ?", expiresAt).
		Set("burn_after_reading = FALSE").
		Where("id = ?", id).
		Where("burn_after_reading").
		// Explicit column list, not "*": the notes table still has the deprecated `private`
		// column (see migrations/postgres/20260720000001_notes_privacy.sql), which the note
		// struct no longer maps — RETURNING * would try to scan it and fail.
		Returning("id, created_at, updated_at, expires_at, owner_id, content, title, " +
			"content_type, edit_code, views, burn_ttl, burn_after_reading, privacy").
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("postgres set burn expiry: %w", err)
	}

	return toDomain(&dbNote), nil
}

// Update modifies an existing note.
func (r *NoteRepository) Update(ctx context.Context, id string, domNote *domain.Note) error {
	dbNote := &note{
		ID:               id,
		Title:            domNote.Title,
		Content:          domNote.Content,
		ExpiresAt:        domNote.ExpiresAt,
		BurnTTL:          domNote.BurnTTL,
		BurnAfterReading: domNote.BurnAfterReading,
		CreatedAt:        domNote.CreatedAt,
		UpdatedAt:        domNote.UpdatedAt,
	}

	var ctVal *string

	if domNote.ContentType != nil {
		sv := string(*domNote.ContentType)
		ctVal = &sv
	}

	// Named pVal, not privacyVal, to avoid shadowing the package-level privacyVal helper above.
	var pVal *string

	if domNote.Privacy != nil {
		sv := string(*domNote.Privacy)
		pVal = &sv
	}

	result, err := r.db.NewUpdate().Model(dbNote).
		Column("title", "content", "expires_at", "burn_ttl", "burn_after_reading", "updated_at").
		Set("content_type = COALESCE(?, content_type)", ctVal).
		Set("privacy = COALESCE(?, privacy)", pVal).
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
func (r *NoteRepository) IncrementViews(ctx context.Context, id string) error {
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
func (r *NoteRepository) Delete(ctx context.Context, id string) error {
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
