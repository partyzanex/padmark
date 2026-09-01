package http

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseCIDR(tb testing.TB, cidr string) *net.IPNet {
	tb.Helper()

	_, network, err := net.ParseCIDR(cidr)
	require.NoError(tb, err)

	return network
}

func newReq(remoteAddr, xff, xri string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	req.RemoteAddr = remoteAddr

	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}

	if xri != "" {
		req.Header.Set("X-Real-IP", xri)
	}

	return req
}

// clientIP

func TestClientIP_NoProxies_UsesRemoteAddr(t *testing.T) {
	req := newReq("10.0.0.1:9000", "1.2.3.4", "")
	assert.Equal(t, "10.0.0.1", clientIP(req, nil))
}

func TestClientIP_TrustedProxy_XForwardedFor(t *testing.T) {
	req := newReq("10.0.0.2:5000", "203.0.113.5, 10.0.0.2", "")
	assert.Equal(t, "203.0.113.5", clientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_XRealIP(t *testing.T) {
	req := newReq("10.0.0.2:5000", "", "203.0.113.7")
	assert.Equal(t, "203.0.113.7", clientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_UntrustedProxy_HeaderIgnored(t *testing.T) {
	req := newReq("10.0.0.1:9000", "1.2.3.4", "")
	assert.Equal(t, "10.0.0.1", clientIP(req, []*net.IPNet{parseCIDR(t, "192.168.0.0/16")}))
}

func TestClientIP_MalformedRemoteAddr(t *testing.T) {
	req := newReq("not-valid", "", "")
	assert.Equal(t, "not-valid", clientIP(req, nil))
}

func TestClientIP_TrustedProxy_SpoofedLeftmostXFFIgnored(t *testing.T) {
	// A client behind the trusted proxy plants "1.2.3.4" in XFF; the proxy appends the real
	// client IP "198.51.100.9". The rightmost non-trusted entry must win, otherwise rotating
	// the leftmost value grants a fresh rate-limit bucket per request.
	req := newReq("10.0.0.2:5000", "1.2.3.4, 198.51.100.9", "")
	assert.Equal(t, "198.51.100.9", clientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_PlantedTrustedEntryDoesNotUnmaskAttackerValue(t *testing.T) {
	// The client lists a fake trusted-proxy IP to the left; the walk stops at the entry the
	// real proxy appended and never reaches the planted one.
	req := newReq("10.0.0.2:5000", "203.0.113.5, 10.0.0.2, 198.51.100.9", "")
	assert.Equal(t, "198.51.100.9", clientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_MalformedXFEntryFallsBackToRemoteAddr(t *testing.T) {
	// A malformed entry breaks the append chain, so nothing in the list is trustworthy;
	// RemoteAddr (the proxy's own IP) is the only safe bucket key.
	req := newReq("10.0.0.2:5000", "8.8.8.8, junk", "")
	assert.Equal(t, "10.0.0.2", clientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_AllXFFEntriesTrustedFallsBackToRemoteAddr(t *testing.T) {
	req := newReq("10.0.0.2:5000", "10.0.0.3, 10.0.0.2", "")
	assert.Equal(t, "10.0.0.2", clientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_InvalidXRealIPFallsBackToRemoteAddr(t *testing.T) {
	req := newReq("10.0.0.2:5000", "", "not-an-ip")
	assert.Equal(t, "10.0.0.2", clientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

// isTrustedProxy

func TestIsTrustedProxy_Contained(t *testing.T) {
	assert.True(t, isTrustedProxy("10.0.0.1", []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestIsTrustedProxy_NotContained(t *testing.T) {
	assert.False(t, isTrustedProxy("192.168.1.1", []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestIsTrustedProxy_InvalidIP(t *testing.T) {
	assert.False(t, isTrustedProxy("not-an-ip", []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

// withTOTPRateLimit

func TestWithTOTPRateLimit_AllowsUnderLimit(t *testing.T) {
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		w.WriteHeader(http.StatusOK)
	})
	handler := withTOTPRateLimit(nil, inner)

	for range totpRatePerMin {
		req := httptest.NewRequest(http.MethodPost, "/totp-login", nil)
		req.RemoteAddr = "1.2.3.4:9000"

		rec := httptest.NewRecorder()
		handler(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	}

	assert.Equal(t, totpRatePerMin, calls)
}

func TestWithTOTPRateLimit_BlocksOverLimit(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := withTOTPRateLimit(nil, inner)

	for range totpRatePerMin {
		req := httptest.NewRequest(http.MethodPost, "/totp-login", nil)
		req.RemoteAddr = "5.5.5.5:9000"

		handler(httptest.NewRecorder(), req)
	}

	// one more must be rate-limited
	req := httptest.NewRequest(http.MethodPost, "/totp-login", nil)
	req.RemoteAddr = "5.5.5.5:9000"

	rec := httptest.NewRecorder()
	handler(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestWithTOTPRateLimit_DifferentIPs_IndependentBuckets(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := withTOTPRateLimit(nil, inner)

	// exhaust bucket for IP A
	for range totpRatePerMin {
		req := httptest.NewRequest(http.MethodPost, "/totp-login", nil)
		req.RemoteAddr = "9.9.9.9:1000"
		handler(httptest.NewRecorder(), req)
	}

	// IP B must still be allowed
	req := httptest.NewRequest(http.MethodPost, "/totp-login", nil)
	req.RemoteAddr = "8.8.8.8:1000"

	rec := httptest.NewRecorder()
	handler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
