package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// Fixed fake user IDs shared across this file's tests: each gomock EXPECT() that checks a
// caller/target ID must see the same value the test's session/token resolves to, so these are
// vars (not inline uuid.New() calls, which would never compare equal to themselves).
var (
	testAdminID  = uuid.New() //nolint:gochecknoglobals // test fixture
	testUserID1  = uuid.New() //nolint:gochecknoglobals // test fixture
	testUserID2  = uuid.New() //nolint:gochecknoglobals // test fixture
	testTargetID = uuid.New() //nolint:gochecknoglobals // test fixture
)

type AuthHandlerSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	manager     *MockNoteManager
	linkManager *MockLinkManager
	pinger      *MockPinger
	auth        *MockAuthManager
	router      http.Handler
}

func TestAuthHandler(t *testing.T) {
	suite.Run(t, new(AuthHandlerSuite))
}

func (s *AuthHandlerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)
	s.auth = NewMockAuthManager(s.ctrl)
	s.router = s.newRouterWithAuth()
}

func (s *AuthHandlerSuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *AuthHandlerSuite) newRouterWithAuth() http.Handler {
	return s.newRouterWithBootstrapToken(testBootstrapToken)
}

// newRouterWithBootstrapToken is newRouterWithAuth with an explicit bootstrap token; "" models an
// operator who configured none (first-admin setup closed).
func (s *AuthHandlerSuite) newRouterWithBootstrapToken(token string) http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.Accounts = s.auth
	handlerCfg.CSRFSecret = authTestCSRFSecret
	handlerCfg.BootstrapToken = token
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithAuthAndScheme is newRouterWithAuth with --public-scheme forced, used to verify
// that invite links and API-token envelopes honour the override behind a TLS-terminating proxy.
func (s *AuthHandlerSuite) newRouterWithAuthAndScheme(forcedScheme string) http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.Accounts = s.auth
	handlerCfg.CSRFSecret = authTestCSRFSecret
	handlerCfg.ForcedScheme = forcedScheme
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithAuthAndSessionTTL is newRouterWithAuth with a custom SessionTTL, used to verify
// the TOTP session cookie's Max-Age tracks the configured --session-ttl instead of a fixed value.
func (s *AuthHandlerSuite) newRouterWithAuthAndSessionTTL(ttl time.Duration) http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.Accounts = s.auth
	handlerCfg.CSRFSecret = authTestCSRFSecret
	handlerCfg.SessionTTL = ttl
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

func (s *AuthHandlerSuite) adminUser() *domain.User {
	return &domain.User{
		ID: testAdminID, Username: "admin", IsAdmin: true,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
}

// authTestCSRFSecret is a fixed 32-byte secret for auth handler tests.
// Must match the CSRFSecret set in newRouterWithAuth's RouterOptions.
var authTestCSRFSecret = []byte("padmark-test-csrf-secret-32bytes") //nolint:gochecknoglobals // test fixture

// testBootstrapToken is the suite router's first-admin bootstrap token (RouterOptions.BootstrapToken).
const testBootstrapToken = "padmark-test-bootstrap-token-32chars"

// testCSRFToken is a valid HMAC-signed CSRF token generated from authTestCSRFSecret.
// Generated once at test-binary init; all auth tests share it via csrfCookie/withCSRF.
var testCSRFToken = web.GenerateCSRFTokenForTest(authTestCSRFSecret) //nolint:gochecknoglobals // test fixture

func (s *AuthHandlerSuite) sessionCookie(value string) *http.Cookie {
	return httptestutil.RequestCookie("padmark_session", value)
}

func (s *AuthHandlerSuite) csrfCookie() *http.Cookie {
	return httptestutil.RequestCookie("padmark_csrf", testCSRFToken)
}

func withCSRF(form url.Values) url.Values {
	form.Set("csrf_token", testCSRFToken)

	return form
}

// csrfTokens caches the per-session CSRF tokens: the token is bound to the session cookie value,
// so a signed-in POST needs one minted for that session (the anonymous testCSRFToken is, by
// design, rejected there). Each token has a random nonce, so the cache keeps the cookie and the
// form value equal for one session.
//
//nolint:gochecknoglobals // test fixture cache
var csrfTokens = csrfTokenCache{mu: sync.Mutex{}, tokens: map[string]string{}}

// csrfTokenCache is a typed map: a sync.Map would need a type assertion on every read.
type csrfTokenCache struct {
	mu     sync.Mutex
	tokens map[string]string
}

func csrfTokenFor(sessionID string) string {
	csrfTokens.mu.Lock()
	defer csrfTokens.mu.Unlock()

	token, ok := csrfTokens.tokens[sessionID]
	if !ok {
		token = web.GenerateSessionCSRFTokenForTest(authTestCSRFSecret, sessionID)
		csrfTokens.tokens[sessionID] = token
	}

	return token
}

// csrfCookieFor is csrfCookie for a request that also carries sessionCookie(sessionID).
func (s *AuthHandlerSuite) csrfCookieFor(sessionID string) *http.Cookie {
	return httptestutil.RequestCookie("padmark_csrf", csrfTokenFor(sessionID))
}

// withCSRFFor is withCSRF for a request that also carries sessionCookie(sessionID).
func withCSRFFor(form url.Values, sessionID string) url.Values {
	form.Set("csrf_token", csrfTokenFor(sessionID))

	return form
}

// ── Login page ──.

// TestLoginPage_TOTPFormWithoutSignOut: the login page has the TOTP form, and no sign-out form
// because it has no session to end.
func (s *AuthHandlerSuite) TestLoginPage_TOTPFormWithoutSignOut() {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "totp-login") // TOTP form action.
	s.NotContains(rec.Body.String(), `action="/logout"`)
}

// TestLoginPage_AlreadyAuthenticated_RedirectsHome verifies a signed-in user is not asked
// to log in again — /login redirects home instead of rendering the form.
func (s *AuthHandlerSuite) TestLoginPage_AlreadyAuthenticated_RedirectsHome() {
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-ok").Return(&domain.User{
		ID:        testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(s.sessionCookie("sess-ok"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/", rec.Header().Get("Location"))
}

// TestLoginPage_AlreadyAuthenticated_RedirectsToNext honours a safe ?next= target.
func (s *AuthHandlerSuite) TestLoginPage_AlreadyAuthenticated_RedirectsToNext() {
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-ok").Return(&domain.User{
		ID:        testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/login?next=/edit/abc", nil)
	req.AddCookie(s.sessionCookie("sess-ok"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/edit/abc", rec.Header().Get("Location"))
}

// ── Sign out control and session replacement ──.

// TestSignOutForm_RenderedOnSignedInPages — the pages only a signed-in user can reach carry a
// POST /logout form with the CSRF token, so there is a way to end the session from the UI.
func (s *AuthHandlerSuite) TestSignOutForm_RenderedOnSignedInPages() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).AnyTimes()
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil).AnyTimes()
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil).AnyTimes()

	for _, path := range []string{"/admin", "/change-password"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(s.sessionCookie("admin-sess"))
		req.AddCookie(s.csrfCookieFor("admin-sess"))

		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)

		s.Require().Equal(http.StatusOK, rec.Code, path)
		body := rec.Body.String()
		s.Contains(body, `<form method="POST" action="/logout">`, path)
		s.Contains(body, "Sign out", path)

		// The page's logout form must carry a token that the guard accepts for this session.
		s.Regexp(`action="/logout">\s*<input type="hidden" name="csrf_token" value="`+
			regexp.QuoteMeta(csrfTokenFor("admin-sess")), body, path)
	}
}

// TestTOTPLogin_ForwardsPresentedSessionForRevocation — signing in again hands the session the
// browser presented to the usecase, which revokes it; the response carries the replacement.
func (s *AuthHandlerSuite) TestTOTPLogin_ForwardsPresentedSessionForRevocation() {
	s.auth.EXPECT().Login(gomock.Any(), loginWith("alice", "", "123456", "sess-old")).
		Return("sess-new", nil)

	form := withCSRFFor(url.Values{"username": {"alice"}, "password": {"ValidP@ss12!"}, "code": {"123456"}}, "sess-old")
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess-old"))
	req.AddCookie(s.csrfCookieFor("sess-old"))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusSeeOther, rec.Code)

	var sessionCookie, csrfCookie *http.Cookie

	for _, ck := range rec.Result().Cookies() {
		switch ck.Name {
		case "padmark_session":
			sessionCookie = ck
		case "padmark_csrf":
			csrfCookie = ck
		}
	}

	s.Require().NotNil(sessionCookie)
	s.Equal("sess-new", sessionCookie.Value)
	s.Require().NotNil(csrfCookie, "the CSRF cookie is re-minted for the new session")
	s.True(web.VerifyCSRFTokenForTest(csrfCookie.Value, authTestCSRFSecret, "sess-new"))
}

// ── TOTP login ──.

// TestTOTPLogin_Success_Sets30DayCookieAndRedirects: a valid TOTP login redirects home and sets
// the session cookie with a Max-Age of 30 days.
func (s *AuthHandlerSuite) TestTOTPLogin_Success_Sets30DayCookieAndRedirects() {
	s.auth.EXPECT().Login(gomock.Any(), loginWith("alice", "", "123456", "")).
		Return("sess-id-abc", nil)

	form := withCSRF(url.Values{"username": {"alice"}, "password": {"ValidP@ss12!"}, "code": {"123456"}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/", rec.Header().Get("Location"))

	const expectedMaxAge = int(30 * 24 * time.Hour / time.Second)

	found := false

	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "padmark_session" && ck.Value == "sess-id-abc" {
			found = true

			s.Equal(expectedMaxAge, ck.MaxAge)
		}
	}

	s.True(found, "padmark_session cookie must be set")
}

func (s *AuthHandlerSuite) TestTOTPLogin_Invalid_RedirectsWithError() {
	s.auth.EXPECT().Login(gomock.Any(), loginWith("alice", "", "000000", "")).
		Return("", domain.ErrInvalidTOTP)

	form := withCSRF(url.Values{"username": {"alice"}, "password": {"ValidP@ss12!"}, "code": {"000000"}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Contains(rec.Header().Get("Location"), "/login?error=1")
}

func (s *AuthHandlerSuite) TestTOTPLogin_WithNext_RedirectsToNext() {
	s.auth.EXPECT().Login(gomock.Any(), loginWith("alice", "", "123456", "")).
		Return("sess-id", nil)

	next := url.QueryEscape("/admin")
	form := withCSRF(url.Values{
		"username": {"alice"}, "password": {"ValidP@ss12!"}, "code": {"123456"}, "next": {"/admin"},
	})
	req := httptest.NewRequest(http.MethodPost, "/totp-login?next="+next, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/admin", rec.Header().Get("Location"))
}

// ── Logout ──.

func (s *AuthHandlerSuite) TestLogout_DeletesSessionAndClears() {
	// /logout is a public path — middleware skips GetSession.
	// LogoutHandler reads the cookie directly and calls Logout.
	s.auth.EXPECT().Logout(gomock.Any(), "sess-xyz").Return(nil)

	form := withCSRFFor(url.Values{}, "sess-xyz")
	req := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess-xyz"))
	req.AddCookie(s.csrfCookieFor("sess-xyz"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/login", rec.Header().Get("Location"))

	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "padmark_session" {
			s.LessOrEqual(ck.MaxAge, 0, "session cookie must be cleared")
		}
	}
}

func (s *AuthHandlerSuite) TestLogout_SessionDeleteFails_Returns500() {
	s.auth.EXPECT().Logout(gomock.Any(), "sess-xyz").Return(domain.ErrSessionExpired)

	form := withCSRFFor(url.Values{}, "sess-xyz")
	req := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess-xyz"))
	req.AddCookie(s.csrfCookieFor("sess-xyz"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusInternalServerError, rec.Code)

	// Cookie must NOT be cleared when server-side deletion failed.
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "padmark_session" {
			s.Fail("session cookie must not be set in response when logout fails")
		}
	}
}

func (s *AuthHandlerSuite) TestLogout_NoSession_StillRedirects() {
	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/login", rec.Header().Get("Location"))
}

// ── Session expiry ──.

// TestTOTPLogin_SessionCookieMaxAge_TracksConfiguredSessionTTL covers RouterOptions.SessionTTL:
// the cookie's Max-Age must match whatever --session-ttl is configured to, not a value
// independent of it — otherwise the browser could drop the cookie before the server-side
// session actually expires (or keep sending it long after), see common.sessionMaxAge.
func (s *AuthHandlerSuite) TestTOTPLogin_SessionCookieMaxAge_TracksConfiguredSessionTTL() {
	const configuredTTL = 2 * time.Hour

	s.router = s.newRouterWithAuthAndSessionTTL(configuredTTL)

	s.auth.EXPECT().Login(gomock.Any(), loginWith("alice", "", "123456", "")).
		Return("sess-id", nil)

	form := withCSRF(url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	expectedMaxAge := int(configuredTTL / time.Second)
	found := false

	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "padmark_session" {
			s.Equal(expectedMaxAge, ck.MaxAge)

			found = true
		}
	}

	s.True(found, "session cookie must be set")
}

// renderedCSRFField extracts the hidden csrf_token value from a rendered HTML form.
func (s *AuthHandlerSuite) renderedCSRFField(body string) string {
	s.T().Helper()

	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(body)
	s.Require().Len(match, 2, "rendered page must carry a csrf_token field")

	return match[1]
}

// loginRequestMatcher matches an auth.LoginRequest with username and code. An empty password or
// previous session ID matches each value, as gomock.Any() did for the positional parameters.
type loginRequestMatcher struct {
	username, password, code, previous string
}

func loginWith(username, password, code, previous string) loginRequestMatcher {
	return loginRequestMatcher{username: username, password: password, code: code, previous: previous}
}

func (m loginRequestMatcher) Matches(x any) bool {
	req, ok := x.(*auth.LoginRequest)

	return ok && req.Username == m.username && req.TOTPCode == m.code &&
		(m.password == "" || req.Password == m.password) &&
		(m.previous == "" || req.PreviousSessionID == m.previous)
}

func (m loginRequestMatcher) String() string {
	return "login request of " + m.username + " with code " + m.code
}
