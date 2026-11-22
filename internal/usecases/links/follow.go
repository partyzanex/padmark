package links

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// Inspect returns the link for reader without a side effect: privacy (domain.ErrForbidden),
// then expiry (domain.ErrExpired). It never consumes a burn_after_click link and never counts a
// view. The link page, GET /links/{id} and the editor use it. Unlike notes.Manager.Inspect, it
// does not delete an expired link: GET /links/{id} is a metadata read without side effects (the
// replica test in tests/auth pins this), and the next Follow deletes the link.
func (m *Manager) Inspect(ctx context.Context, slug string, reader domain.Reader) (*domain.Link, error) {
	link, err := m.visibleLink(ctx, slug, reader)
	if err != nil {
		return nil, err
	}

	if link.Expired(m.now()) {
		return nil, domain.ErrExpired
	}

	return link, nil
}

// visibleLink reads the link and applies the privacy rule for reader.
func (m *Manager) visibleLink(ctx context.Context, slug string, reader domain.Reader) (*domain.Link, error) {
	link, err := m.resolveLink(ctx, slug)
	if err != nil {
		return nil, err
	}

	if !link.VisibleTo(reader) {
		return nil, domain.ErrForbidden
	}

	return link, nil
}

// Follow resolves slug to its target URL for reader. It applies privacy (domain.ErrForbidden),
// then expiry (an expired link is deleted, and Follow returns domain.ErrExpired), then the burn
// intent: a burn_after_click link needs reader.AllowBurn, otherwise Follow returns
// domain.ErrBurnIntentRequired and the link stays. A burn link is consumed atomically, so of two
// parallel reads only one gets it. Another link counts the view. The consume and the counter
// check the privacy of the current row again in storage, so a concurrent privacy change cannot
// make an authorized public read consume or count a private replacement.
func (m *Manager) Follow(ctx context.Context, slug string, reader domain.Reader) (*domain.Link, error) {
	link, err := m.visibleLink(ctx, slug, reader)
	if err != nil {
		return nil, err
	}

	now := m.now()
	if link.Expired(now) {
		return nil, m.expireLink(ctx, slug, reader, now)
	}

	dbID := domain.HashSlug(slug)

	if link.BurnAfterClick {
		if !reader.AllowBurn {
			return nil, domain.ErrBurnIntentRequired
		}

		return m.consumeLink(ctx, slug, dbID, reader)
	}

	err = m.incrementRedirectViews(ctx, dbID, link, reader)
	if err != nil {
		return nil, err
	}

	m.metrics.IncRedirects()

	return link, nil
}

// expireLink deletes the expired link that the caller read. The snapshot can be old, so storage
// checks the expiry again in the DELETE: if the owner extended the link after the read, or a new
// live link took the ID, the row stays and only this request sees the link as expired. With
// privacy on, the DELETE also checks that the current row is visible to reader; a row that is not
// gives domain.ErrNotFound.
func (m *Manager) expireLink(ctx context.Context, slug string, reader domain.Reader, now time.Time) error {
	dbID := domain.HashSlug(slug)

	var err error

	if reader.SkipPrivacy {
		err = m.storage.DeleteExpired(ctx, dbID, now)
		if errors.Is(err, domain.ErrNotFound) {
			m.log.DebugContext(ctx, "expired link already deleted or extended", "id", dbID)

			err = nil
		}
	} else {
		_, err = m.storage.ConsumeVisible(ctx, dbID, reader.CallerID, reader.Authenticated, false, now)
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("expire link: %w", err)
		}
	}

	if err != nil {
		m.log.ErrorContext(ctx, "delete expired link", "id", dbID, "err", err)
	}

	m.evictCache(dbID)

	return domain.ErrExpired
}

// incrementRedirectViews counts the view. With privacy on, storage counts it only while the
// current row is visible to reader, and a row that is not gives domain.ErrNotFound. Other
// counter errors are statistics only: they are logged, and the redirect continues.
func (m *Manager) incrementRedirectViews(
	ctx context.Context, dbID string, link *domain.Link, reader domain.Reader,
) error {
	var err error

	if reader.SkipPrivacy {
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
