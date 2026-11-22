package links

import (
	"context"
	"fmt"
	"time"

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
// creation time, burn-after-click setting, or click counter. The checks have the sequence of
// files.Manager.Update: the reader must see the link (domain.ErrForbidden), the link must not be
// expired (domain.ErrExpired), and the rules of checkMutation apply: the reader must be its owner
// or give the original edit code.
func (m *Manager) Update(
	ctx context.Context, slug, editCode string, reader domain.Reader, targetURL string, opts UpdateOptions,
) (*domain.Link, error) {
	validURL, err := validateTargetURL(targetURL)
	if err != nil {
		return nil, fmt.Errorf("validate target url: %w", err)
	}

	// No lock: omitted fields stay because the storage patch does not write them, and a stale
	// cache fill is never served (see resolveLink).
	dbID := domain.HashSlug(slug)

	existing, err := m.storage.GetByID(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("update link: %w", err)
	}

	// Privacy comes before expiry, so that a hidden link gives the same answer in each state.
	if !existing.VisibleTo(reader) {
		return nil, domain.ErrForbidden
	}

	// An expired link is already gone for readers, so an update must not extend its life.
	if existing.Expired(time.Now()) {
		return nil, domain.ErrExpired
	}

	err = m.checkMutation(existing, editCode, reader)
	if err != nil {
		return nil, err
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
