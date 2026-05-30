package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithForeignKeys(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"bare path", "padmark.db", "padmark.db?_pragma=foreign_keys(1)"},
		{"existing query", "file:db?cache=shared", "file:db?cache=shared&_pragma=foreign_keys(1)"},
		{"already set is idempotent", "db?_pragma=foreign_keys(1)", "db?_pragma=foreign_keys(1)"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, withForeignKeys(testCase.dsn))
		})
	}
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

func TestAllowedHostSet(t *testing.T) {
	require.Nil(t, allowedHostSet(""))
	require.Nil(t, allowedHostSet("  ,  "))

	set := allowedHostSet("example.com, www.example.com ")
	require.Equal(t, map[string]struct{}{"example.com": {}, "www.example.com": {}}, set)
}
