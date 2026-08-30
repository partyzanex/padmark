// Package linkcache provides an in-memory LRU cache for frequently-followed ("hot") short links,
// implementing links.Cache (see links.Manager.WithCache). Reuses github.com/bluele/gcache — an
// already-vendored dependency, the same library backing the fail-lockout cache in
// internal/adapters/http/middleware_faillock.go — rather than introducing a second LRU library
// for one more use of the same shape.
package linkcache

import (
	"context"
	"log/slog"
	"time"

	"github.com/bluele/gcache"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	// DefaultSize bounds how many links are cached simultaneously; LRU evicts the
	// least-recently-followed entry once full.
	DefaultSize = 10_000
	// DefaultTTL bounds how long a cached link may be served without checking storage again —
	// the window in which an out-of-band delete (not through links.Manager.Delete, which
	// invalidates explicitly) could be served stale.
	DefaultTTL = 30 * time.Second
)

// Cache is a gcache-backed LRU cache of decrypted links, keyed by domain.HashSlug(id).
type Cache struct {
	gc  gcache.Cache
	log *slog.Logger
}

// New creates a Cache holding up to size entries (LRU-evicted) for ttl each. log is used to
// surface unexpected gcache failures; pass slog.Default() if no project logger is at hand.
func New(size int, ttl time.Duration, log *slog.Logger) *Cache {
	return &Cache{gc: gcache.New(size).LRU().Expiration(ttl).Build(), log: log}
}

// NewDefault creates a Cache sized for a "hot links" workload (DefaultSize entries, DefaultTTL
// each) without needing per-deployment tuning.
func NewDefault(log *slog.Logger) *Cache {
	return New(DefaultSize, DefaultTTL, log)
}

// Get returns a clone of the cached link for id, or (nil, false) on a miss (absent or expired).
// A clone, not the stored pointer, is returned so a caller's in-place mutation (e.g.
// links.Manager.Redirect incrementing its local copy's Views) can never corrupt the cached entry.
func (c *Cache) Get(id string) (*domain.Link, bool) {
	v, err := c.gc.Get(id)
	if err != nil {
		return nil, false
	}

	link, ok := v.(*domain.Link)
	if !ok {
		return nil, false
	}

	clone := *link

	return &clone, true
}

// Set stores a clone of link under id — see Get's doc comment for why a clone, not the caller's
// own pointer, is stored.
//
// Set only fails when the cache was built with a LoaderFunc that itself errors — this Cache
// never configures one, so the error is unreachable in practice. We log it anyway so a future
// change to gcache cannot silently swallow the failure.
func (c *Cache) Set(ctx context.Context, id string, link *domain.Link) {
	clone := *link

	err := c.gc.Set(id, &clone)
	if err != nil {
		c.log.WarnContext(ctx, "link cache set failed", "id", id, "err", err)
	}
}

// Delete evicts id, if present. A no-op miss (nothing cached for id) is not an error.
func (c *Cache) Delete(id string) {
	c.gc.Remove(id)
}
