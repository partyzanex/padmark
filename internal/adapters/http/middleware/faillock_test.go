package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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

	handler := WithFailLockout(cache, nil, testDiscardLog, inner)

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
	handler := WithFailLockout(cache, nil, testDiscardLog, inner)
	send := func(caller uuid.UUID, success bool) int {
		req := httptest.NewRequest(http.MethodPut, "/notes/resource", nil)
		req.SetPathValue("id", "resource")

		if caller != uuid.Nil {
			req = req.WithContext(context.WithValue(req.Context(), keyUser, &domain.User{ID: caller}))
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

	handler := WithFailLockout(cache, nil, testDiscardLog, inner)

	req := httptest.NewRequest(http.MethodPut, "/notes/almost-locked", nil)
	req.SetPathValue("id", "almost-locked")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}
