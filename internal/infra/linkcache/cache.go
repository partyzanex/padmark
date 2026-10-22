// Package linkcache provides an in-memory LRU cache for frequently-followed ("hot") short links,
// implementing links.Cache (see links.Manager.WithCache). Reuses github.com/bluele/gcache — an
// already-vendored dependency, the same library backing the fail-lockout cache in
// internal/adapters/http/middleware_faillock.go — rather than introducing a second LRU library
// for one more use of the same shape.
package linkcache

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"time"

	"github.com/bluele/gcache"
)

const (
	// DefaultSize bounds how many links are cached simultaneously; LRU evicts the
	// least-recently-followed entry once full.
	DefaultSize = 10_000
	// DefaultTTL bounds retention of decrypted public URLs. It is not a privacy
	// staleness window: the manager reads storage metadata and checks ciphertext
	// before each reuse, including on other replicas.
	DefaultTTL = 30 * time.Second
)

// Cache stores decrypted public targets by hashed record ID and ciphertext fingerprint.
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

type entry struct {
	targetURL   string
	fingerprint [sha256.Size]byte
}

// Get returns a target only for the ciphertext read from storage by this caller.
// An update on another replica changes the fingerprint and forces a miss.
func (c *Cache) Get(id, ciphertext string) (string, bool) {
	v, err := c.gc.Get(id)
	if err != nil {
		return "", false
	}

	cached, ok := v.(entry)
	if !ok || cached.fingerprint != sha256.Sum256([]byte(ciphertext)) {
		return "", false
	}

	return cached.targetURL, true
}

// Set stores an immutable target plus fingerprint, without authorization metadata
// or another full copy of the ciphertext.
//
// Set only fails when the cache was built with a LoaderFunc that itself errors — this Cache
// never configures one, so the error is unreachable in practice. We log it anyway so a future
// change to gcache cannot silently swallow the failure.
func (c *Cache) Set(ctx context.Context, id, ciphertext, targetURL string) {
	err := c.gc.Set(id, entry{targetURL: targetURL, fingerprint: sha256.Sum256([]byte(ciphertext))})
	if err != nil {
		c.log.WarnContext(ctx, "link cache set failed", "id", id, "err", err)
	}
}

// Delete evicts id, if present. A no-op miss (nothing cached for id) is not an error.
func (c *Cache) Delete(id string) {
	c.gc.Remove(id)
}
