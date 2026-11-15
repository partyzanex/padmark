package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	time "time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestWithFailLockout_LockedOut_ReturnsJSONError pins the OpenAPI-promised error shape
// ({"message": ...} JSON, not net/http's default text/plain) for the 429 a locked-out note ID
// gets — see errors.go's WriteJSONErrorStatus.
func TestWithFailLockout_LockedOut_ReturnsJSONError(t *testing.T) {
	cache := NewFailLockoutCache()

	setErr := cache.Set(failLockoutKey{ID: "locked-note", IP: httptestRemoteIP, CallerID: uuid.Nil}, maxFailedAttempts)
	require.NoError(t, setErr)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream handler must not be reached once locked out")
		w.WriteHeader(http.StatusOK)
	})

	handler := WithFailLockout(cache, nil, testDiscardLog(), inner)

	req := httptest.NewRequest(http.MethodPut, "/notes/locked-note", nil)
	req.SetPathValue("id", "locked-note")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"message":"too many failed attempts, try again later"}`, rec.Body.String())
}

func TestWithFailLockout_CallerSuccessDoesNotResetOthers(t *testing.T) {
	cache := NewFailLockoutCache()
	first, second := uuid.New(), uuid.New()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("success") {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusForbidden)
		}
	})
	handler := WithFailLockout(cache, nil, testDiscardLog(), inner)
	send := func(caller uuid.UUID, success bool) int {
		req := httptest.NewRequest(http.MethodPut, "/notes/resource", nil)
		req.SetPathValue("id", "resource")

		if caller != uuid.Nil {
			req = req.WithContext(context.WithValue(req.Context(), keyUser, &domain.User{
				ID:        caller,
				CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
				PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
			}))
		}

		if success {
			req.URL.RawQuery = "success"
		}

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		return rec.Code
	}

	for range maxFailedAttempts - 1 {
		require.Equal(t, http.StatusForbidden, send(first, false))
		require.Equal(t, http.StatusForbidden, send(second, false))
		require.Equal(t, http.StatusForbidden, send(uuid.Nil, false))
	}

	require.Equal(t, http.StatusNoContent, send(first, true))
	require.Equal(t, http.StatusForbidden, send(first, false))
	require.Equal(t, http.StatusForbidden, send(second, false))
	require.Equal(t, http.StatusForbidden, send(uuid.Nil, false))
	require.Equal(t, http.StatusTooManyRequests, send(second, false))
	require.Equal(t, http.StatusTooManyRequests, send(uuid.Nil, false))
	require.Equal(t, http.StatusForbidden, send(first, false))
}

// TestWithFailLockout_UnderThreshold_ReachesUpstream is the control case: below
// maxFailedAttempts the request passes through untouched.
func TestWithFailLockout_UnderThreshold_ReachesUpstream(t *testing.T) {
	cache := NewFailLockoutCache()

	setErr := cache.Set(failLockoutKey{ID: "almost-locked", IP: httptestRemoteIP, CallerID: uuid.Nil}, maxFailedAttempts-1)
	require.NoError(t, setErr)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true

		w.WriteHeader(http.StatusOK)
	})

	handler := WithFailLockout(cache, nil, testDiscardLog(), inner)

	req := httptest.NewRequest(http.MethodPut, "/notes/almost-locked", nil)
	req.SetPathValue("id", "almost-locked")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestWithFailLockout_ParallelBurst_StopsAtThreshold covers audit finding R8: attempts are counted
// before the edit-code check, so a parallel burst from one bucket passes at most
// maxFailedAttempts requests to the handler, and the rest get 429.
func TestWithFailLockout_ParallelBurst_StopsAtThreshold(t *testing.T) {
	t.Parallel()

	const requests = 20

	var (
		entered atomic.Int32
		wg      sync.WaitGroup
	)

	release := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered.Add(1)
		<-release
		w.WriteHeader(http.StatusForbidden)
	})
	handler := WithFailLockout(NewFailLockoutCache(), nil, testDiscardLog(), inner)

	codes := make(chan int, requests)

	for range requests {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodPut, "/notes/same", nil)
			req.SetPathValue("id", "same")

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			codes <- rec.Code
		})
	}

	// The rejected requests finish without the handler; wait for them before the release.
	require.Eventually(t, func() bool { return len(codes) == requests-maxFailedAttempts },
		5*time.Second, time.Millisecond)
	close(release)
	wg.Wait()
	close(codes)

	assert.Equal(t, int32(maxFailedAttempts), entered.Load())

	tooMany := 0

	for code := range codes {
		if code == http.StatusTooManyRequests {
			tooMany++
		}
	}

	assert.Equal(t, requests-maxFailedAttempts, tooMany)
}

// TestWithFailLockout_NonFailureRefundsAttempt verifies that responses other than 403, such as
// the 401 of the privacy gate or a 404, take the reserved attempt back and never lock the bucket.
func TestWithFailLockout_NonFailureRefundsAttempt(t *testing.T) {
	t.Parallel()

	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		w.WriteHeader(http.StatusNotFound)
	})
	handler := WithFailLockout(NewFailLockoutCache(), nil, testDiscardLog(), inner)

	for range 2 * maxFailedAttempts {
		req := httptest.NewRequest(http.MethodDelete, "/notes/missing", nil)
		req.SetPathValue("id", "missing")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	}

	assert.Equal(t, 2*maxFailedAttempts, calls)
}
