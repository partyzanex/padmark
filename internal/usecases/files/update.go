package files

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// UpdateOptions holds the changes to the access rules of a file. A nil pointer keeps the
// current value. ClearExpiry removes the expiry and has priority over ExpiresAt.
type UpdateOptions struct {
	ExpiresAt         *time.Time
	Privacy           *domain.Privacy
	BurnAfterDownload *bool
	ClearExpiry       bool
}

// Update changes the expiry, privacy and burn-after-download rule of a file. It does not change
// the content, name, content key, object key, owner or download counter. The owner can omit
// the edit code; other callers must give the original code. Errors are fail-hard: Update
// returns domain.ErrNotFound for a missing or expired file, domain.ErrInvalidEditCode for a
// wrong code, a validation error for invalid options, and wraps storage errors.
func (m *Manager) Update(
	ctx context.Context, slug, editCode string, callerID uuid.UUID, opts UpdateOptions,
) (*domain.File, error) {
	dbID := domain.HashSlug(slug)
	// The lock keeps two updates in this process from losing each other's omitted fields.
	unlock, err := m.deps.SlugLock.Lock(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("lock file: %w", err)
	}
	defer unlock()

	existing, err := m.deps.Storage.GetByID(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("update file: %w", err)
	}

	// An expired file is already gone for readers, so an update must not extend its life.
	if existing.Expired(time.Now()) {
		return nil, domain.ErrNotFound
	}

	if !existing.OwnedBy(callerID) && !m.deps.Hasher.Verify(existing.EditCode, editCode) {
		return nil, domain.ErrInvalidEditCode
	}

	updated := applyUpdateOptions(existing, opts)

	err = updated.Validate()
	if err == nil {
		err = updated.ValidateOwnership()
	}

	if err != nil {
		return nil, fmt.Errorf("validate file: %w", err)
	}

	err = m.deps.Storage.Update(ctx, updated)
	if err != nil {
		return nil, fmt.Errorf("update file: %w", err)
	}

	// The response shows the file name, which is only in the sealed metadata.
	err = m.openMeta(ctx, slug, updated)
	if err != nil {
		return nil, err
	}

	// The edit code is a secret of the create response. An update response does not show it.
	updated.EditCode = ""

	m.deps.Log.DebugContext(ctx, "file updated", "id", dbID)

	return updated, nil
}

func applyUpdateOptions(existing *domain.File, opts UpdateOptions) *domain.File {
	updated := *existing
	if opts.Privacy != nil {
		updated.Privacy = opts.Privacy
	}

	if opts.ClearExpiry {
		updated.ExpiresAt = nil
	} else if opts.ExpiresAt != nil {
		updated.ExpiresAt = opts.ExpiresAt
	}

	if opts.BurnAfterDownload != nil {
		updated.BurnAfterDownload = *opts.BurnAfterDownload
	}

	return &updated
}
