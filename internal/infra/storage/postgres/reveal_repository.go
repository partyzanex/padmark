package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"
)

type revealTokenRow struct {
	bun.BaseModel `bun:"table:reveal_tokens"`

	ExpiresAt time.Time  `bun:"expires_at,notnull"`
	UsedAt    *time.Time `bun:"used_at"`
	// TokenHash is the base64url SHA-256 of the bearer handed to the client; the raw token is
	// never persisted, so a database leak yields no redeemable reveal tokens.
	TokenHash string `bun:"token_hash,pk"`
	NoteID    string `bun:"note_id,notnull"`
}

// RevealRepository stores one-time reveal tokens for burn-after-reading notes. It keeps only
// token hashes. The token value, its lifetime and the cleanup schedule belong to the caller.
type RevealRepository struct {
	db *bun.DB
}

// NewRevealRepository returns a new PostgreSQL-backed RevealRepository.
func NewRevealRepository(db *bun.DB) *RevealRepository {
	return &RevealRepository{db: db}
}

// Create stores an unused reveal token that is bound to noteID and expires at expiresAt.
func (s *RevealRepository) Create(ctx context.Context, tokenHash, noteID string, expiresAt time.Time) error {
	row := &revealTokenRow{
		BaseModel: bun.BaseModel{},
		TokenHash: tokenHash,
		NoteID:    noteID,
		ExpiresAt: expiresAt,
		// UsedAt nil keeps the token unused until Consume sets it.
		UsedAt: nil,
	}

	_, err := s.db.NewInsert().Model(row).Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres insert reveal token: %w", err)
	}

	return nil
}

// Consume marks the token as used when it is bound to noteID, unused, and not expired at now.
// The conditions are in the UPDATE itself, so the check and the write are one statement.
// It returns false and no error when a condition is not met; the token then stays as it was.
// It returns an error only when the database fails.
func (s *RevealRepository) Consume(ctx context.Context, tokenHash, noteID string, now time.Time) (bool, error) {
	res, err := s.db.NewUpdate().
		TableExpr("reveal_tokens").
		Set("used_at = ?", now).
		Where("token_hash = ?", tokenHash).
		Where("note_id = ?", noteID).
		Where("used_at IS NULL").
		Where("expires_at > ?", now).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("postgres consume reveal token: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("postgres consume reveal token rows affected: %w", err)
	}

	return affected > 0, nil
}

// DeleteStale deletes the tokens that expired before now and the tokens that are used.
func (s *RevealRepository) DeleteStale(ctx context.Context, now time.Time) error {
	_, err := s.db.NewDelete().
		TableExpr("reveal_tokens").
		Where("expires_at <= ?", now).
		WhereOr("used_at IS NOT NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("postgres delete stale reveal tokens: %w", err)
	}

	return nil
}
