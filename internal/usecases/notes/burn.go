package notes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// burner owns the on-read policy for a fetched note: plain-expiry enforcement and the
// burn-after-reading state machine (immediate consume vs. starting a grace-period timer).
// It shares Manager's storage/encryptor/log collaborators directly rather than being exposed
// behind its own interface — Manager is its only constructor and only caller, so a seam here
// would be premature abstraction (no second implementation, nothing to mock independently).
type burner struct {
	storage   Storage
	encryptor Encryptor
	log       *slog.Logger
}

func newBurner(storage Storage, encryptor Encryptor, log *slog.Logger) *burner {
	return &burner{storage: storage, encryptor: encryptor, log: log}
}

// applyPolicy applies expiry and burn-after-reading logic to an already-fetched note. Plain
// TTL expiry and burn-timer expiry share the same ExpiresAt field, so both are enforced here
// in one pass rather than split across two call sites.
func (b *burner) applyPolicy(ctx context.Context, id string, note *domain.Note) (*domain.Note, error) {
	if note.ExpiresAt != nil && time.Now().After(*note.ExpiresAt) {
		delErr := b.storage.Delete(ctx, domain.HashSlug(id))
		if delErr != nil {
			b.log.ErrorContext(ctx, "delete expired note", "id", id, "err", delErr)
		}

		return nil, domain.ErrExpired
	}

	if note.BurnAfterReading {
		return b.burnNote(ctx, id, note)
	}

	return note, nil
}

// applyViewIncrement increments the view counter unless the note was just consumed by a
// pure burn-after-reading (no TTL) — that note no longer exists, so a view count is moot.
// A burn-after-reading note with a TTL survives its grace period and IS counted on every
// read: SetBurnExpiry flips burn_after_reading off on the timer-start read, so by the time
// this runs BurnAfterReading is true only for the deleted pure-burn case.
func (b *burner) applyViewIncrement(ctx context.Context, id string, note *domain.Note) {
	if note.BurnAfterReading {
		return
	}

	incErr := b.storage.IncrementViews(ctx, domain.HashSlug(id))
	if incErr != nil {
		b.log.ErrorContext(ctx, "increment views", "id", id, "err", incErr)
	} else {
		note.Views++
	}
}

func (b *burner) burnNote(ctx context.Context, id string, note *domain.Note) (*domain.Note, error) {
	if note.BurnTTL > 0 {
		return b.startTimer(ctx, id, note)
	}

	consumed, consumeErr := b.storage.Consume(ctx, domain.HashSlug(id))
	if consumeErr != nil {
		return nil, fmt.Errorf("burn after reading: %w", consumeErr)
	}

	consumed.ID = id

	decErr := decryptContent(ctx, b.encryptor, b.log, consumed)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading: %w", decErr)
	}

	b.log.DebugContext(ctx, "note burned", "id", id)

	return consumed, nil
}

func (b *burner) startTimer(ctx context.Context, id string, note *domain.Note) (*domain.Note, error) {
	expiresAt := time.Now().UTC().Add(time.Duration(note.BurnTTL) * time.Second)

	updated, burnErr := b.storage.SetBurnExpiry(ctx, domain.HashSlug(id), expiresAt)
	if burnErr != nil {
		return b.handleSetExpiryErr(ctx, id, burnErr)
	}

	updated.ID = id

	decErr := decryptContent(ctx, b.encryptor, b.log, updated)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading: %w", decErr)
	}

	b.log.DebugContext(ctx, "burn timer started", "id", id, "expires_at", expiresAt)

	return updated, nil
}

func (b *burner) handleSetExpiryErr(ctx context.Context, id string, burnErr error) (*domain.Note, error) {
	if !errors.Is(burnErr, domain.ErrNotFound) {
		return nil, fmt.Errorf("burn after reading: %w", burnErr)
	}

	current, getErr := b.storage.Get(ctx, domain.HashSlug(id))
	if getErr != nil {
		return nil, fmt.Errorf("burn after reading (re-fetch): %w", getErr)
	}

	current.ID = id

	decErr := decryptContent(ctx, b.encryptor, b.log, current)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading (re-fetch): %w", decErr)
	}

	return current, nil
}
