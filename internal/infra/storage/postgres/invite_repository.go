package postgres

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

type inviteRow struct {
	bun.BaseModel `bun:"table:invites"`

	ExpiresAt time.Time  `bun:"expires_at,notnull"`
	UsedAt    *time.Time `bun:"used_at"`
	TokenHash string     `bun:"token_hash,pk"`
	UsedBy    string     `bun:"used_by"`
	CreatedBy uuid.UUID  `bun:"created_by,notnull"`
}

// InviteRepository is a PostgreSQL-backed store for single-use registration invites. It keeps
// only token hashes. The token value, its lifetime and the cleanup schedule belong to the caller.
type InviteRepository struct {
	db *bun.DB
}

// NewInviteRepository returns a new PostgreSQL-backed InviteRepository.
func NewInviteRepository(db *bun.DB) *InviteRepository {
	return &InviteRepository{db: db}
}

// Create stores inv. The caller sets inv.TokenHash and inv.ExpiresAt.
func (r *InviteRepository) Create(ctx context.Context, inv *domain.Invite) error {
	row := &inviteRow{
		BaseModel: bun.BaseModel{},
		TokenHash: inv.TokenHash,
		CreatedBy: inv.CreatedBy,
		ExpiresAt: inv.ExpiresAt,
		UsedAt:    inv.UsedAt,
		UsedBy:    inv.UsedBy,
	}

	_, err := r.db.NewInsert().Model(row).Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres insert invite: %w", err)
	}

	return nil
}

// DeleteStale deletes the invites that expired before now and the invites that are used.
func (r *InviteRepository) DeleteStale(ctx context.Context, now time.Time) error {
	_, err := r.db.NewDelete().
		TableExpr("invites").
		Where("expires_at <= ?", now).
		WhereOr("used_at IS NOT NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres delete stale invites: %w", err)
	}

	return nil
}

// classifyInviteMiss reads the invite row via conn (a *bun.DB or bun.Tx) and returns the
// sentinel explaining a zero-row consume: ErrNotFound, ErrInviteUsed, or ErrInviteExpired.
func classifyInviteMiss(ctx context.Context, conn bun.IDB, tokenHash string) error {
	var existing inviteRow

	err := conn.NewSelect().Model(&existing).Where("token_hash = ?", tokenHash).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}

		return fmt.Errorf("postgres fetch invite after miss: %w", err)
	}

	if existing.UsedAt != nil {
		return domain.ErrInviteUsed
	}

	return domain.ErrInviteExpired
}

// RedeemInvite marks the invite with tokenHash as used by username and inserts usr, in one
// transaction. The conditional UPDATE is the check and the write in one statement. If the user
// insert fails (for example ErrUserExists after a username race), the transaction rolls back and
// the invite stays unused, so a failed registration never uses up the token.
// Returns the invite sentinels (ErrNotFound, ErrInviteUsed, ErrInviteExpired) or ErrUserExists.
func (r *InviteRepository) RedeemInvite(
	ctx context.Context, tokenHash, username string, usr *domain.User, now time.Time,
) error {
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, btx bun.Tx) error {
		res, updErr := btx.NewUpdate().
			TableExpr("invites").
			Set("used_at = ?", now).
			Set("used_by = ?", username).
			Where("token_hash = ?", tokenHash).
			Where("used_at IS NULL").
			Where("expires_at > ?", now).
			Exec(ctx)
		if updErr != nil {
			return fmt.Errorf("postgres consume invite: %w", updErr)
		}

		affected, affErr := res.RowsAffected()
		if affErr != nil {
			return fmt.Errorf("postgres rows affected: %w", affErr)
		}

		if affected == 0 {
			return classifyInviteMiss(ctx, btx, tokenHash)
		}

		return insertUser(ctx, btx, usr)
	})
	if err != nil {
		return fmt.Errorf("postgres redeem invite: %w", err)
	}

	return nil
}
