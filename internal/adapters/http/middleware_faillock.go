package http

import (
	"net/http"
	"sync"
	"time"

	"github.com/bluele/gcache"
)

const (
	// maxFailedAttempts is the number of consecutive wrong edit-code attempts after
	// which the note ID is locked for failLockoutDur.
	maxFailedAttempts = 10

	// failLockoutDur is how long a note ID stays locked after hitting maxFailedAttempts.
	// The TTL is refreshed on every subsequent failure, so sustained brute-forcing
	// keeps the lockout alive.
	failLockoutDur = 5 * time.Minute

	// maxTrackedFailures is the maximum number of note IDs tracked simultaneously.
	// ARC evicts the least-recently-used entry when the cache is full.
	maxTrackedFailures = 10_000
)

// noteFailLockout tracks consecutive failed edit-code attempts per note ID.
// After maxFailedAttempts failures it returns true from isLocked, causing the
// withFailLockout middleware to respond 429 instead of forwarding the request.
type noteFailLockout struct {
	cache gcache.Cache
	mu    sync.Mutex
}

func newNoteFailLockout() *noteFailLockout {
	return &noteFailLockout{
		cache: gcache.New(maxTrackedFailures).ARC().Build(),
	}
}

func (fl *noteFailLockout) isLocked(id string) bool {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	cached, err := fl.cache.Get(id)
	if err != nil {
		return false
	}

	count, ok := cached.(int)

	return ok && count >= maxFailedAttempts
}

func (fl *noteFailLockout) recordFailure(id string) {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	count := 0

	cached, getErr := fl.cache.Get(id)
	if getErr == nil {
		if prevCount, ok := cached.(int); ok {
			count = prevCount
		}
	}

	setErr := fl.cache.SetWithExpire(id, count+1, failLockoutDur)
	if setErr != nil {
		// ARC SetWithExpire never returns an error in practice; skip silently.
		return
	}
}

// withFailLockout wraps edit-code-protected routes (PUT /notes/{id},
// DELETE /notes/{id}). It rejects requests with 429 when the note is locked,
// and increments the failure counter when the upstream handler responds 403.
func withFailLockout(lockout *noteFailLockout, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		if lockout.isLocked(id) {
			http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)

			return
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		if rec.status == http.StatusForbidden {
			lockout.recordFailure(id)
		}
	})
}
