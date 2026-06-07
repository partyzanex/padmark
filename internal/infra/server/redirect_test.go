package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syncBuffer is a goroutine-safe buffer for capturing async slog output in tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestWithSQLitePragmas(t *testing.T) {
	// Bare path: all three pragmas appended with "?" then "&".
	require.Equal(t,
		"padmark.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		withSQLitePragmas("padmark.db"))

	// Existing query string: pragmas appended with "&".
	require.Equal(t,
		"file:db?cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		withSQLitePragmas("file:db?cache=shared"))

	// Per-pragma idempotency: a pre-set foreign_keys is not duplicated; the others are added.
	got := withSQLitePragmas("db?_pragma=foreign_keys(0)")
	require.Equal(t, 1, strings.Count(got, "_pragma=foreign_keys"), "foreign_keys must not be duplicated")
	require.Contains(t, got, "_pragma=busy_timeout(5000)")
	require.Contains(t, got, "_pragma=journal_mode(WAL)")
}

func TestHTTPRedirectHandler(t *testing.T) {
	tests := []struct {
		name       string
		allowed    map[string]struct{}
		host       string
		wantStatus int
		wantLoc    string
	}{
		{
			name:       "no allowlist redirects to request host (legacy)",
			allowed:    nil,
			host:       "example.com",
			wantStatus: http.StatusMovedPermanently,
			wantLoc:    "https://example.com/path?q=1",
		},
		{
			name:       "allowlisted host redirects",
			allowed:    map[string]struct{}{"example.com": {}},
			host:       "example.com",
			wantStatus: http.StatusMovedPermanently,
			wantLoc:    "https://example.com/path?q=1",
		},
		{
			name:       "allowlisted host with port redirects",
			allowed:    map[string]struct{}{"example.com": {}},
			host:       "example.com:8080",
			wantStatus: http.StatusMovedPermanently,
			wantLoc:    "https://example.com:8080/path?q=1",
		},
		{
			name:       "host not in allowlist is rejected with 400",
			allowed:    map[string]struct{}{"example.com": {}},
			host:       "evil.com",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			handler := httpRedirectHandler(testCase.allowed)
			req := httptest.NewRequest(http.MethodGet, "http://"+testCase.host+"/path?q=1", nil)
			req.Host = testCase.host
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			require.Equal(t, testCase.wantStatus, rec.Code)

			if testCase.wantLoc != "" {
				require.Equal(t, testCase.wantLoc, rec.Header().Get("Location"))
			}
		})
	}
}

// TestStartRedirectServer_BindFailureIsNonFatal verifies that a redirect-listener bind error
// (port already in use) is logged and swallowed rather than crashing or propagating — a healthy
// main server must not be affected by the auxiliary redirector failing to start.
func TestStartRedirectServer_BindFailureIsNonFatal(t *testing.T) {
	// Occupy a port so the redirect listener cannot bind to it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = ln.Close() }()

	addr := ln.Addr().String()

	var sink syncBuffer

	log := slog.New(slog.NewTextHandler(&sink, &slog.HandlerOptions{Level: slog.LevelError}))

	// Must not panic and must return immediately (failure handled in its own goroutine).
	startRedirectServer(context.Background(), addr, nil, log)

	require.Eventually(t, func() bool {
		return strings.Contains(sink.String(), "http redirect listener stopped")
	}, time.Second, 10*time.Millisecond, "bind failure must be logged, not fatal")
}

func TestAllowedHostSet(t *testing.T) {
	require.Nil(t, allowedHostSet(""))
	require.Nil(t, allowedHostSet("  ,  "))

	set := allowedHostSet("example.com, www.example.com ")
	require.Equal(t, map[string]struct{}{"example.com": {}, "www.example.com": {}}, set)
}
