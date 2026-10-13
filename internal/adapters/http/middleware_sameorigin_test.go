package http

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSameOrigin_HostIsTheGate — the host comparison settles every cross-site origin, scheme
// precision is additional when the requestOrigin middleware ran.
func TestSameOrigin_HostIsTheGate(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		ctxHost  string
		ctxSche  string
		origin   string
		wantSame bool
	}{
		{"exact match", "padmark.example", "padmark.example", "https", "https://padmark.example", true},
		{"case-insensitive host", "Padmark.Example", "padmark.example", "https", "https://PADMARK.example", true},
		{"sibling subdomain", "padmark.example", "padmark.example", "https", "https://evil.sibling.example", false},
		{"wrong scheme, ctx knows", "padmark.example", "padmark.example", "http", "https://padmark.example", false},
		{"scheme ignored without ctx origin", "padmark.example", "", "", "https://padmark.example", true},
	}

	for _, caseRow := range tests {
		r := httptest.NewRequest(http.MethodPost, "https://"+caseRow.host+"/api/tokens", nil)

		if caseRow.ctxHost != "" {
			r = r.WithContext(withTestOrigin(
				r.Context(), requestOrigin{Scheme: caseRow.ctxSche, Host: caseRow.ctxHost}))
		}

		assert.Equal(t, caseRow.wantSame, sameOrigin(r, caseRow.origin), caseRow.name)
	}
}

// TestSameOrigin_MalformedOriginIsForeign — an unparseable Origin (or a header without
// scheme/host) is treated as foreign: the check fails closed.
func TestSameOrigin_MalformedOriginIsForeign(t *testing.T) {
	for _, bad := range []string{"notaurl", "://", "https://"} {
		r := httptest.NewRequest(http.MethodPost, "https://padmark.example/api/tokens", nil)
		r = r.WithContext(withTestOrigin(r.Context(), requestOrigin{Scheme: protoHTTPS, Host: "padmark.example"}))

		assert.False(t, sameOrigin(r, bad), "malformed Origin %q must not count as same-origin", bad)
	}
}

// TestRequireSameOrigin_RejectsAndAllows drives the middleware itself.
func TestRequireSameOrigin_RejectsAndAllows(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	gated := requireSameOrigin(slog.New(slog.DiscardHandler), upstream)

	tests := []struct {
		name   string
		origin string
		want   int
	}{
		{"no origin (machine client)", "", http.StatusCreated},
		{"same-origin", "http://example.com", http.StatusCreated},
		{"cross-origin", "https://evil.example", http.StatusForbidden},
	}

	for _, caseRow := range tests {
		r := httptest.NewRequest(http.MethodPost, "/api/tokens", nil)
		if caseRow.origin != "" {
			r.Header.Set("Origin", caseRow.origin)
		}

		rec := httptest.NewRecorder()

		gated.ServeHTTP(rec, r)

		require.Equal(t, caseRow.want, rec.Code, caseRow.name)

		if caseRow.want == http.StatusForbidden {
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"), caseRow.name)
		}
	}
}

// withTestOrigin installs a requestOrigin value directly, standing in for withRequestOrigin —
// the unit test bypasses the middleware that would produce it.
func withTestOrigin(ctx context.Context, origin requestOrigin) context.Context {
	return context.WithValue(ctx, originCtxKey{}, origin)
}
