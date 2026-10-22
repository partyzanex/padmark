package http

import (
	"sync"
	"time"
)

// maxLockedBuckets is the hard cap on simultaneously pinned locked buckets per lockedSet.
// Entries expire after the lockout duration, so the count is naturally bounded by the number of
// buckets that can be driven to the lock threshold within one duration (threshold failures each).
// The cap only protects memory against a pathological rate: reaching it costs an attacker
// maxFailedAttempts failures per bucket for 100k buckets, and even then the soonest-to-expire
// lock is the one dropped.
const maxLockedBuckets = 100_000

// lockedSet pins locked buckets outside the ARC counter caches. ARC keeps a locked bucket in its
// frequency list, where a flood of other buckets hit twice each (cheap) pushes it out, and an
// evicted bucket restarts at zero failures. Locks therefore live here until they expire and are
// never subject to the churn of the counter cache. It is safe for concurrent use.
type lockedSet[K comparable] struct {
	until map[K]time.Time
	mu    sync.Mutex
}

func newLockedSet[K comparable]() *lockedSet[K] {
	return &lockedSet[K]{until: make(map[K]time.Time), mu: sync.Mutex{}}
}

// remaining returns the time left on key's lock, or false when key is not locked at now.
func (s *lockedSet[K]) remaining(key K, now time.Time) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	expires, ok := s.until[key]
	if !ok {
		return 0, false
	}

	if !expires.After(now) {
		delete(s.until, key)

		return 0, false
	}

	return expires.Sub(now), true
}

// lock pins key until expires. When the set is full, expired entries are dropped first and then
// the entry closest to expiry, so memory stays bounded by maxLockedBuckets.
func (s *lockedSet[K]) lock(key K, now, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.until[key]; !exists && len(s.until) >= maxLockedBuckets {
		s.evictLocked(now)
	}

	s.until[key] = expires
}

// evictLocked frees room: all expired entries, or the soonest-to-expire one if none expired.
// The caller must hold mu.
func (s *lockedSet[K]) evictLocked(now time.Time) {
	var (
		oldest    K
		oldestExp time.Time
		found     bool
	)

	for key, expires := range s.until {
		if !expires.After(now) {
			delete(s.until, key)

			continue
		}

		if !found || expires.Before(oldestExp) {
			oldest, oldestExp, found = key, expires, true
		}
	}

	if found && len(s.until) >= maxLockedBuckets {
		delete(s.until, oldest)
	}
}

// unlock removes key's lock, if any.
func (s *lockedSet[K]) unlock(key K) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.until, key)
}
