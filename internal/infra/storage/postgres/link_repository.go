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
		if isSlugConflict(err) {
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

// Update writes the target URL and only the optional fields that patch sets (see
// links.Storage). RETURNING reports the persisted counters and immutable fields without
// overwriting clicks recorded since the caller's read. storedEditCode selects the link version.
func (r *LinkRepository) Update(
	ctx context.Context, id, storedEditCode string, patch *domain.LinkPatch,
) (*domain.Link, error) {
	var dbLink link

	query := r.db.NewUpdate().Model(&dbLink).Set("target_url = ?", patch.TargetURL)

	err := setLinkPatch(query, patch).
		Where("id = ?", id).
		Where("edit_code = ?", storedEditCode).
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

// setLinkPatch adds a SET clause for each optional field that patch sets. An omitted field gets
// no clause, so the UPDATE keeps the value that is stored at the time of the write.
func setLinkPatch(query *bun.UpdateQuery, patch *domain.LinkPatch) *bun.UpdateQuery {
	patched := query

	switch {
	case patch.ClearExpiry:
		patched = patched.Set("expires_at = NULL")
	case patch.ExpiresAt != nil:
		patched = patched.Set("expires_at = ?", patch.ExpiresAt)
	default: // The caller omitted the expiry.
	}

	if patch.Privacy != nil {
		patched = patched.Set("privacy = ?", string(*patch.Privacy))
	}

	switch {
	case patch.ResetDirectRedirect:
		patched = patched.Set("direct_redirect = NULL")
	case patch.DirectRedirect != nil:
		patched = patched.Set("direct_redirect = ?", *patch.DirectRedirect)
	default: // The caller omitted the redirect mode.
	}

	return patched
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

// Exists reports whether a link holds the slug id. notes.Manager, links.Manager and files.Manager
// use it for the cross-resource slug check. An expired link does not hold its slug: Exists first
// deletes it through DeleteExpired, which checks the expiry in the DELETE, so a link that its
// owner extended stays. Thus the address of an expired link does not depend on a read after the
// expiry.
func (r *LinkRepository) Exists(ctx context.Context, id string) (bool, error) {
	err := r.DeleteExpired(ctx, id)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return false, fmt.Errorf("postgres link exists: %w", err)
	}

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

// Delete removes the link version with id and storedEditCode (see links.Storage).
func (r *LinkRepository) Delete(ctx context.Context, id, storedEditCode string) error {
	result, err := r.db.NewDelete().Model((*link)(nil)).
		Where("id = ?", id).Where("edit_code = ?", storedEditCode).Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres delete link: %w", err)
	}

	return linkRowRemoved(result)
}

// DeleteExpired removes the link with id only while it is still expired at the moment of the
// DELETE. It returns domain.ErrNotFound when no such row exists, for example after its owner
// extended it. An unread burn-after-click link without an expiry stays.
func (r *LinkRepository) DeleteExpired(ctx context.Context, id string) error {
	result, err := r.db.NewDelete().Model((*link)(nil)).
		Where("id = ?", id).
		Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now()).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres delete expired link: %w", err)
	}

	return linkRowRemoved(result)
}

// linkRowRemoved returns domain.ErrNotFound when a DELETE removed no link.
func linkRowRemoved(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres rowsaffected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}
