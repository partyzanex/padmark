package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/errorcontext"
)

const (
	inviteTTL        = 24 * time.Hour
	inviteTokenBytes = 32
)

type inviteRow struct {
	bun.BaseModel `bun:"table:invites"`

	ExpiresAt time.Time  `bun:"expires_at,notnull"`
	UsedAt    *time.Time `bun:"used_at"`
	TokenHash string     `bun:"token_hash,pk"`
	UsedBy    string     `bun:"used_by"`
	CreatedBy uuid.UUID  `bun:"created_by,notnull"`
}

func (r *inviteRow) toDomain() *domain.Invite {
	return &domain.Invite{
		TokenHash: r.TokenHash,
		CreatedBy: r.CreatedBy,
		ExpiresAt: r.ExpiresAt,
		UsedAt:    r.UsedAt,
		UsedBy:    r.UsedBy,
	}
}

// InviteRepository is a SQLite-backed store for single-use registration invites.
// Consume uses a transaction (SELECT + UPDATE) since SQLite is single-writer.
type InviteRepository struct {
	db  *bun.DB
	log *slog.Logger
}

// NewInviteRepository returns a new SQLite-backed InviteRepository. log is used to surface
// lazy-sweep failures — pass slog.Default() if no project logger is at hand.
func NewInviteRepository(db *bun.DB, log *slog.Logger) *InviteRepository {
	return &InviteRepository{db: db, log: log}
}

// Issue returns a single-use invite token with a 24-hour TTL, storing only its SHA-256 hash.
// Expired and consumed tokens are lazily swept on each call.
func (r *InviteRepository) Issue(ctx context.Context, createdByID uuid.UUID) (string, error) {
	buf := make([]byte, inviteTokenBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	tok := base64.RawURLEncoding.EncodeToString(buf)

	row := &inviteRow{
		BaseModel: bun.BaseModel{},
		TokenHash: domain.HashInviteToken(tok),
		CreatedBy: createdByID,
		ExpiresAt: time.Now().Add(inviteTTL),
		// UsedAt/UsedBy zero = nil/empty until a redemption records the consuming user.
		UsedAt: nil,
		UsedBy: "",
	}

	_, err = r.db.NewInsert().Model(row).Exec(ctx)
	if err != nil {
		return "", fmt.Errorf("insert invite: %w", err)
	}

	// Best-effort sweep of stale invites; a failure here does not block token issuance —
	// stale rows simply persist until the next Issue call.
	_, sweepErr := r.db.NewDelete().
		TableExpr("invites").
		Where("expires_at < ?", time.Now()).
		WhereOr("used_at IS NOT NULL").
		Exec(ctx)
	if sweepErr != nil {
		r.log.WarnContext(ctx, "invite sweep failed", "err", sweepErr)
	}

	return tok, nil
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

		return fmt.Errorf("fetch invite after miss: %w", err)
	}

	if existing.UsedAt != nil {
		return domain.ErrInviteUsed
	}

	return domain.ErrInviteExpired
}

// Consume atomically marks the invite as used by username inside a transaction.
// Returns domain.ErrInviteUsed, domain.ErrInviteExpired, or domain.ErrNotFound
// when the token cannot be consumed.
func (r *InviteRepository) Consume(ctx context.Context, token, username string) (*domain.Invite, error) {
	tokenHash := domain.HashInviteToken(token)

	var result *domain.Invite

	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, btx bun.Tx) error {
		var row inviteRow

		err := btx.NewSelect().Model(&row).
			Where("token_hash = ?", tokenHash).
			Where("used_at IS NULL").
			Where("expires_at > ?", time.Now()).
			Scan(ctx)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("select invite: %w", err)
			}

			return errorcontext.Wrap(classifyInviteMiss(ctx, btx, tokenHash), "classify invite miss")
		}

		now := time.Now()

		_, err = btx.NewUpdate().Model(&row).
			Set("used_at = ?", now).
			Set("used_by = ?", username).
			Where("token_hash = ?", tokenHash).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("mark invite used: %w", err)
		}

		row.UsedAt = &now
		row.UsedBy = username
		result = row.toDomain()

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("consume invite: %w", err)
	}

	return result, nil
}

// RedeemInvite atomically consumes the invite and inserts usr in one transaction.
// If the user insert fails (e.g. a username race → ErrUserExists), the whole tx rolls
// back and the invite stays unconsumed, so a failed registration never burns the token.
// Returns invite sentinels (ErrNotFound/ErrInviteUsed/ErrInviteExpired) or ErrUserExists.
func (r *InviteRepository) RedeemInvite(ctx context.Context, token, username string, usr *domain.User) error {
	tokenHash := domain.HashInviteToken(token)

	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, btx bun.Tx) error {
		var row inviteRow

		selErr := btx.NewSelect().Model(&row).
			Where("token_hash = ?", tokenHash).
			Where("used_at IS NULL").
			Where("expires_at > ?", time.Now()).
			Scan(ctx)
		if selErr != nil {
			if !errors.Is(selErr, sql.ErrNoRows) {
				return fmt.Errorf("select invite: %w", selErr)
			}

			return errorcontext.Wrap(classifyInviteMiss(ctx, btx, tokenHash), "classify invite miss")
		}

		now := time.Now()

		_, updErr := btx.NewUpdate().Model(&row).
			Set("used_at = ?", now).
			Set("used_by = ?", username).
			Where("token_hash = ?", tokenHash).
			Exec(ctx)
		if updErr != nil {
			return fmt.Errorf("mark invite used: %w", updErr)
		}

		return errorcontext.Wrap(insertUser(ctx, btx, usr), "insert user")
	})
	if err != nil {
		return fmt.Errorf("redeem invite: %w", err)
	}

	return nil
}
