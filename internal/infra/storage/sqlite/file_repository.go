package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

// file is the database model for a published file.
type file struct {
	bun.BaseModel `bun:"table:files"`

	CreatedAt         time.Time  `bun:"created_at"`
	ExpiresAt         *time.Time `bun:"expires_at"`
	OwnerID           *uuid.UUID `bun:"owner_id"`
	ID                string     `bun:"id,pk"`
	ObjectKey         string     `bun:"object_key"`
	SealedMeta        string     `bun:"sealed_meta"`
	EditCode          string     `bun:"edit_code"`
	Privacy           string     `bun:"privacy"`
	Size              int64      `bun:"size"`
	Downloads         int        `bun:"downloads"`
	BurnAfterDownload bool       `bun:"burn_after_download"`
}

// FileRepository implements files.Storage using SQLite.
type FileRepository struct {
	db *bun.DB
}

// NewFileRepository returns a SQLite-backed FileRepository.
func NewFileRepository(db *bun.DB) *FileRepository {
	return &FileRepository{db: db}
}

func fileToDomain(row *file) *domain.File {
	return &domain.File{
		CreatedAt:         row.CreatedAt,
		ExpiresAt:         row.ExpiresAt,
		Privacy:           privacyPtr(row.Privacy),
		OwnerID:           row.OwnerID,
		ID:                row.ID,
		ObjectKey:         row.ObjectKey,
		SealedMeta:        row.SealedMeta,
		Name:              "",
		ContentKey:        nil,
		EditCode:          row.EditCode,
		Size:              row.Size,
		Downloads:         row.Downloads,
		BurnAfterDownload: row.BurnAfterDownload,
	}
}

// visibleWhere is the SQL form of domain.File.VisibleTo. Storage checks it again in each
// write, so a concurrent privacy change cannot let an old read change the record.
const visibleWhere = "privacy = ? OR (privacy = ? AND ?) OR (privacy = ? AND owner_id = ? AND ?)"

func visibleArgs(callerID uuid.UUID, authenticated bool) []any {
	return []any{
		domain.PrivacyPublic, domain.PrivacyAuthenticated, authenticated,
		domain.PrivacyOwner, callerID, callerID != uuid.Nil,
	}
}

// Create inserts a file record. A slug that another note, link or file has gives
// domain.ErrSlugConflict.
func (r *FileRepository) Create(ctx context.Context, domFile *domain.File) error {
	row := &file{
		BaseModel:         bun.BaseModel{},
		CreatedAt:         domFile.CreatedAt,
		ExpiresAt:         domFile.ExpiresAt,
		OwnerID:           domFile.OwnerID,
		ID:                domFile.ID,
		ObjectKey:         domFile.ObjectKey,
		SealedMeta:        domFile.SealedMeta,
		EditCode:          domFile.EditCode,
		Privacy:           privacyVal(domFile.Privacy),
		Size:              domFile.Size,
		Downloads:         domFile.Downloads,
		BurnAfterDownload: domFile.BurnAfterDownload,
	}

	_, err := r.db.NewInsert().Model(row).Exec(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrSlugConflict
		}

		return fmt.Errorf("sqlite create file: %w", err)
	}

	return nil
}

// GetByID returns the file record with the ID HashSlug(slug).
func (r *FileRepository) GetByID(ctx context.Context, id string) (*domain.File, error) {
	var row file

	err := r.db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("sqlite get file: %w", err)
	}

	return fileToDomain(&row), nil
}

// Exists reports whether a file record has the ID.
func (r *FileRepository) Exists(ctx context.Context, id string) (bool, error) {
	exists, err := r.db.NewSelect().Model((*file)(nil)).Where("id = ?", id).Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("sqlite file exists: %w", err)
	}

	return exists, nil
}

// Delete removes the file record and returns it.
func (r *FileRepository) Delete(ctx context.Context, id string) (*domain.File, error) {
	var row file

	err := r.db.NewDelete().Model(&row).Where("id = ?", id).Returning("*").Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("sqlite delete file: %w", err)
	}

	return fileToDomain(&row), nil
}

// Update writes the editable fields of the record: expiry, privacy and burn after download.
// It does not change the content, the sealed metadata, the object key or the download counter.
func (r *FileRepository) Update(ctx context.Context, domFile *domain.File) error {
	result, err := r.db.NewUpdate().TableExpr("files").
		Set("expires_at = ?", domFile.ExpiresAt).
		Set("privacy = ?", privacyVal(domFile.Privacy)).
		Set("burn_after_download = ?", domFile.BurnAfterDownload).
		Where("id = ?", domFile.ID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("sqlite update file: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite file rowsaffected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// ConsumeVisible deletes and returns the record when it is visible to the reader and it is
// expired, or it is a burn file and allowBurn is true.
func (r *FileRepository) ConsumeVisible(
	ctx context.Context, id string, callerID uuid.UUID, authenticated, allowBurn bool,
) (*domain.File, error) {
	var row file

	err := r.db.NewDelete().Model(&row).Where("id = ?", id).
		Where("(expires_at IS NOT NULL AND expires_at <= ?) OR (burn_after_download AND ?)", time.Now(), allowBurn).
		Where(visibleWhere, visibleArgs(callerID, authenticated)...).
		Returning("*").Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("sqlite consume file: %w", err)
	}

	return fileToDomain(&row), nil
}

// IncrementDownloadsVisible counts a download while the record is visible to the reader.
func (r *FileRepository) IncrementDownloadsVisible(
	ctx context.Context, id string, callerID uuid.UUID, authenticated bool,
) error {
	result, err := r.db.NewUpdate().TableExpr("files").Set("downloads = downloads + 1").Where("id = ?", id).
		Where(visibleWhere, visibleArgs(callerID, authenticated)...).Exec(ctx)
	if err != nil {
		return fmt.Errorf("sqlite count file download: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite file rowsaffected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// DeleteExpired removes the records that expired before now and returns their object keys.
func (r *FileRepository) DeleteExpired(ctx context.Context, now time.Time) ([]string, error) {
	var keys []string

	err := r.db.NewDelete().Model((*file)(nil)).
		Where("expires_at IS NOT NULL AND expires_at < ?", now).
		Returning("object_key").Scan(ctx, &keys)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlite delete expired files: %w", err)
	}

	return keys, nil
}
