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
	err := b.enforceExpiry(ctx, id, note)
	if err != nil {
		return nil, fmt.Errorf("enforce expiry: %w", err)
	}

	if note.BurnAfterReading {
		operationResult, operationErr := b.burnNote(ctx, id, note)
		if operationErr != nil {
			return operationResult, fmt.Errorf("burn note: %w", operationErr)
		}

		return operationResult, nil
	}

	return note, nil
}

// enforceExpiry is the expiry half of applyPolicy: an expired note (plain TTL or a started burn
// timer) is deleted and ErrExpired returned. It never consumes a note or starts a burn timer.
func (b *burner) enforceExpiry(ctx context.Context, id string, note *domain.Note) error {
	if note.ExpiresAt == nil || !time.Now().After(*note.ExpiresAt) {
		return nil
	}

	delErr := b.storage.Delete(ctx, domain.HashSlug(id))
	if delErr != nil {
		b.log.ErrorContext(ctx, "delete expired note", "id", domain.HashSlug(id), "err", delErr)
	}

	return domain.ErrExpired
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
		b.log.ErrorContext(ctx, "increment views", "id", domain.HashSlug(id), "err", incErr)
	} else {
		note.Views++
	}
}

func (b *burner) burnNote(ctx context.Context, id string, note *domain.Note) (*domain.Note, error) {
	if note.BurnTTL > 0 {
		operationResult, operationErr := b.startTimer(ctx, id, note)
		if operationErr != nil {
			return operationResult, fmt.Errorf("start timer: %w", operationErr)
		}

		return operationResult, nil
	}

	consumed, consumeErr := b.storage.Consume(ctx, domain.HashSlug(id), note.EffectivePrivacy())
	if consumeErr != nil {
		return nil, fmt.Errorf("burn after reading: %w", consumeErr)
	}

	consumed.ID = id

	decErr := decryptContent(ctx, b.encryptor, b.log, consumed)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading: %w", decErr)
	}

	b.log.DebugContext(ctx, "note burned", "id", domain.HashSlug(id))

	return consumed, nil
}

func (b *burner) startTimer(ctx context.Context, id string, note *domain.Note) (*domain.Note, error) {
	expiresAt := time.Now().UTC().Add(time.Duration(note.BurnTTL) * time.Second)

	expected := note.EffectivePrivacy()

	updated, burnErr := b.storage.SetBurnExpiry(ctx, domain.HashSlug(id), expiresAt, expected)
	if burnErr != nil {
		operationResult, operationErr := b.handleSetExpiryErr(ctx, id, expected, burnErr)
		if operationErr != nil {
			return operationResult, fmt.Errorf("handle set expiry err: %w", operationErr)
		}

		return operationResult, nil
	}

	updated.ID = id

	decErr := decryptContent(ctx, b.encryptor, b.log, updated)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading: %w", decErr)
	}

	b.log.DebugContext(ctx, "burn timer started", "id", domain.HashSlug(id), "expires_at", expiresAt)

	return updated, nil
}

// handleSetExpiryErr serves a note whose timer a concurrent reader already started. It returns
// the current row only while its privacy is still expected: a changed access level means the
// reader's check no longer applies, so the read fails as not found.
func (b *burner) handleSetExpiryErr(
	ctx context.Context, id string, expected domain.Privacy, burnErr error,
) (*domain.Note, error) {
	if !errors.Is(burnErr, domain.ErrNotFound) {
		return nil, fmt.Errorf("burn after reading: %w", burnErr)
	}

	current, getErr := b.storage.Get(ctx, domain.HashSlug(id))
	if getErr != nil {
		return nil, fmt.Errorf("burn after reading (re-fetch): %w", getErr)
	}

	if current.EffectivePrivacy() != expected {
		return nil, fmt.Errorf("burn after reading (re-fetch): %w", domain.ErrNotFound)
	}

	current.ID = id

	decErr := decryptContent(ctx, b.encryptor, b.log, current)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading (re-fetch): %w", decErr)
	}

	return current, nil
}
