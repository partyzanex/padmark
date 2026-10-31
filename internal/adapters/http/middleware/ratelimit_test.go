package middleware

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testDiscardLog = slog.New(slog.DiscardHandler) //nolint:gochecknoglobals // test helper

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

// ClientIP

func TestClientIP_NoProxies_UsesRemoteAddr(t *testing.T) {
	req := newReq("10.0.0.1:9000", "1.2.3.4", "")
	assert.Equal(t, "10.0.0.1", ClientIP(req, nil))
}

func TestClientIP_TrustedProxy_XForwardedFor(t *testing.T) {
	req := newReq("10.0.0.2:5000", "203.0.113.5, 10.0.0.2", "")
	assert.Equal(t, "203.0.113.5", ClientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_XRealIP(t *testing.T) {
	req := newReq("10.0.0.2:5000", "", "203.0.113.7")
	assert.Equal(t, "203.0.113.7", ClientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_UntrustedProxy_HeaderIgnored(t *testing.T) {
	req := newReq("10.0.0.1:9000", "1.2.3.4", "")
	assert.Equal(t, "10.0.0.1", ClientIP(req, []*net.IPNet{parseCIDR(t, "192.168.0.0/16")}))
}

func TestClientIP_MalformedRemoteAddr(t *testing.T) {
	req := newReq("not-valid", "", "")
	assert.Equal(t, "not-valid", ClientIP(req, nil))
}

func TestClientIP_TrustedProxy_SpoofedLeftmostXFFIgnored(t *testing.T) {
	// A client behind the trusted proxy plants "1.2.3.4" in XFF; the proxy appends the real
	// client IP "198.51.100.9". The rightmost non-trusted entry must win, otherwise rotating
	// the leftmost value grants a fresh rate-limit bucket per request.
	req := newReq("10.0.0.2:5000", "1.2.3.4, 198.51.100.9", "")
	assert.Equal(t, "198.51.100.9", ClientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_PlantedTrustedEntryDoesNotUnmaskAttackerValue(t *testing.T) {
	// The client lists a fake trusted-proxy IP to the left; the walk stops at the entry the
	// real proxy appended and never reaches the planted one.
	req := newReq("10.0.0.2:5000", "203.0.113.5, 10.0.0.2, 198.51.100.9", "")
	assert.Equal(t, "198.51.100.9", ClientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_MalformedXFEntryFallsBackToRemoteAddr(t *testing.T) {
	// A malformed entry breaks the append chain, so nothing in the list is trustworthy;
	// RemoteAddr (the proxy's own IP) is the only safe bucket key.
	req := newReq("10.0.0.2:5000", "8.8.8.8, junk", "")
	assert.Equal(t, "10.0.0.2", ClientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_AllXFFEntriesTrustedFallsBackToRemoteAddr(t *testing.T) {
	req := newReq("10.0.0.2:5000", "10.0.0.3, 10.0.0.2", "")
	assert.Equal(t, "10.0.0.2", ClientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

func TestClientIP_TrustedProxy_InvalidXRealIPFallsBackToRemoteAddr(t *testing.T) {
	req := newReq("10.0.0.2:5000", "", "not-an-ip")
	assert.Equal(t, "10.0.0.2", ClientIP(req, []*net.IPNet{parseCIDR(t, "10.0.0.0/8")}))
}

// ClientIP under a misconfigured --trusted-proxies
//
// These pin what actually happens when the operator gets the flag wrong in either direction.
// Both cases are configuration errors, not code defects, but the consequences differ enough
// that they are worth stating in executable form rather than in prose.

// TestClientIP_WideOpenProxies_XForwardedForStillFallsBackToRemoteAddr shows the right-to-left
// walk holds even under the worst possible configuration: with every address "trusted", every
// XFF entry is treated as a proxy hop, the walk finds no client entry, and RemoteAddr wins.
// So a wide-open CIDR does not, by itself, make X-Forwarded-For spoofable.
func TestClientIP_WideOpenProxies_XForwardedForStillFallsBackToRemoteAddr(t *testing.T) {
	req := newReq("198.51.100.9:5000", "1.2.3.4, 5.6.7.8", "")
	assert.Equal(t, "198.51.100.9", ClientIP(req, []*net.IPNet{parseCIDR(t, "0.0.0.0/0")}))
}

// TestClientIP_WideOpenProxies_XRealIPIsSpoofable is the real cost of an over-broad
// --trusted-proxies. X-Real-IP is a single value with no chain to walk, so once RemoteAddr
// counts as trusted it is taken verbatim — and with a wide-open CIDR every client counts as
// trusted. The client then picks its own rate-limit bucket.
func TestClientIP_WideOpenProxies_XRealIPIsSpoofable(t *testing.T) {
	req := newReq("198.51.100.9:5000", "", "1.2.3.4")
	assert.Equal(t, "1.2.3.4", ClientIP(req, []*net.IPNet{parseCIDR(t, "0.0.0.0/0")}))
}

// TestClientIP_WideOpenProxies_XRealIPWinsOverNeutralisedXFF combines the two above: XFF
// resolves to nothing, so the spoofable header decides. Documented so a future change to the
// header precedence cannot silently make this worse.
func TestClientIP_WideOpenProxies_XRealIPWinsOverNeutralisedXFF(t *testing.T) {
	req := newReq("198.51.100.9:5000", "1.2.3.4, 5.6.7.8", "9.9.9.9")
	assert.Equal(t, "9.9.9.9", ClientIP(req, []*net.IPNet{parseCIDR(t, "0.0.0.0/0")}))
}

// TestWithTOTPRateLimit_WideOpenProxies_SpoofedXRealIPDefeatsLimit is the end-to-end
// consequence: with --trusted-proxies wide open, rotating X-Real-IP per attempt gives every
// request a fresh bucket, so the 10/min TOTP cap never engages. This is the misconfiguration
// scenario, stated concretely.
func TestWithTOTPRateLimit_WideOpenProxies_SpoofedXRealIPDefeatsLimit(t *testing.T) {
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		w.WriteHeader(http.StatusOK)
	})
	handler := WithTOTPRateLimit([]*net.IPNet{parseCIDR(t, "0.0.0.0/0")}, testDiscardLog, inner)

	const attempts = totpRatePerMin * 3

	for i := range attempts {
		req := httptest.NewRequest(http.MethodPost, "/totp-login", nil)
		req.RemoteAddr = "198.51.100.9:9000"
		req.Header.Set("X-Real-IP", fmt.Sprintf("203.0.113.%d", i%256))

		rec := httptest.NewRecorder()
		handler(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	}

	assert.Equal(t, attempts, calls, "a wide-open trusted-proxy list lets one client rotate buckets freely")
}

// TestWithTOTPRateLimit_NoProxiesConfigured_SpoofedHeadersDoNotDefeatLimit is the same attack
// against the default configuration, and it fails: with no trusted proxies the headers are
// ignored outright, so every attempt lands in the RemoteAddr bucket and the cap engages.
func TestWithTOTPRateLimit_NoProxiesConfigured_SpoofedHeadersDoNotDefeatLimit(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WithTOTPRateLimit(nil, testDiscardLog, inner)

	var lastCode int

	for i := range totpRatePerMin + 1 {
		req := httptest.NewRequest(http.MethodPost, "/totp-login", nil)
		req.RemoteAddr = "198.51.100.9:9000"
		req.Header.Set("X-Real-IP", fmt.Sprintf("203.0.113.%d", i%256))
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i%256))

		rec := httptest.NewRecorder()
		handler(rec, req)

		lastCode = rec.Code
	}

	assert.Equal(t, http.StatusTooManyRequests, lastCode,
		"headers must be ignored entirely when no proxy is trusted")
}

// TestWithTOTPRateLimit_ProxyPresentButUntrusted_ClientsShareOneBucket pins the opposite
// misconfiguration: the server really is behind a proxy but --trusted-proxies was left empty,
// so every request carries the proxy's address and all clients collapse into a single bucket.
// Nothing is spoofable here — the cost is that unrelated users throttle each other, and one
// noisy client can lock everyone out of TOTP login.
func TestWithTOTPRateLimit_ProxyPresentButUntrusted_ClientsShareOneBucket(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WithTOTPRateLimit(nil, testDiscardLog, inner)

	// Distinct real clients, each correctly announced by the proxy in X-Forwarded-For.
	for i := range totpRatePerMin {
		req := httptest.NewRequest(http.MethodPost, "/totp-login", nil)
		req.RemoteAddr = "10.0.0.2:9000"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d, 10.0.0.2", i%256))

		handler(httptest.NewRecorder(), req)
	}

	// A fresh, unrelated client is already throttled by the others' traffic.
	req := httptest.NewRequest(http.MethodPost, "/totp-login", nil)
	req.RemoteAddr = "10.0.0.2:9000"
	req.Header.Set("X-Forwarded-For", "198.51.100.77, 10.0.0.2")

	rec := httptest.NewRecorder()
	handler(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"an unconfigured proxy collapses every client into the proxy's own bucket")
}

// WarnUnconfiguredProxy

// countingHandler records how many requests reached the wrapped handler.
type countingHandler struct{ calls int }

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	c.calls++

	w.WriteHeader(http.StatusOK)
}

// newWarnRecorder returns a logger writing into buf, so a test can assert on what was logged.
func newWarnRecorder(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn,
		AddSource: false, ReplaceAttr: nil,
	}))
}

// warnReq builds a request optionally carrying each forwarding header.
func warnReq(xff, xri string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.2:9000"

	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}

	if xri != "" {
		req.Header.Set("X-Real-IP", xri)
	}

	return req
}

func TestWarnUnconfiguredProxy_ForwardingHeaderWarnsOnce(t *testing.T) {
	var buf bytes.Buffer

	inner := &countingHandler{
		calls: 0,
	}
	handler := WarnUnconfiguredProxy(newWarnRecorder(&buf), inner)

	for range 3 {
		handler.ServeHTTP(httptest.NewRecorder(), warnReq("203.0.113.5", ""))
	}

	assert.Equal(t, 3, inner.calls, "the warning must never interfere with the request")
	assert.Equal(t, 1, strings.Count(buf.String(), "no proxy is trusted"),
		"the warning is a one-off, not a per-request log flood")
}

func TestWarnUnconfiguredProxy_XRealIPAlsoWarns(t *testing.T) {
	var buf bytes.Buffer

	handler := WarnUnconfiguredProxy(newWarnRecorder(&buf), &countingHandler{
		calls: 0,
	})
	handler.ServeHTTP(httptest.NewRecorder(), warnReq("", "203.0.113.5"))

	assert.Contains(t, buf.String(), "no proxy is trusted")
}

// TestWarnUnconfiguredProxy_NoHeaderStaysSilent is the false-positive guard: a server exposed
// directly, with nothing forwarding to it, is a legitimate configuration and must not be
// nagged. This is why the warning keys off the header rather than the listen address.
func TestWarnUnconfiguredProxy_NoHeaderStaysSilent(t *testing.T) {
	var buf bytes.Buffer

	inner := &countingHandler{
		calls: 0,
	}
	handler := WarnUnconfiguredProxy(newWarnRecorder(&buf), inner)

	for range 3 {
		handler.ServeHTTP(httptest.NewRecorder(), warnReq("", ""))
	}

	assert.Equal(t, 3, inner.calls)
	assert.Empty(t, buf.String(), "no forwarding header means no proxy to warn about")
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

// WithTOTPRateLimit

func TestWithTOTPRateLimit_AllowsUnderLimit(t *testing.T) {
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		w.WriteHeader(http.StatusOK)
	})
	handler := WithTOTPRateLimit(nil, testDiscardLog, inner)

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
	handler := WithTOTPRateLimit(nil, testDiscardLog, inner)

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

	// The OpenAPI ErrorResponse schema promises JSON ({"message": ...}), not net/http's
	// default text/plain (see errors.go's WriteJSONErrorStatus).
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"message":"too many requests"}`, rec.Body.String())
}

// TestWithRateLimit_Exceeded_ReturnsJSONError is withTOTPRateLimit_BlocksOverLimit's general
// counterpart: WithRateLimit (the whole-stack per-IP limiter, not just POST /totp-login) must
// answer 429 with the same JSON shape.
func TestWithRateLimit_Exceeded_ReturnsJSONError(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WithRateLimit(1, 1, nil, testDiscardLog, inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "7.7.7.7:9000"

	// First request consumes the single burst token.
	handler.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"message":"too many requests"}`, rec.Body.String())
}

func TestWithTOTPRateLimit_DifferentIPs_IndependentBuckets(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WithTOTPRateLimit(nil, testDiscardLog, inner)

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
