package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The lockout caches (NewFailLockoutCache, NewLoginLockout) are bounded gcache ARC caches. ARC
// keeps entries seen once in its recency list (T1) and entries seen at least twice in its
// frequency list (T2). A locked bucket has been hit maxFailedAttempts / maxLoginFailures times,
// so it lives in T2. Flooding the cache with keys that are hit only once churns T1 and never
// reaches it; flooding with keys hit twice each competes for T2 and pushes the locked bucket
// out once the cache size is reached — and an evicted bucket starts over at zero failures.
//
// Measured with the handlers below: 10,000 distinct keys × 1 failure (and up to 40,000) never
// evict the lock; 10,000 distinct keys × 2 failures (20,000 requests) always do, 9,999 do not.

// evictionAttacker is the anonymous client driving the edit-code lockout in these tests.
const evictionAttacker = "198.51.100.7:4000"

// failLockoutAttempt sends one anonymous PUT for resource id through handler and returns the
// status; the upstream answers 403 (a wrong edit code) to everything that gets through.
func failLockoutAttempt(handler http.Handler, id string) int {
	req := httptest.NewRequest(http.MethodPut, "/notes/"+id, nil)
	req.SetPathValue("id", id)
	req.RemoteAddr = evictionAttacker

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec.Code
}

// lockedFailLockout returns a WithFailLockout handler in which the attacker has already locked
// the bucket of resource "victim".
func lockedFailLockout(t *testing.T) http.Handler {
	t.Helper()

	handler := WithFailLockout(NewFailLockoutCache(), nil, testDiscardLog(), alwaysForbidden())

	for range maxFailedAttempts {
		require.Equal(t, http.StatusForbidden, failLockoutAttempt(handler, "victim"))
	}

	return handler
}

// floodFailLockout produces hits wrong-edit-code failures on each of keys distinct resources —
// e.g. notes the attacker created itself, which answer 403 to a wrong code.
func floodFailLockout(handler http.Handler, keys, hits int) {
	for idx := range keys {
		id := "own-" + strconv.Itoa(idx)

		for range hits {
			failLockoutAttempt(handler, id)
		}
	}
}

// TestFailLockout_SingleFailureFlood_DoesNotEvictLock pins the part of the eviction question that
// is safe today: failing once on each of 2×maxTrackedFailures fresh resources only churns ARC's
// recency list and leaves the locked bucket in place.
func TestFailLockout_SingleFailureFlood_DoesNotEvictLock(t *testing.T) {
	handler := lockedFailLockout(t)

	floodFailLockout(handler, 2*maxTrackedFailures, 1)

	assert.Equal(t, http.StatusTooManyRequests, failLockoutAttempt(handler, "victim"))
}

// TestFailLockout_DoubleFailureFlood_KeepsLock is INTENTIONALLY RED today (audit §9, "is the
// 10k-entry ARC lockout cache evictable by 403s on one's own notes"): yes. Failing twice on
// each of maxTrackedFailures (10,000) distinct resources — 20,000 requests — evicts the locked
// bucket, and the attacker gets a fresh maxFailedAttempts guesses at the victim resource.
//
// Desired: a bucket that reached the lock threshold stays locked for failLockoutDur no matter
// how many other buckets are created meanwhile.
func TestFailLockout_DoubleFailureFlood_KeepsLock(t *testing.T) {
	handler := lockedFailLockout(t)

	floodFailLockout(handler, maxTrackedFailures, 2)

	assert.Equal(t, http.StatusTooManyRequests, failLockoutAttempt(handler, "victim"),
		"flooding the lockout cache with other buckets must not unlock the victim resource")
}

// lockedLoginLockout returns a login-lockout fixture in which username "victim" is locked.
func lockedLoginLockout(t *testing.T) *loginLockFixture {
	t.Helper()

	fix := newLoginLockFixture()
	fix.fail(t, "victim", maxLoginFailures)
	require.Equal(t, http.StatusTooManyRequests, fix.attempt("victim", "bad").Code)

	return fix
}

// sprayLoginLockout makes attempts failed sign-ins for each of usernames distinct usernames.
func sprayLoginLockout(fix *loginLockFixture, usernames, attempts int) {
	for idx := range usernames {
		name := "spray-" + strconv.Itoa(idx)

		for range attempts {
			fix.attempt(name, "bad")
		}
	}
}

// TestLoginLockout_SingleAttemptSpray_DoesNotEvictLock: one failed sign-in for each of
// 2×maxTrackedLogins usernames leaves a locked username locked (safe today).
func TestLoginLockout_SingleAttemptSpray_DoesNotEvictLock(t *testing.T) {
	fix := lockedLoginLockout(t)

	sprayLoginLockout(fix, 2*maxTrackedLogins, 1)

	assert.Equal(t, http.StatusTooManyRequests, fix.attempt("victim", "bad").Code)
}

// TestLoginLockout_DoubleAttemptSpray_KeepsLock is INTENTIONALLY RED today: two failed sign-ins
// for each of maxTrackedLogins (10,000) distinct usernames — 20,000 POST /totp-login requests,
// spread over as many IPs as the per-IP limiter requires — evict the locked username, resetting
// its counter and handing the attacker another maxLoginFailures guesses at that account.
//
// Desired: a username that reached the lock threshold stays locked for loginLockoutDur no matter
// how many other usernames are sprayed meanwhile.
func TestLoginLockout_DoubleAttemptSpray_KeepsLock(t *testing.T) {
	fix := lockedLoginLockout(t)

	sprayLoginLockout(fix, maxTrackedLogins, 2)

	assert.Equal(t, http.StatusTooManyRequests, fix.attempt("victim", "bad").Code,
		"spraying other usernames must not unlock the victim username")
}
