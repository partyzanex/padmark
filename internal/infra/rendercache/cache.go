// Package rendercache provides a small in-memory LRU cache of rendered note HTML, implementing
// notes.RenderCache (see notes.Manager.WithRenderCache). It reuses github.com/bluele/gcache, the
// library already backing internal/infra/linkcache and the fail-lockout cache, instead of adding
// another cache dependency.
package rendercache

import (
	"log/slog"
	"time"

	"github.com/bluele/gcache"
)

const (
	// DefaultSize bounds how many rendered notes are held at once. Together with the manager's
	// per-entry size limit it keeps the worst case at a few tens of megabytes.
	DefaultSize = 128
	// DefaultTTL bounds how long rendered HTML (which can contain private note content) stays in
	// memory. It is a memory-retention window, not a correctness one: entries are also keyed by
	// the note's updated_at and dropped on update and delete.
	DefaultTTL = 2 * time.Minute
)

// Cache stores rendered HTML per hashed note id together with the updated_at it was rendered at.
type Cache struct {
	gc  gcache.Cache
	log *slog.Logger
}

// New creates a Cache holding up to size entries (LRU-evicted) for ttl each. log surfaces
// unexpected gcache failures.
func New(size int, ttl time.Duration, log *slog.Logger) *Cache {
	return &Cache{gc: gcache.New(size).LRU().Expiration(ttl).Build(), log: log}
}

// NewDefault creates a Cache with DefaultSize entries of DefaultTTL each.
func NewDefault(log *slog.Logger) *Cache {
	return New(DefaultSize, DefaultTTL, log)
}

type entry struct {
	updatedAt time.Time
	html      string
}

// Get returns the HTML cached for id when it was rendered at exactly updatedAt.
func (c *Cache) Get(id string, updatedAt time.Time) (string, bool) {
	value, err := c.gc.Get(id)
	if err != nil {
		return "", false
	}

	cached, ok := value.(entry)
	if !ok || !cached.updatedAt.Equal(updatedAt) {
		return "", false
	}

	return cached.html, true
}

// Set stores html as the render of id at updatedAt, replacing any earlier entry for id.
// gcache's Set only fails when a LoaderFunc errors and none is configured here, so the error is
// unreachable today; it is logged so a future gcache change cannot swallow it silently.
func (c *Cache) Set(id string, updatedAt time.Time, html string) {
	err := c.gc.Set(id, entry{updatedAt: updatedAt, html: html})
	if err != nil {
		c.log.Warn("render cache set failed", "id", id, "err", err)
	}
}

// Delete drops the entry for id; a missing entry is not an error.
func (c *Cache) Delete(id string) {
	c.gc.Remove(id)
}
