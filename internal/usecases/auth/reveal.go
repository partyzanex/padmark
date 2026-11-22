//go:generate go run go.uber.org/mock/mockgen@latest -source=reveal.go -destination=reveal_mocks_test.go -package=auth -write_package_comment=false

package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// revealTokenTTL is how long a reveal token stays valid after Issue.
const revealTokenTTL = 10 * time.Minute

// RevealTokenStore persists one-time reveal tokens by token hash. The raw token never reaches it.
type RevealTokenStore interface {
	// Create stores an unused token bound to noteID. Fail-hard.
	Create(ctx context.Context, tokenHash, noteID string, expiresAt time.Time) error
	// Consume marks the token as used only when it is bound to noteID, unused, and not expired
	// at now, in one atomic statement. It returns false and no error when a condition is not
	// met, and an error only when the store fails.
	Consume(ctx context.Context, tokenHash, noteID string, now time.Time) (bool, error)
	// DeleteStale removes the tokens that expired before now and the used tokens. Fail-soft:
	// Issue calls it after it stores a new token and only logs an error.
	DeleteStale(ctx context.Context, now time.Time) error
}

// RevealManager issues and consumes the one-time reveal tokens that confirm the intent to read
// a burn-after-reading note. It owns the token value, its lifetime and the cleanup schedule.
type RevealManager struct {
	store RevealTokenStore
	log   *slog.Logger
}

// NewRevealManager returns a new RevealManager.
func NewRevealManager(store RevealTokenStore, log *slog.Logger) *RevealManager {
	return &RevealManager{store: store, log: log}
}

// Issue returns a new one-time token bound to noteID that is valid for ten minutes. The store
// keeps only the token hash. Then Issue deletes stale tokens; no background worker does that.
func (m *RevealManager) Issue(ctx context.Context, noteID string) (string, error) {
	tok, err := newRandomToken()
	if err != nil {
		return "", fmt.Errorf("new reveal token: %w", err)
	}

	now := time.Now()

	err = m.store.Create(ctx, domain.HashRevealToken(tok), noteID, now.Add(revealTokenTTL))
	if err != nil {
		return "", fmt.Errorf("create reveal token: %w", err)
	}

	sweepErr := m.store.DeleteStale(ctx, now)
	if sweepErr != nil {
		m.log.WarnContext(ctx, "delete stale reveal tokens failed", "err", sweepErr)
	}

	return tok, nil
}

// Consume marks tok as used when it is bound to noteID, unused, and not expired. It returns
// false when a condition is not met; the token then stays intact for its owner.
//
// A store failure also returns false, so the note is not read. The web adapter interface
// returns a bool only, so Consume logs the store error at the error level to keep it visible.
func (m *RevealManager) Consume(ctx context.Context, tok, noteID string) bool {
	consumed, err := m.store.Consume(ctx, domain.HashRevealToken(tok), noteID, time.Now())
	if err != nil {
		m.log.ErrorContext(ctx, "consume reveal token failed", "err", err)

		return false
	}

	return consumed
}
