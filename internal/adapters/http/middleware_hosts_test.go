package http

import (
	net_http "net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithAllowedHosts(t *testing.T) {
	t.Parallel()

	okHandler := net_http.HandlerFunc(func(w net_http.ResponseWriter, _ *net_http.Request) {
		w.WriteHeader(net_http.StatusOK)
	})

	cases := []struct {
		name  string
		hosts []string
		host  string
		path  string
		want  int
	}{
		{"listed host passes", []string{"example.com"}, "example.com", "/", net_http.StatusOK},
		{"listed host with port passes", []string{"example.com"}, "example.com:8443", "/", net_http.StatusOK},
		{"comparison is case-insensitive", []string{"Example.COM"}, "EXAMPLE.com", "/", net_http.StatusOK},
		{"trailing dot is ignored", []string{"example.com"}, "example.com.", "/", net_http.StatusOK},
		{"second listed host passes", []string{"a.test", "b.test"}, "b.test", "/", net_http.StatusOK},
		{"foreign host is rejected", []string{"example.com"}, "evil.test", "/", net_http.StatusMisdirectedRequest},
		{"foreign host with port is rejected", []string{"example.com"}, "evil.test:80", "/x",
			net_http.StatusMisdirectedRequest},
		{"suffix lookalike is rejected", []string{"example.com"}, "example.com.evil.test", "/",
			net_http.StatusMisdirectedRequest},
		{"ipv6 literal passes when listed", []string{"::1"}, "[::1]:8080", "/", net_http.StatusOK},
		{"empty list disables the check", nil, "anything.test", "/", net_http.StatusOK},
		{"probe paths bypass the check", []string{"example.com"}, "10.0.0.7:8080", "/healthz", net_http.StatusOK},
		{"readiness probe bypasses the check", []string{"example.com"}, "10.0.0.7", "/readyz", net_http.StatusOK},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(net_http.MethodGet, testCase.path, nil)
			req.Host = testCase.host

			rec := httptest.NewRecorder()
			withAllowedHosts(testCase.hosts, okHandler).ServeHTTP(rec, req)

			assert.Equal(t, testCase.want, rec.Code)
		})
	}
}
