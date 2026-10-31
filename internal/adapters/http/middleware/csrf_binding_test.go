package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/tests/httptestutil"
)

var bindingTestSecret = []byte("padmark-test-csrf-secret-32bytes") //nolint:gochecknoglobals // test fixture

// TestCSRFToken_IsBoundToSession verifies a token verifies only for the session (or the
// anonymous state) it was issued for.
func TestCSRFToken_IsBoundToSession(t *testing.T) {
	t.Parallel()

	anon, err := GenerateCSRFToken(bindingTestSecret, "")
	require.NoError(t, err)

	bound, err := GenerateCSRFToken(bindingTestSecret, "session-1")
	require.NoError(t, err)

	assert.True(t, VerifyCSRFToken(anon, bindingTestSecret, ""))
	assert.False(t, VerifyCSRFToken(anon, bindingTestSecret, "session-1"),
		"a token issued to an anonymous visitor must not validate for a signed-in session")

	assert.True(t, VerifyCSRFToken(bound, bindingTestSecret, "session-1"))
	assert.False(t, VerifyCSRFToken(bound, bindingTestSecret, "session-2"), "another session")
	assert.False(t, VerifyCSRFToken(bound, bindingTestSecret, ""), "the session is gone")
}

func csrfPost(t *testing.T, token, sessionID string, headers map[string]string) *http.Request {
	t.Helper()

	form := url.Values{csrfFieldName: {token}}
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(httptestutil.RequestCookie(csrfCookieName, token))

	if sessionID != "" {
		req.AddCookie(httptestutil.RequestCookie(SessionCookieName, sessionID))
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	return req
}

func runGuard(req *http.Request) (code int, called bool) {
	guard := CSRFGuard(bindingTestSecret, func(w http.ResponseWriter, _ *http.Request) {
		called = true

		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	guard(rec, req)

	return rec.Code, called
}

func TestCSRFGuard_RejectsAnonymousTokenOnSignedInPost(t *testing.T) {
	t.Parallel()

	anon, err := GenerateCSRFToken(bindingTestSecret, "")
	require.NoError(t, err)

	code, called := runGuard(csrfPost(t, anon, "sess-1", nil))
	assert.Equal(t, http.StatusForbidden, code, "a pair minted before sign-in must not authorise a session POST")
	assert.False(t, called)

	bound, err := GenerateCSRFToken(bindingTestSecret, "sess-1")
	require.NoError(t, err)

	code, called = runGuard(csrfPost(t, bound, "sess-1", nil))
	assert.Equal(t, http.StatusNoContent, code)
	assert.True(t, called)
}

func TestCSRFGuard_RejectsCrossSiteSignals(t *testing.T) {
	t.Parallel()

	token, err := GenerateCSRFToken(bindingTestSecret, "sess-1")
	require.NoError(t, err)

	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no origin signals (non-browser)", nil, http.StatusNoContent},
		{"same origin", map[string]string{"Origin": "http://example.com"}, http.StatusNoContent},
		{"same-origin fetch metadata", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusNoContent},
		{"foreign origin", map[string]string{"Origin": "http://evil.test"}, http.StatusForbidden},
		{"opaque origin", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"sibling subdomain origin", map[string]string{"Origin": "http://a.example.com"}, http.StatusForbidden},
		{"cross-site fetch metadata", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site fetch metadata", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			code, _ := runGuard(csrfPost(t, token, "sess-1", testCase.headers))
			assert.Equal(t, testCase.want, code)
		})
	}
}

// TestWithCSRFToken_ReissuesTokenWhenSessionChanges verifies the middleware replaces a cookie
// bound to another state, so the page rendered after sign-in embeds a token for the new session.
func TestWithCSRFToken_ReissuesTokenWhenSessionChanges(t *testing.T) {
	t.Parallel()

	anon, err := GenerateCSRFToken(bindingTestSecret, "")
	require.NoError(t, err)

	var seen string

	handler := WithCSRFToken(bindingTestSecret, "", nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = CSRFFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(httptestutil.RequestCookie(csrfCookieName, anon))
	req.AddCookie(httptestutil.RequestCookie(SessionCookieName, "sess-1"))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.NotEmpty(t, seen)
	assert.NotEqual(t, anon, seen, "an anonymous-bound cookie must be replaced")
	assert.True(t, VerifyCSRFToken(seen, bindingTestSecret, "sess-1"))
	assert.Contains(t, rec.Header().Get("Set-Cookie"), csrfCookieName+"="+seen)
}
