package middleware

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
)

// httptestRemoteIP is the client IP httptest.NewRequest assigns (RemoteAddr "192.0.2.1:1234"),
// i.e. the anonymous bucket's IP for requests that do not set RemoteAddr themselves.
const httptestRemoteIP = "192.0.2.1"

// failLockoutProbe drives WithFailLockout around an upstream that answers 403 (a wrong edit
// code) for every request, reporting the status a caller identified by (remoteAddr, callerID)
// gets for the shared resource "resource".
func failLockoutProbe(handler http.Handler, remoteAddr string, callerID uuid.UUID) int {
	req := httptest.NewRequest(http.MethodPut, "/notes/resource", nil)
	req.SetPathValue("id", "resource")
	req.RemoteAddr = remoteAddr

	if callerID != uuid.Nil {
		req = req.WithContext(context.WithValue(req.Context(), keyUser, &domain.User{ID: callerID}))
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec.Code
}

func alwaysForbidden() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
}

// TestFailLockout_AnonymousBucketIsPerIP pins finding L-6/M-8: an anonymous attacker at one IP
// who exhausts maxFailedAttempts must not lock the resource for an anonymous editor at another
// IP — anonymous buckets are keyed by client IP, not shared under uuid.Nil.
func TestFailLockout_AnonymousBucketIsPerIP(t *testing.T) {
	handler := WithFailLockout(NewFailLockoutCache(), nil, testDiscardLog, alwaysForbidden())

	for range maxFailedAttempts {
		require.Equal(t, http.StatusForbidden, failLockoutProbe(handler, "10.0.0.1:4000", uuid.Nil))
	}

	require.Equal(t, http.StatusTooManyRequests, failLockoutProbe(handler, "10.0.0.1:4001", uuid.Nil),
		"the attacking IP itself is locked, whatever source port it uses")
	require.Equal(t, http.StatusForbidden, failLockoutProbe(handler, "10.0.0.2:4000", uuid.Nil),
		"an anonymous caller at another IP must keep its own bucket")
}

// TestFailLockout_AuthenticatedBucketIgnoresIP pins the unchanged invariant for authenticated
// callers: their bucket is keyed by user ID alone, shared across every IP, session and token,
// and independent of anonymous buckets at the same IP.
func TestFailLockout_AuthenticatedBucketIgnoresIP(t *testing.T) {
	handler := WithFailLockout(NewFailLockoutCache(), nil, testDiscardLog, alwaysForbidden())
	user := uuid.New()

	for i := range maxFailedAttempts {
		addr := "10.0.0.1:4000"
		if i%2 == 1 {
			addr = "10.0.0.9:4000"
		}

		require.Equal(t, http.StatusForbidden, failLockoutProbe(handler, addr, user))
	}

	require.Equal(t, http.StatusTooManyRequests, failLockoutProbe(handler, "10.0.0.3:4000", user),
		"a user's failures from different IPs share one bucket")
	require.Equal(t, http.StatusForbidden, failLockoutProbe(handler, "10.0.0.1:4000", uuid.Nil),
		"the user's lockout must not spill into the anonymous bucket at the same IP")
}

// TestFailLockout_AnonymousIPv6GroupedBy64 pins that anonymous IPv6 callers share a bucket per
// /64: rotating addresses inside one prefix must not mint fresh buckets.
func TestFailLockout_AnonymousIPv6GroupedBy64(t *testing.T) {
	handler := WithFailLockout(NewFailLockoutCache(), nil, testDiscardLog, alwaysForbidden())

	for range maxFailedAttempts {
		require.Equal(t, http.StatusForbidden, failLockoutProbe(handler, "[2001:db8:1:2::1]:4000", uuid.Nil))
	}

	require.Equal(t, http.StatusTooManyRequests,
		failLockoutProbe(handler, "[2001:db8:1:2:ffff::9]:4000", uuid.Nil), "same /64 shares the bucket")
	require.Equal(t, http.StatusForbidden,
		failLockoutProbe(handler, "[2001:db8:1:3::1]:4000", uuid.Nil), "another /64 has its own bucket")
}

// TestFailLockout_AnonymousIPFromTrustedProxy pins that the anonymous bucket uses the same
// client-IP resolution as the rate limiter: X-Forwarded-For counts only from a trusted proxy.
func TestFailLockout_AnonymousIPFromTrustedProxy(t *testing.T) {
	_, proxyNet, err := net.ParseCIDR("10.1.0.0/16")
	require.NoError(t, err)

	handler := WithFailLockout(NewFailLockoutCache(), []*net.IPNet{proxyNet}, testDiscardLog, alwaysForbidden())
	probe := func(forwardedFor string) int {
		req := httptest.NewRequest(http.MethodPut, "/notes/resource", nil)
		req.SetPathValue("id", "resource")
		req.RemoteAddr = "10.1.0.5:4000"
		req.Header.Set("X-Forwarded-For", forwardedFor)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		return rec.Code
	}

	for range maxFailedAttempts {
		require.Equal(t, http.StatusForbidden, probe("203.0.113.7"))
	}

	require.Equal(t, http.StatusTooManyRequests, probe("203.0.113.7"))
	require.Equal(t, http.StatusForbidden, probe("203.0.113.8"),
		"distinct clients behind a trusted proxy keep distinct buckets")
}
