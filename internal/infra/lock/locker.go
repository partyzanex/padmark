// Package lock provides an in-process, per-key mutual exclusion primitive.
package lock

import (
	"context"
	"sync"
)

// entry is a single key's reference-counted mutex. refs tracks how many goroutines currently
// hold or are waiting for this key, so Locker can drop the map entry once nobody references it
// anymore instead of leaking one *entry per key forever.
type entry struct {
	mu   sync.Mutex
	refs int
}

// Locker serializes access to arbitrary string keys within one process. It backs
// notes.Manager and links.Manager's SlugLock: both resources share one Locker instance so a
// concurrent notes.Manager.Create and links.Manager.Create targeting the same slug serialize
// their preflight checks locally. Links also use it to coordinate local cache eviction.
//
// Locker is single-process only. Storage enforces global slug uniqueness across replicas
// through the shared slug_namespace primary key and transactional resource triggers.
type Locker struct {
	locks map[string]*entry
	mu    sync.Mutex
}

// NewLocker creates a Locker ready for use.
func NewLocker() *Locker {
	return &Locker{locks: make(map[string]*entry), mu: sync.Mutex{}}
}

// Lock blocks until key is exclusively held, then returns a function that releases it. The
// caller must call the returned function exactly once, typically via defer. ctx is accepted for
// interface symmetry with a future database-backed implementation (e.g. Postgres advisory
// locks); this in-process implementation never fails and does not respect cancellation, since
// every hold is a bounded local critical section, not an external wait.
func (l *Locker) Lock(_ context.Context, key string) (func(), error) {
	l.mu.Lock()

	ent, ok := l.locks[key]
	if !ok {
		ent = &entry{mu: sync.Mutex{}, refs: 0}
		l.locks[key] = ent
	}

	ent.refs++

	l.mu.Unlock()

	ent.mu.Lock()

	unlock := func() {
		ent.mu.Unlock()

		l.mu.Lock()

		ent.refs--
		if ent.refs == 0 {
			delete(l.locks, key)
		}

		l.mu.Unlock()
	}

	return unlock, nil
}
