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
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// file is the database model for a published file.
type file struct {
	bun.BaseModel `bun:"table:files"`

	CreatedAt         time.Time  `bun:"created_at"`
	ExpiresAt         *time.Time `bun:"expires_at"`
	OwnerID           *uuid.UUID `bun:"owner_id"`
	BurnClaim         *string    `bun:"burn_claim"`
	BurnClaimUntil    *time.Time `bun:"burn_claim_until"`
	ID                string     `bun:"id,pk"`
	ObjectKey         string     `bun:"object_key"`
	SealedMeta        string     `bun:"sealed_meta"`
	EditCode          string     `bun:"edit_code"`
	Privacy           string     `bun:"privacy"`
	Size              int64      `bun:"size"`
	Downloads         int        `bun:"downloads"`
	BurnAfterDownload bool       `bun:"burn_after_download"`
}

// FileRepository implements files.Storage using PostgreSQL.
type FileRepository struct {
	db *bun.DB
}

// NewFileRepository returns a PostgreSQL-backed FileRepository.
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

const (
	// visibleWhere is the SQL form of domain.File.VisibleTo. Storage checks it again in each
	// write, so a concurrent privacy change cannot let an old read change the record. The
	// first parameter is AccessOptions.SkipPrivacy.
	visibleWhere = "? OR privacy = ? OR (privacy = ? AND ?) OR (privacy = ? AND owner_id = ? AND ?)"
	// liveWhere selects a record that is not expired at the time parameter.
	liveWhere = "expires_at IS NULL OR expires_at >= ?"
	// unclaimedWhere selects a record without a claim, or with a stale claim, at the time
	// parameter.
	unclaimedWhere = "burn_claim_until IS NULL OR burn_claim_until <= ?"
)

func visibleArgs(reader files.AccessOptions) []any {
	return []any{
		reader.SkipPrivacy,
		domain.PrivacyPublic, domain.PrivacyAuthenticated, reader.Authenticated,
		domain.PrivacyOwner, reader.CallerID, reader.CallerID != uuid.Nil,
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
		BurnClaim:         nil,
		BurnClaimUntil:    nil,
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
		var pgErr pgdriver.Error
		if errors.As(err, &pgErr) && pgErr.Field('C') == "23505" {
			return domain.ErrSlugConflict
		}

		return fmt.Errorf("postgres create file: %w", err)
	}

	return nil
}

// GetByID returns the file record with the ID HashSlug(slug). A record that a burn download
// claims is not found until the claim ends or becomes stale.
func (r *FileRepository) GetByID(ctx context.Context, id string) (*domain.File, error) {
	var row file

	err := r.db.NewSelect().Model(&row).Where("id = ?", id).Where(unclaimedWhere, time.Now()).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("postgres get file: %w", err)
	}

	return fileToDomain(&row), nil
}

// Exists reports whether a file record has the ID. A claimed record also counts.
func (r *FileRepository) Exists(ctx context.Context, id string) (bool, error) {
	exists, err := r.db.NewSelect().Model((*file)(nil)).Where("id = ?", id).Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("postgres file exists: %w", err)
	}

	return exists, nil
}

// Delete removes the file record with id and objectKey.
func (r *FileRepository) Delete(ctx context.Context, id, objectKey string) error {
	result, err := r.db.NewDelete().Model((*file)(nil)).
		Where("id = ?", id).Where("object_key = ?", objectKey).Exec(ctx)

	return oneFileRow("postgres delete file", result, err)
}

// Update writes the fields that opts sets on the record with id and objectKey, while the
// record is not expired at now. It does not change the content, the sealed metadata or the
// download counter.
func (r *FileRepository) Update(
	ctx context.Context, id, objectKey string, opts files.UpdateOptions, now time.Time,
) error {
	query := r.db.NewUpdate().TableExpr("files").
		Where("id = ?", id).Where("object_key = ?", objectKey).Where(liveWhere, now)

	changes := fileChanges(&opts)
	for i := range changes {
		query = query.Set(changes[i].expr, changes[i].args...)
	}

	result, err := query.Exec(ctx)

	return oneFileRow("postgres update file", result, err)
}

// ClaimBurn marks a visible, live burn record without a live claim as claimed by claim.Token.
// The UPDATE locks the row and checks the conditions again after the lock, so of parallel
// claims only one changes the row.
func (r *FileRepository) ClaimBurn(ctx context.Context, claim *files.BurnClaim) error {
	result, err := r.db.NewUpdate().TableExpr("files").
		Set("burn_claim = ?", claim.Token).
		Set("burn_claim_until = ?", claim.Until).
		Where("id = ?", claim.ID).Where("object_key = ?", claim.ObjectKey).
		Where("burn_after_download").
		Where(liveWhere, claim.Now).
		Where(unclaimedWhere, claim.Now).
		Where(visibleWhere, visibleArgs(claim.Reader)...).
		Exec(ctx)

	return oneFileRow("postgres claim burn file", result, err)
}

// ReleaseBurn removes the claim from the record.
func (r *FileRepository) ReleaseBurn(ctx context.Context, claim *files.BurnClaim) error {
	result, err := r.db.NewUpdate().TableExpr("files").
		Set("burn_claim = NULL").Set("burn_claim_until = NULL").
		Where("id = ?", claim.ID).Where("object_key = ?", claim.ObjectKey).
		Where("burn_claim = ?", claim.Token).Exec(ctx)

	return oneFileRow("postgres release burn file", result, err)
}

// IncrementDownloadsVisible counts a download while the record is visible to the reader, is
// not a burn file and is not expired.
func (r *FileRepository) IncrementDownloadsVisible(
	ctx context.Context, id, objectKey string, reader files.AccessOptions,
) error {
	result, err := r.db.NewUpdate().TableExpr("files").Set("downloads = downloads + 1").
		Where("id = ?", id).Where("object_key = ?", objectKey).
		Where("NOT burn_after_download").
		Where(liveWhere, time.Now()).
		Where(visibleWhere, visibleArgs(reader)...).Exec(ctx)

	return oneFileRow("postgres count file download", result, err)
}

// ListExpired returns at most limit records that expired before now and have no live claim,
// the oldest expiry first, then by ID. It returns only the records after the cursor: the
// bound on expires_at lets the database use the index, and the second condition breaks ties
// of equal expiry by ID. ID, ObjectKey and ExpiresAt are set.
func (r *FileRepository) ListExpired(
	ctx context.Context, now time.Time, after files.ExpiredCursor, limit int,
) ([]*domain.File, error) {
	var rows []file

	query := r.db.NewSelect().Model(&rows).Column("id", "object_key", "expires_at").
		Where("expires_at IS NOT NULL AND expires_at < ?", now).
		Where(unclaimedWhere, now)

	if !after.IsZero() {
		query = query.Where("expires_at >= ?", after.ExpiresAt).
			Where("expires_at > ? OR id > ?", after.ExpiresAt, after.ID)
	}

	err := query.OrderExpr("expires_at, id").Limit(int64(limit)).Scan(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("postgres list expired files: %w", err)
	}

	expired := make([]*domain.File, 0, len(rows))
	for i := range rows {
		expired = append(expired, fileToDomain(&rows[i]))
	}

	return expired, nil
}

// DeleteExpired removes the record with id and objectKey while it is expired at now and has
// no live claim.
func (r *FileRepository) DeleteExpired(ctx context.Context, id, objectKey string, now time.Time) error {
	result, err := r.db.NewDelete().Model((*file)(nil)).
		Where("id = ?", id).Where("object_key = ?", objectKey).
		Where("expires_at IS NOT NULL AND expires_at < ?", now).
		Where(unclaimedWhere, now).Exec(ctx)

	return oneFileRow("postgres delete expired file", result, err)
}

// columnChange is one SET clause of an UPDATE.
type columnChange struct {
	expr string
	args []any
}

// fileChanges returns a SET clause for each field that opts sets.
func fileChanges(opts *files.UpdateOptions) []columnChange {
	var changes []columnChange

	if opts.ClearExpiry {
		changes = append(changes, columnChange{expr: "expires_at = NULL", args: nil})
	} else if opts.ExpiresAt != nil {
		changes = append(changes, columnChange{expr: "expires_at = ?", args: []any{*opts.ExpiresAt}})
	}

	if opts.Privacy != nil {
		changes = append(changes, columnChange{expr: "privacy = ?", args: []any{privacyVal(opts.Privacy)}})
	}

	if opts.BurnAfterDownload != nil {
		changes = append(changes, columnChange{expr: "burn_after_download = ?", args: []any{*opts.BurnAfterDownload}})
	}

	if len(changes) == 0 {
		// An UPDATE needs a SET clause. This one changes nothing and still finds the row.
		changes = append(changes, columnChange{expr: "downloads = downloads", args: nil})
	}

	return changes
}

// oneFileRow returns domain.ErrNotFound when the statement changed no row.
func oneFileRow(operation string, result sql.Result, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}

	affected, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("%s: rows affected: %w", operation, rowsErr)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}
