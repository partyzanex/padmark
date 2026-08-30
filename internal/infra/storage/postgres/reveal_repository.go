package postgres

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
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
// Consume is atomic: a single UPDATE ... RETURNING eliminates the SELECT+UPDATE race.
type RevealRepository struct {
	db  *bun.DB
	log *slog.Logger
}

// NewRevealRepository returns a new PostgreSQL-backed RevealStore. log is used to surface
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
		Token:     tok,
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

// Consume atomically marks the token as used when it is bound to noteID, unused, and unexpired.
// A single conditional UPDATE avoids the SELECT+UPDATE race under concurrent requests.
// Returns false if the token is unknown, expired, already used, or bound to a different noteID —
// in all these cases the token is left intact.
func (s *RevealRepository) Consume(ctx context.Context, tok, noteID string) bool {
	now := time.Now()

	res, err := s.db.NewUpdate().
		TableExpr("reveal_tokens").
		Set("used_at = ?", now).
		Where("token = ?", tok).
		Where("note_id = ?", noteID).
		Where("used_at IS NULL").
		Where("expires_at > ?", now).
		Exec(ctx)
	if err != nil {
		return false
	}

	affected, err := res.RowsAffected()

	return err == nil && affected > 0
}
