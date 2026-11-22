package links

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// UpdateOptions carries optional changes. Nil pointers preserve the current value;
// ClearExpiry and ResetDirectRedirect explicitly remove their respective overrides.
type UpdateOptions struct {
	ExpiresAt           *time.Time
	Privacy             *domain.Privacy
	DirectRedirect      *bool
	ClearExpiry         bool
	ResetDirectRedirect bool
}

// Update replaces the destination without changing the slug, edit code, owner,
// creation time, burn-after-click setting, or click counter. The exact owner may
// omit the edit code; other callers must prove knowledge of the original code.
func (m *Manager) Update(
	ctx context.Context, slug, editCode string, callerID uuid.UUID, targetURL string, opts UpdateOptions,
) (*domain.Link, error) {
	validURL, err := validateTargetURL(targetURL)
	if err != nil {
		return nil, fmt.Errorf("validate target url: %w", err)
	}

	dbID := domain.HashSlug(slug)
	// Serialize updates and cache fills for this slug so an in-flight read cannot
	// cache the old destination after this write evicts it. The lock is local to this process:
	// omitted fields stay because the storage patch does not write them.
	unlock, err := m.slugLock.Lock(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("lock link: %w", err)
	}
	defer unlock()

	existing, err := m.storage.GetByID(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("update link: %w", err)
	}

	if !existing.OwnedBy(callerID) && !m.hasher.Verify(existing.EditCode, editCode) {
		return nil, domain.ErrInvalidEditCode
	}

	// The merged snapshot is only for validation. OwnerID cannot change, so the ownership check
	// stays correct when another replica changes the other fields after GetByID.
	err = validateLink(applyUpdateOptions(existing, opts))
	if err != nil {
		return nil, fmt.Errorf("validate link: %w", err)
	}

	ciphertext, err := m.encryptor.Encrypt(validURL, dbID)
	if err != nil {
		return nil, fmt.Errorf("encrypt target url: %w", err)
	}

	// The patch carries only the fields that the caller sent. existing.EditCode is the stored
	// hash, so the UPDATE changes only the link checked above, not a new link with this ID.
	persisted, err := m.storage.Update(ctx, dbID, existing.EditCode, newLinkPatch(ciphertext, opts))
	if err != nil {
		return nil, fmt.Errorf("update link: %w", err)
	}

	m.evictCache(dbID)

	persisted.ID = slug

	persisted.TargetURL = validURL
	// The edit code is a creation-only secret, never an update response field.
	persisted.EditCode = ""

	m.log.DebugContext(ctx, "link updated", "id", domain.HashSlug(slug))

	return persisted, nil
}

// newLinkPatch converts the caller's options to a storage patch with the encrypted destination.
func newLinkPatch(ciphertext string, opts UpdateOptions) *domain.LinkPatch {
	return &domain.LinkPatch{
		ExpiresAt:           opts.ExpiresAt,
		Privacy:             opts.Privacy,
		DirectRedirect:      opts.DirectRedirect,
		TargetURL:           ciphertext,
		ClearExpiry:         opts.ClearExpiry,
		ResetDirectRedirect: opts.ResetDirectRedirect,
	}
}

// applyUpdateOptions returns existing with the caller's options applied, for validation.
func applyUpdateOptions(existing *domain.Link, opts UpdateOptions) *domain.Link {
	updated := *existing
	if opts.Privacy != nil {
		updated.Privacy = opts.Privacy
	}

	if opts.ClearExpiry {
		updated.ExpiresAt = nil
	} else if opts.ExpiresAt != nil {
		updated.ExpiresAt = opts.ExpiresAt
	}

	if opts.ResetDirectRedirect {
		updated.DirectRedirect = nil
	} else if opts.DirectRedirect != nil {
		updated.DirectRedirect = opts.DirectRedirect
	}

	return &updated
}
