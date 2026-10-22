package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/bluele/gcache"
	"github.com/google/uuid"
)

const (
	// maxFailedAttempts is the number of consecutive wrong edit-code attempts after
	// which this resource/caller pair is locked for failLockoutDur.
	maxFailedAttempts = 10

	// failLockoutDur runs from the last 403 for this resource/caller pair.
	// Requests rejected with 429 do not extend the lockout.
	failLockoutDur = 5 * time.Minute

	// maxTrackedFailures bounds the number of resource/caller pairs tracked at once.
	// ARC evicts the least-recently-used entry when the cache is full.
	maxTrackedFailures = 10_000
)

// Use the authenticated user ID, shared by all their sessions and API tokens.
// uuid.Nil gives anonymous callers one separate bucket per resource. Never key
// on unverified credential strings: rotating bogus tokens must not reset a bucket.
type failLockoutKey struct {
	ID       string
	CallerID uuid.UUID
}

//nolint:ireturn // gcache.Cache is an interface by design; no concrete type is exposed by the library
func newFailLockoutCache() gcache.Cache {
	return gcache.New(maxTrackedFailures).ARC().Build()
}

// withFailLockout wraps note/link PUT and DELETE routes after authentication.
// Failures and successful resets affect only the current resource/caller pair,
// so another user or an anonymous attack cannot block the authenticated owner.
func withFailLockout(cache gcache.Cache, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := failLockoutKey{ID: r.PathValue("id"), CallerID: callerIDFromCtx(r.Context())}

		cached, err := cache.Get(key)
		if err == nil {
			if count, ok := cached.(int); ok && count >= maxFailedAttempts {
				writeJSONErrorStatus(w, r, log, http.StatusTooManyRequests, "too many failed attempts, try again later")

				return
			}
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK, written: false}
		next.ServeHTTP(rec, r)

		switch {
		case rec.status == http.StatusForbidden:
			count := 0

			prev, getErr := cache.Get(key)
			if getErr == nil {
				if prevCount, ok := prev.(int); ok {
					count = prevCount
				}
			}

			setErr := cache.SetWithExpire(key, count+1, failLockoutDur)
			if setErr != nil {
				// ARC SetWithExpire never returns an error in practice; skip silently.
				return
			}
		case rec.status < http.StatusBadRequest:
			// Success resets only this caller's failures, preserving other buckets.
			cache.Remove(key)
		}
	})
}
