package http

import (
	"net/http"
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

//nolint:ireturn // gcache.Cache is an interface by design; no concrete type is exposed by the library
func newFailLockoutCache() gcache.Cache {
	return gcache.New(maxTrackedFailures).ARC().Build()
}

// withFailLockout wraps edit-code-protected routes (PUT /notes/{id},
// DELETE /notes/{id}). It rejects requests with 429 when the note is locked,
// and increments the failure counter when the upstream handler responds 403.
func withFailLockout(cache gcache.Cache, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		cached, err := cache.Get(id)
		if err == nil {
			if count, ok := cached.(int); ok && count >= maxFailedAttempts {
				http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)

				return
			}
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		if rec.status == http.StatusForbidden {
			count := 0

			prev, getErr := cache.Get(id)
			if getErr == nil {
				if prevCount, ok := prev.(int); ok {
					count = prevCount
				}
			}

			setErr := cache.SetWithExpire(id, count+1, failLockoutDur)
			if setErr != nil {
				// ARC SetWithExpire never returns an error in practice; skip silently.
				return
			}
		}
	})
}
