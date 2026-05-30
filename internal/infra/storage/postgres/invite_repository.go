package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	inviteTTL        = 24 * time.Hour
	inviteTokenBytes = 32
)

type inviteRow struct {
	bun.BaseModel `bun:"table:invites"`

	ExpiresAt time.Time  `bun:"expires_at,notnull"`
	UsedAt    *time.Time `bun:"used_at"`
	Token     string     `bun:"token,pk"`
	CreatedBy string     `bun:"created_by,notnull"`
	UsedBy    string     `bun:"used_by"`
}

func (r *inviteRow) toDomain() *domain.Invite {
	return &domain.Invite{
		Token:     r.Token,
		CreatedBy: r.CreatedBy,
		ExpiresAt: r.ExpiresAt,
		UsedAt:    r.UsedAt,
		UsedBy:    r.UsedBy,
	}
}

// InviteRepository is a PostgreSQL-backed store for single-use registration invites.
// Consume is atomic: a single UPDATE ... RETURNING eliminates the SELECT+UPDATE race.
type InviteRepository struct {
	db *bun.DB
}

// NewInviteRepository returns a new PostgreSQL-backed InviteRepository.
func NewInviteRepository(db *bun.DB) *InviteRepository {
	return &InviteRepository{db: db}
}

// Issue generates a single-use invite token with a 24-hour TTL.
// Expired and consumed tokens are lazily swept on each call.
//
//nolint:dupl // token-issuing pattern is identical across token table types by design
func (r *InviteRepository) Issue(ctx context.Context, createdByID string) (string, error) {
	buf := make([]byte, inviteTokenBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	tok := base64.RawURLEncoding.EncodeToString(buf)

	row := &inviteRow{
		Token:     tok,
		CreatedBy: createdByID,
		ExpiresAt: time.Now().Add(inviteTTL),
	}

	_, err = r.db.NewInsert().Model(row).Exec(ctx)
	if err != nil {
		return "", fmt.Errorf("insert invite: %w", err)
	}

	//nolint:errcheck // best-effort sweep; stale invites persist until next Issue
	_, _ = r.db.NewDelete().
		TableExpr("invites").
		Where("expires_at < ?", time.Now()).
		WhereOr("used_at IS NOT NULL").
		Exec(ctx)

	return tok, nil
}

// Consume atomically marks the invite as used by username.
// Returns domain.ErrInviteUsed, domain.ErrInviteExpired, or domain.ErrNotFound when the
// update matches zero rows, disambiguating by a follow-up SELECT.
func (r *InviteRepository) Consume(ctx context.Context, token, username string) (*domain.Invite, error) {
	now := time.Now()

	var row inviteRow

	const query = `UPDATE invites SET used_at = ?, used_by = ?` +
		` WHERE token = ? AND used_at IS NULL AND expires_at > ? RETURNING *`

	err := r.db.NewRaw(query, now, username, token, now).Scan(ctx, &row)
	if err == nil {
		return row.toDomain(), nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("consume invite: %w", err)
	}

	return nil, classifyInviteMiss(ctx, r.db, token)
}

// classifyInviteMiss reads the invite row via conn (a *bun.DB or bun.Tx) and returns
// the sentinel explaining why a consume matched zero rows: ErrNotFound, ErrInviteUsed,
// or ErrInviteExpired.
func classifyInviteMiss(ctx context.Context, conn bun.IDB, token string) error {
	var existing inviteRow

	err := conn.NewSelect().Model(&existing).Where("token = ?", token).Scan(ctx)
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

// RedeemInvite atomically consumes the invite and inserts usr in one transaction.
// If the user insert fails (e.g. a username race → ErrUserExists), the whole tx rolls
// back and the invite stays unconsumed, so a failed registration never burns the token.
// Returns invite sentinels (ErrNotFound/ErrInviteUsed/ErrInviteExpired) or ErrUserExists.
func (r *InviteRepository) RedeemInvite(ctx context.Context, token, username string, usr *domain.User) error {
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, btx bun.Tx) error {
		now := time.Now()

		res, updErr := btx.NewUpdate().
			TableExpr("invites").
			Set("used_at = ?", now).
			Set("used_by = ?", username).
			Where("token = ?", token).
			Where("used_at IS NULL").
			Where("expires_at > ?", now).
			Exec(ctx)
		if updErr != nil {
			return fmt.Errorf("consume invite: %w", updErr)
		}

		affected, affErr := res.RowsAffected()
		if affErr != nil {
			return fmt.Errorf("rows affected: %w", affErr)
		}

		if affected == 0 {
			return classifyInviteMiss(ctx, btx, token)
		}

		return insertUser(ctx, btx, usr)
	})
	if err != nil {
		if isInviteSentinel(err) {
			return err //nolint:wrapcheck // domain sentinel, must stay unwrapped for errors.Is
		}

		return fmt.Errorf("redeem invite: %w", err)
	}

	return nil
}

// isInviteSentinel reports whether err is a domain sentinel that must pass through
// RunInTx unwrapped so callers can errors.Is it.
func isInviteSentinel(err error) bool {
	return errors.Is(err, domain.ErrNotFound) ||
		errors.Is(err, domain.ErrInviteUsed) ||
		errors.Is(err, domain.ErrInviteExpired) ||
		errors.Is(err, domain.ErrUserExists)
}
