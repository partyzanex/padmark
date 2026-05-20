package sqlite

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/uptrace/bun"
)

const (
	revealTokenTTL   = 10 * time.Minute
	revealTokenBytes = 32
)

type revealTokenRow struct {
	bun.BaseModel `bun:"table:reveal_tokens"`

	ExpiresAt time.Time  `bun:"expires_at,notnull"`
	UsedAt    *time.Time `bun:"used_at"`
	Token     string     `bun:"token,pk"`
	NoteID    string     `bun:"note_id,notnull"`
}

// RevealRepository issues and consumes one-time reveal tokens for burn-after-reading notes.
// SQLite is single-writer, so the transaction approach is safe in practice.
type RevealRepository struct {
	db *bun.DB
}

// NewRevealRepository returns a new SQLite-backed RevealStore.
func NewRevealRepository(db *bun.DB) *RevealRepository {
	return &RevealRepository{db: db}
}

// Issue generates a one-time token bound to noteID with a 10-minute TTL.
// Expired and consumed tokens are lazily cleaned up on each call.
func (s *RevealRepository) Issue(ctx context.Context, noteID string) (string, error) {
	buf := make([]byte, revealTokenBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	tok := base64.RawURLEncoding.EncodeToString(buf)

	row := &revealTokenRow{
		Token:     tok,
		NoteID:    noteID,
		ExpiresAt: time.Now().Add(revealTokenTTL),
	}

	_, err = s.db.NewInsert().Model(row).Exec(ctx)
	if err != nil {
		return "", fmt.Errorf("insert reveal token: %w", err)
	}

	//nolint:errcheck // best-effort sweep; stale tokens persist until next Issue
	_, _ = s.db.NewDelete().
		TableExpr("reveal_tokens").
		Where("expires_at < ?", time.Now()).
		WhereOr("used_at IS NOT NULL").
		Exec(ctx)

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
			Where("token = ?", tok).
			Where("note_id = ?", noteID).
			Where("used_at IS NULL").
			Where("expires_at > ?", time.Now()).
			Scan(ctx)
		if err != nil {
			return fmt.Errorf("select reveal token: %w", err)
		}

		_, err = btx.NewUpdate().Model(&row).
			Set("used_at = ?", time.Now()).
			Where("token = ?", tok).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("mark reveal token used: %w", err)
		}

		return nil
	})

	return err == nil
}
