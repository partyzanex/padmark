package web_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
)

// ── IndexPage ──.

func (s *HandlerSuite) TestIndexPage() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "Padmark")
	s.Contains(w.Body.String(), "Publish")
}

// TestCreateLinkPage_AnonymousWithAccounts: with accounts a signed-out user sees the protected
// levels disabled, and a sign-in link returns the user to the form. The note editor needs a
// session with accounts, so the short-link form is the anonymous form.
func (s *HandlerSuite) TestCreateLinkPage_AnonymousWithAccounts() {
	w := httptest.NewRecorder()
	s.newRouterWithTOTPAuth().ServeHTTP(w, linkPageRequest("/links"))

	s.Require().Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, `id="privacy-public" value="public" checked>`)
	s.Contains(body, `id="privacy-authenticated" value="authenticated" disabled>`)
	s.Contains(body, `id="privacy-owner" value="owner" disabled>`)
	s.Contains(body, `href="/login?next=%2Flinks"`)
	s.Contains(body, "Sign in to restrict access")
}

// ── EditPage ──.

func (s *HandlerSuite) TestEditPage_OK() {
	note := newTestNote("edit me", "# hello")
	note.Privacy = new(domain.PrivacyAuthenticated)

	future := time.Now().Add(time.Hour)
	note.ExpiresAt = &future

	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "edit me")
	s.Contains(body, "# hello")
	s.Contains(body, "Save")
	// Without accounts the stored level stays selectable but says it is not enforced (review R2).
	s.Contains(body, `value="authenticated" checked>`)
	s.Contains(body, "Not enforced: accounts are off, anyone with the link can view")
}

// TestEditPage_FixedSlugAndDelete: the note editor shows the slug as fixed text and has the
// "Delete note" action, as the link and file editors do.
func (s *HandlerSuite) TestEditPage_FixedSlugAndDelete() {
	note := newTestNote("edit me", "# hello")

	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil))

	s.Require().Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, `id="slugFixed"`)
	s.NotContains(body, `id="slugInput"`)
	s.Contains(body, `<button type="button" class="btn-danger" id="deleteBtn">Delete note</button>`)
}

// TestEditPage_BurnNote_ContentWithheld: the editor never burns, so it must not pre-fill a
// burn-after-reading note's content — otherwise /edit/{id} reads it any number of times (H-1).
func (s *HandlerSuite) TestEditPage_BurnNote_ContentWithheld() {
	note := newTestNote("edit me", "# burn-only-once")
	note.BurnAfterReading = true

	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, "edit me")
	s.NotContains(body, "burn-only-once")
	s.Contains(body, "Burn-after-reading content is not shown here")
	s.Contains(body, "checked")
}

// TestEditPage_Expired_Gone: the editor reads with Inspect, which applies expiry — an expired
// note is deleted (by the usecase) and answered 410 instead of being served (H-1).
func (s *HandlerSuite) TestEditPage_Expired_Gone() {
	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(nil, domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
	s.NotContains(w.Body.String(), "stale-content")
}

// ── SuccessPage ──.

func (s *HandlerSuite) TestSuccessPage_OK() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "Paste created")
	s.Contains(w.Body.String(), "http://example.com/abc")
	s.Contains(w.Body.String(), "never expires")
}

// TestSuccessPage_Link: the result of a new short link shares /{id} and opens the link page
// /links/{id}, because /{id} is the redirect. As for a note, the page has no Edit action, and
// "New" opens the create page of the same kind.
func (s *HandlerSuite) TestSuccessPage_Link() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?kind=link&id=abc", nil)

	s.router.ServeHTTP(w, r)

	body := w.Body.String()
	s.Equal(http.StatusOK, w.Code)
	s.Contains(body, "Link created")
	s.Contains(body, "Your link is live")
	s.Contains(body, `<a href="http://example.com/abc" class="copy-value"`)
	s.Contains(body, `href="/links/abc" class="btn-ghost" id="openRecordLink"`)
	s.NotContains(body, "/edit/abc")
	s.Contains(body, `href="/links" class="btn-primary header-new" aria-label="New link"`)
}

func (s *HandlerSuite) TestSuccessPage_Privacy() {
	for query, expected := range map[string]string{
		"&privacy=owner":         "Only you can open it.",
		"&privacy=authenticated": "Signed-in users with the link can open it.",
		"&privacy=bogus":         "Anyone with the link can open it.",
		"":                       "Anyone with the link can open it.",
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/success?id=abc"+query, nil)

		s.router.ServeHTTP(w, r)

		s.Equal(http.StatusOK, w.Code, query)
		s.Contains(w.Body.String(), expected, query)
	}
}

func (s *HandlerSuite) TestSuccessPage_NoID() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestSuccessPage_WithBurnAndExpires() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/success?id=abc&burn=1&expires=2026-06-15T12:00:00Z", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, "Burns after reading")
	s.Contains(body, "expires Jun 15, 2026")
	// edit code is stored in sessionStorage before redirect and read by client-side JS;
	// it is never rendered into the HTML by the server.
	s.Contains(body, "editCodeBlock")
}

func (s *HandlerSuite) TestSuccessPage_HTTPS() {
	_, trustedNet, err := net.ParseCIDR("192.0.2.1/32")
	s.Require().NoError(err)

	router := s.newRouterWithOptions([]*net.IPNet{trustedNet}, "")

	w := httptest.NewRecorder()
	// httptest.NewRequest defaults RemoteAddr to 192.0.2.1:1234, matching trustedNet above.
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)
	r.Header.Set("X-Forwarded-Proto", "https")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "https://example.com/abc")
	s.NotContains(w.Body.String(), "http://example.com/abc")
}

func (s *HandlerSuite) TestSuccessPage_UntrustedProxyIsIgnored() {
	// No trusted proxies configured: X-Forwarded-Proto must be ignored even though it claims
	// https, since RemoteAddr (192.0.2.1, the httptest default) is not in any trusted CIDR.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)
	r.Header.Set("X-Forwarded-Proto", "https")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "http://example.com/abc")
}

func (s *HandlerSuite) TestSuccessPage_ForcedScheme() {
	// --public-scheme=https forces the scheme with no TLS, no trusted proxies, and no
	// X-Forwarded-Proto header at all — the override needs no signal from the request.
	router := s.newRouterWithOptions(nil, "https")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "https://example.com/abc")
}

func (s *HandlerSuite) TestSuccessPage_ForcedSchemeOverridesXForwardedProto() {
	// The forced scheme takes precedence even when a trusted proxy's X-Forwarded-Proto
	// disagrees, so a misconfigured proxy can't undermine an explicit operator override.
	_, trustedNet, err := net.ParseCIDR("192.0.2.1/32")
	s.Require().NoError(err)

	router := s.newRouterWithOptions([]*net.IPNet{trustedNet}, "http")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)
	r.Header.Set("X-Forwarded-Proto", "https")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "http://example.com/abc")
}

func (s *HandlerSuite) TestSuccessPage_InvalidExpires() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc&expires=bad-date", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "never expires")
}

// ── API docs ──.

func (s *HandlerSuite) TestAPIDocsPage() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "openapi.yaml")
	s.Contains(w.Body.String(), "redoc")
}

// TestCSP_NonFetchDirectivesHardened verifies the directives that default-src does not cover are
// pinned on every page, including the API docs page: no <base> injection, forms posting only to
// the same origin, no framing and no plugin content.
func (s *HandlerSuite) TestCSP_NonFetchDirectivesHardened() {
	for _, path := range []string{"/", "/api"} {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		csp := rec.Header().Get("Content-Security-Policy")

		for _, directive := range []string{
			"base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'", "object-src 'none'",
		} {
			s.Contains(csp, directive, "CSP on %s must contain %q", path, directive)
		}
	}
}

func (s *HandlerSuite) TestAPISpec() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Equal("application/yaml", w.Header().Get("Content-Type"))
	s.Contains(w.Body.String(), "openapi: 3.1.0")
	s.Contains(w.Body.String(), "Padmark API")
}

func (s *HandlerSuite) TestAPIDocsPage_WriteFail() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg)
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/api", nil)

	handler.APIDocsPage(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestAPISpec_WriteFail() {
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	web.APISpec(discardLog())(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestAPIDocsPage_Public() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.Accounts = s.authMgr
	handler := newTestHandler(s.T(), handlerCfg)
	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}
	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code, "/api should be public")
}
