package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// ── CSRF guard ──.

func (s *AuthHandlerSuite) TestCSRFGuard_MissingToken_Returns403() {
	// POST without CSRF cookie must be rejected before reaching the handler.
	form := url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}}
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// no padmark_csrf cookie.

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestCSRFGuard_TokenMismatch_Returns403() {
	// Cookie value doesn't match form field.
	form := url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}, "csrf_token": {"wrong-token"}}
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie()) // cookie = testCSRFToken, field = "wrong-token".

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

// TestCSRFGuard_InvalidHMAC_Returns403 covers the branch where cookie and form field match
// (so the equality check passes) but the token's HMAC signature is invalid — e.g. a token
// minted with a different secret. The guard must still reject it via VerifyCSRFToken.
func (s *AuthHandlerSuite) TestCSRFGuard_InvalidHMAC_Returns403() {
	forgedSecret := []byte("a-different-csrf-secret-32bytes!!")
	forged := web.GenerateCSRFTokenForTest(forgedSecret)

	form := url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}, "csrf_token": {forged}}
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Cookie == field (equality check passes) but the HMAC was signed with the wrong secret.
	req.AddCookie(httptestutil.RequestCookie("padmark_csrf", forged))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

// ── CSRF secret sharing across processes ──.

// newRouterWithCSRFSecret builds an independent router instance with the given CSRF secret. Two
// of them stand in for two replicas, or for the same server before and after a restart.
func (s *AuthHandlerSuite) newRouterWithCSRFSecret(secret []byte) http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.Accounts = s.auth
	handlerCfg.CSRFSecret = secret
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

// issuedCSRFCookie drives a page load through router so it mints and sets its own CSRF cookie,
// then returns that cookie — the token a browser would carry into the next form POST. Taking it
// from the router rather than minting one locally is the point: it proves the value the server
// actually hands out is portable, not just one the test could construct.
func (s *AuthHandlerSuite) issuedCSRFCookie(router http.Handler) *http.Cookie {
	s.T().Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "padmark_csrf" {
			return cookie
		}
	}

	s.FailNow("router did not set a padmark_csrf cookie")

	return nil
}

// postTOTPLoginWithCSRF replays a form POST carrying cookie as both the CSRF cookie and the
// matching form field, the way a browser submits a page rendered earlier.
func (s *AuthHandlerSuite) postTOTPLoginWithCSRF(router http.Handler, cookie *http.Cookie) int {
	s.T().Helper()

	form := url.Values{
		"username":   {"alice"},
		"password":   {testPw},
		"code":       {"123456"},
		"csrf_token": {cookie.Value},
	}

	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec.Code
}

// TestCSRF_SharedSecret_TokenSurvivesInstanceChange is the regression guard for
// --csrf-secret / PADMARK_CSRF_SECRET: a token minted by one process must still validate in
// another one configured with the same secret. That covers a restart (same secret, new process)
// and a multi-replica deployment where the form POST lands on a different replica than the GET
// that rendered the form.
func (s *AuthHandlerSuite) TestCSRF_SharedSecret_TokenSurvivesInstanceChange() {
	s.auth.EXPECT().
		Login(gomock.Any(), loginWith("alice", "", "123456", "")).
		Return("sess-id", nil)

	before := s.newRouterWithCSRFSecret(authTestCSRFSecret)
	after := s.newRouterWithCSRFSecret(authTestCSRFSecret)

	cookie := s.issuedCSRFCookie(before)

	s.NotEqual(http.StatusForbidden, s.postTOTPLoginWithCSRF(after, cookie),
		"a shared csrf secret must make form tokens portable across instances")
}

// TestCSRF_NoSecret_TokenRejectedByOtherInstance pins the failure the flag exists to remove:
// with no configured secret every process invents its own, so a token from one is worthless to
// the next. This is what a restart or a second replica looks like without --csrf-secret — and
// it is why leaving the flag empty logs a warning at startup.
func (s *AuthHandlerSuite) TestCSRF_NoSecret_TokenRejectedByOtherInstance() {
	s.auth.EXPECT().
		Login(gomock.Any(), loginWith("alice", "", "123456", "")).
		Return("sess-id", nil)

	before := s.newRouterWithCSRFSecret(nil)
	after := s.newRouterWithCSRFSecret(nil)

	cookie := s.issuedCSRFCookie(before)

	s.Equal(http.StatusForbidden, s.postTOTPLoginWithCSRF(after, cookie),
		"without a shared secret the other instance must reject the token")
	s.NotEqual(http.StatusForbidden, s.postTOTPLoginWithCSRF(before, cookie),
		"the minting instance must still accept its own token")
}
