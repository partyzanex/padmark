package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
)

// link is the database model for a short link.
type link struct {
	bun.BaseModel `bun:"table:links"`

	CreatedAt      time.Time  `bun:"created_at"`
	ExpiresAt      *time.Time `bun:"expires_at"`
	OwnerID        *uuid.UUID `bun:"owner_id"`
	DirectRedirect *bool      `bun:"direct_redirect"`
	TargetURL      string     `bun:"target_url"`
	EditCode       string     `bun:"edit_code"`
	ID             string     `bun:"id,pk"`
	Privacy        string     `bun:"privacy"`
	Views          int        `bun:"views"`
	BurnAfterClick bool       `bun:"burn_after_click"`
}

// LinkRepository implements links.Storage using PostgreSQL.
type LinkRepository struct {
	db  *bun.DB
	log *slog.Logger
}

// NewLinkRepository creates a new PostgreSQL-backed LinkRepository. log is used to surface
// lazy-sweep failures.
func NewLinkRepository(db *bun.DB, log *slog.Logger) *LinkRepository {
	return &LinkRepository{db: db, log: log}
}

// linkToDomain converts a db link row to a domain.Link. privacyPtr is shared with
// note_repository.go (same package).
func linkToDomain(dbLink *link) *domain.Link {
	return &domain.Link{
		ID:             dbLink.ID,
		TargetURL:      dbLink.TargetURL,
		CreatedAt:      dbLink.CreatedAt,
		ExpiresAt:      dbLink.ExpiresAt,
		OwnerID:        dbLink.OwnerID,
		DirectRedirect: dbLink.DirectRedirect,
		Privacy:        privacyPtr(dbLink.Privacy),
		EditCode:       dbLink.EditCode,
		Views:          dbLink.Views,
		BurnAfterClick: dbLink.BurnAfterClick,
	}
}

// Create inserts a new link. Expired links are lazily swept on each call (mirroring the
// session/invite/reveal-token pattern — see session_repository.go) since there is no background
// sweep worker in this codebase. This is the "nobody ever revisits it" gap Redirect's own
// delete-on-read (links.Manager.Redirect) can't close on its own: a link that expires and is
// never followed again would otherwise linger in the table forever.
func (r *LinkRepository) Create(ctx context.Context, domLink *domain.Link) error {
	dbLink := &link{
		BaseModel:      bun.BaseModel{},
		ID:             domLink.ID,
		TargetURL:      domLink.TargetURL,
		CreatedAt:      domLink.CreatedAt,
		ExpiresAt:      domLink.ExpiresAt,
		OwnerID:        domLink.OwnerID,
		DirectRedirect: domLink.DirectRedirect,
		EditCode:       domLink.EditCode,
		Views:          domLink.Views,
		BurnAfterClick: domLink.BurnAfterClick,
		Privacy:        privacyVal(domLink.Privacy),
	}

	_, err := r.db.NewInsert().Model(dbLink).Exec(ctx)
	if err != nil {
		var pgErr pgdriver.Error
		if errors.As(err, &pgErr) && pgErr.Field('C') == "23505" {
			return domain.ErrSlugConflict
		}

		return fmt.Errorf("postgres create link: %w", err)
	}

	// id != ? excludes the row just inserted above: a caller-supplied ExpiresAt in the past (e.g.
	// ttl=0, matching notes' "immediate" semantics) must not vanish before Create even returns —
	// the sweep is for cleaning up old abandoned links, not undoing this call's own insert.
	// Best-effort sweep of stale links; a failure here does not block link creation —
	// stale rows simply persist until the next Create call.
	_, sweepErr := r.db.NewDelete().
		TableExpr("links").
		Where("id != ? AND expires_at IS NOT NULL AND expires_at < ?", dbLink.ID, time.Now()).
		Exec(ctx)
	if sweepErr != nil {
		r.log.WarnContext(ctx, "link sweep failed", "err", sweepErr)
	}

	return nil
}

// Update changes only editable fields. RETURNING reports the persisted counters and
// immutable fields without overwriting clicks recorded since the caller's read.
func (r *LinkRepository) Update(ctx context.Context, id string, domLink *domain.Link) (*domain.Link, error) {
	var dbLink link

	err := r.db.NewUpdate().
		Model(&dbLink).
		Set("target_url = ?", domLink.TargetURL).
		Set("expires_at = ?", domLink.ExpiresAt).
		Set("privacy = ?", privacyVal(domLink.Privacy)).
		Set("direct_redirect = ?", domLink.DirectRedirect).
		Where("id = ?", id).
		Returning("*").
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("postgres update link: %w", err)
	}

	return linkToDomain(&dbLink), nil
}

// GetByID retrieves a link by ID (HashSlug(slug)).
func (r *LinkRepository) GetByID(ctx context.Context, id string) (*domain.Link, error) {
	var dbLink link

	err := r.db.NewSelect().Model(&dbLink).Where("id = ?", id).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("postgres get link: %w", err)
	}

	return linkToDomain(&dbLink), nil
}

// Exists reports whether a link with the given ID exists — used for the cross-table slug
// uniqueness check shared with notes (see links.Manager.Create / notes.Manager.Create).
func (r *LinkRepository) Exists(ctx context.Context, id string) (bool, error) {
	exists, err := r.db.NewSelect().Model((*link)(nil)).Where("id = ?", id).Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("postgres link exists: %w", err)
	}

	return exists, nil
}

// Consume atomically deletes a link and returns it, but only if the link is eligible for
// deletion: burn_after_click is set, or expires_at has passed. Returns domain.ErrNotFound
// otherwise. Mirrors note_repository.go:Consume. Unlike notes, links has no deprecated column
// the Go struct doesn't map, so a plain Returning("*") is safe here.
func (r *LinkRepository) Consume(ctx context.Context, id string) (*domain.Link, error) {
	var dbLink link

	err := r.db.NewDelete().
		Model(&dbLink).
		Where("id = ?", id).
		Where("burn_after_click OR (expires_at IS NOT NULL AND expires_at <= ?)", time.Now()).
		Returning("*").
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("postgres consume link: %w", err)
	}

	return linkToDomain(&dbLink), nil
}

// ConsumeVisible enforces the reader's current access in the DELETE itself, including after
// a concurrent writer has changed privacy. With allowBurn=false only expired rows qualify.
func (r *LinkRepository) ConsumeVisible(
	ctx context.Context, id string, callerID uuid.UUID, authenticated, allowBurn bool,
) (*domain.Link, error) {
	var dbLink link

	err := r.db.NewDelete().Model(&dbLink).Where("id = ?", id).
		Where("(expires_at IS NOT NULL AND expires_at <= ?) OR (burn_after_click AND ?)", time.Now(), allowBurn).
		Where("privacy = ? OR (privacy = ? AND ?) OR (privacy = ? AND owner_id = ? AND ?)",
			domain.PrivacyPublic, domain.PrivacyAuthenticated, authenticated,
			domain.PrivacyOwner, callerID, callerID != uuid.Nil).
		Returning("*").Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("postgres consume visible link: %w", err)
	}

	return linkToDomain(&dbLink), nil
}

// IncrementViewsVisible counts a read only while the current row still admits this reader.
func (r *LinkRepository) IncrementViewsVisible(
	ctx context.Context, id string, callerID uuid.UUID, authenticated bool,
) error {
	result, err := r.db.NewUpdate().TableExpr("links").Set("views = views + 1").Where("id = ?", id).
		Where("privacy = ? OR (privacy = ? AND ?) OR (privacy = ? AND owner_id = ? AND ?)",
			domain.PrivacyPublic, domain.PrivacyAuthenticated, authenticated,
			domain.PrivacyOwner, callerID, callerID != uuid.Nil).Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres increment visible link views: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres visible link rowsaffected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// IncrementViews atomically increments the click counter for a link.
func (r *LinkRepository) IncrementViews(ctx context.Context, id string) error {
	result, err := r.db.NewUpdate().
		TableExpr("links").
		Set("views = views + 1").
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres increment link views: %w", err)
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

// Delete removes a link by ID.
func (r *LinkRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.NewDelete().Model((*link)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres delete link: %w", err)
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
