package sqlite

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	revealTokenTTL   = 10 * time.Minute
	revealTokenBytes = 32
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

// RevealRepository issues and consumes one-time reveal tokens for burn-after-reading notes.
// SQLite is single-writer, so the transaction approach is safe in practice.
type RevealRepository struct {
	db  *bun.DB
	log *slog.Logger
}

// NewRevealRepository returns a new SQLite-backed RevealStore. log is used to surface
// lazy-sweep failures.
func NewRevealRepository(db *bun.DB, log *slog.Logger) *RevealRepository {
	return &RevealRepository{db: db, log: log}
}

// Issue generates a one-time token bound to noteID with a 10-minute TTL.
// Expired and consumed tokens are lazily cleaned up on each call.
//

func (s *RevealRepository) Issue(ctx context.Context, noteID string) (string, error) {
	buf := make([]byte, revealTokenBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	tok := base64.RawURLEncoding.EncodeToString(buf)

	row := &revealTokenRow{
		BaseModel: bun.BaseModel{},
		TokenHash: domain.HashRevealToken(tok),
		NoteID:    noteID,
		ExpiresAt: time.Now().Add(revealTokenTTL),
		// UsedAt zero = nil until Consume sets it; preserves the "unused" invariant on insert.
		UsedAt: nil,
	}

	_, err = s.db.NewInsert().Model(row).Exec(ctx)
	if err != nil {
		return "", fmt.Errorf("insert reveal token: %w", err)
	}

	// Best-effort sweep of expired reveal tokens; a failure here does not block token issuance —
	// stale rows simply persist until the next Issue call.
	_, sweepErr := s.db.NewDelete().
		TableExpr("reveal_tokens").
		Where("expires_at < ?", time.Now()).
		WhereOr("used_at IS NOT NULL").
		Exec(ctx)
	if sweepErr != nil {
		s.log.WarnContext(ctx, "reveal token sweep failed", "err", sweepErr)
	}

	return tok, nil
}

// Consume marks the token as used when it is bound to noteID, unused, and unexpired.
// SQLite is single-writer, so the SELECT + UPDATE inside a transaction is race-free.
// Returns false if the token is unknown, expired, already used, or bound to a different noteID —
// in all these cases the token is left intact.
func (s *RevealRepository) Consume(ctx context.Context, tok, noteID string) bool {
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, btx bun.Tx) error {
		var row revealTokenRow

		err := btx.NewSelect().Model(&row).
			Where("token_hash = ?", domain.HashRevealToken(tok)).
			Where("note_id = ?", noteID).
			Where("used_at IS NULL").
			Where("expires_at > ?", time.Now()).
			Scan(ctx)
		if err != nil {
			return fmt.Errorf("select reveal token: %w", err)
		}

		_, err = btx.NewUpdate().Model(&row).
			Set("used_at = ?", time.Now()).
			Where("token_hash = ?", domain.HashRevealToken(tok)).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("mark reveal token used: %w", err)
		}

		return nil
	})

	return err == nil
}
