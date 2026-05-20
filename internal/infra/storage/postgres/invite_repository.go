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

	// Disambiguate: fetch the row regardless of state to classify the failure.
	var existing inviteRow

	selectErr := r.db.NewSelect().Model(&existing).Where("token = ?", token).Scan(ctx)
	if selectErr != nil {
		if errors.Is(selectErr, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("fetch invite after miss: %w", selectErr)
	}

	if existing.UsedAt != nil {
		return nil, domain.ErrInviteUsed
	}

	return nil, domain.ErrInviteExpired
}
