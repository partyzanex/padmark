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
