package files

import (
	"context"
	"fmt"
	"time"

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
// the content, name, content key, object key, owner or download counter. The rules are in
// checkMutation: the reader must see the file, and must be its owner or give the original
// code. Errors are fail-hard: Update returns domain.ErrNotFound for a missing file,
// domain.ErrForbidden for a hidden file, domain.ErrExpired for an expired file,
// domain.ErrInvalidEditCode for a wrong code, a validation error for invalid options, and wraps
// storage errors. Notes and links use the same sequence of checks.
func (m *Manager) Update(
	ctx context.Context, slug, editCode string, reader domain.Reader, opts UpdateOptions,
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

	// Privacy comes before expiry, so that a hidden file gives the same answer in each state.
	if !existing.VisibleTo(reader) {
		return nil, domain.ErrForbidden
	}

	// An expired file is already gone for readers, so an update must not extend its life. The
	// error is the error of a read (410), so that the editor and the clients give the same answer.
	if existing.Expired(time.Now()) {
		return nil, domain.ErrExpired
	}

	err = m.checkMutation(existing, editCode, reader)
	if err != nil {
		return nil, err
	}

	updated := applyUpdateOptions(existing, opts)

	err = updated.Validate()
	if err == nil {
		err = updated.ValidateOwnership()
	}

	if err != nil {
		return nil, fmt.Errorf("validate file: %w", err)
	}

	// The response shows the file name, which is only in the sealed metadata. A record that
	// cannot be opened is not found, so Update must not change it.
	err = m.openMeta(ctx, slug, updated)
	if err != nil {
		return nil, err
	}

	// The update response does not need the content key.
	dropKey(updated)

	if opts.changes() {
		// Only the fields in opts are written: a parallel update of a different field on a
		// different server process stays. The object key stops a write to a new file that took
		// the slug after the read.
		err = m.deps.Storage.Update(ctx, dbID, existing.ObjectKey, opts, time.Now())
		if err != nil {
			return nil, fmt.Errorf("update file: %w", err)
		}
	}

	// The edit code is a secret of the create response. An update response does not show it.
	updated.EditCode = ""

	m.deps.Log.DebugContext(ctx, "file updated", "id", dbID)

	return updated, nil
}

// changes reports whether o changes a field.
func (o *UpdateOptions) changes() bool {
	return o.ClearExpiry || o.ExpiresAt != nil || o.Privacy != nil || o.BurnAfterDownload != nil
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
