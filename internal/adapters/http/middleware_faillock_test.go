package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithFailLockout_LockedOut_ReturnsJSONError pins the OpenAPI-promised error shape
// ({"message": ...} JSON, not net/http's default text/plain) for the 429 a locked-out note ID
// gets — see errors.go's writeJSONErrorStatus.
func TestWithFailLockout_LockedOut_ReturnsJSONError(t *testing.T) {
	cache := newFailLockoutCache()

	setErr := cache.Set("locked-note", maxFailedAttempts)
	require.NoError(t, setErr)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream handler must not be reached once locked out")
		w.WriteHeader(http.StatusOK)
	})

	handler := withFailLockout(cache, testDiscardLog, inner)

	req := httptest.NewRequest(http.MethodPut, "/notes/locked-note", nil)
	req.SetPathValue("id", "locked-note")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	//nolint:testifylint // Content-Type header string, not a JSON body; JSONEq suggestion misfires on "mimeJSON"
	assert.Equal(t, mimeJSON, rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"message":"too many failed attempts, try again later"}`, rec.Body.String())
}

// TestWithFailLockout_UnderThreshold_ReachesUpstream is the control case: below
// maxFailedAttempts the request passes through untouched.
func TestWithFailLockout_UnderThreshold_ReachesUpstream(t *testing.T) {
	cache := newFailLockoutCache()

	setErr := cache.Set("almost-locked", maxFailedAttempts-1)
	require.NoError(t, setErr)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true

		w.WriteHeader(http.StatusOK)
	})

	handler := withFailLockout(cache, testDiscardLog, inner)

	req := httptest.NewRequest(http.MethodPut, "/notes/almost-locked", nil)
	req.SetPathValue("id", "almost-locked")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}
