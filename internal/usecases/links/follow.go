package links

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// FollowOptions identifies the reader and whether the request deliberately follows the link.
type FollowOptions struct {
	CallerID      uuid.UUID
	Authenticated bool
	AllowBurn     bool
}

// Follow checks access on the same snapshot that supplies the target URL. Destructive reads
// and view increments also enforce visibility atomically in storage, so a concurrent privacy
// change cannot make an authorized public read consume or update a private replacement.
func (m *Manager) Follow(ctx context.Context, slug string, options FollowOptions) (*domain.Link, error) {
	link, err := m.resolveLink(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("follow link: %w", err)
	}

	if !link.VisibleTo(options.CallerID, options.Authenticated) {
		return nil, domain.ErrForbidden
	}

	operationResult, operationErr := m.redirectLink(ctx, slug, link,
		redirectAccess{reader: &options, allowBurn: options.AllowBurn})
	if operationErr != nil {
		return operationResult, fmt.Errorf("redirect link: %w", operationErr)
	}

	return operationResult, nil
}

func (m *Manager) expireLink(ctx context.Context, slug, dbID string, reader *FollowOptions) error {
	var err error

	if reader == nil {
		err = m.storage.Delete(ctx, dbID)
	} else {
		_, err = m.storage.ConsumeVisible(ctx, dbID, reader.CallerID, reader.Authenticated, false)
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("expire link: %w", err)
		}
	}

	if err != nil {
		m.log.ErrorContext(ctx, "delete expired link", "id", domain.HashSlug(slug), "err", err)
	}

	m.evictCache(dbID)

	return domain.ErrExpired
}

func (m *Manager) incrementRedirectViews(
	ctx context.Context, dbID string, link *domain.Link, reader *FollowOptions,
) error {
	var err error

	if reader == nil {
		err = m.storage.IncrementViews(ctx, dbID)
	} else {
		err = m.storage.IncrementViewsVisible(ctx, dbID, reader.CallerID, reader.Authenticated)
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("increment link views: %w", err)
		}
	}

	if err != nil {
		m.log.ErrorContext(ctx, "increment link views", "id", dbID, "err", err)
	} else {
		link.Views++
	}

	return nil
}
