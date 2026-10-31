package http_test

import (
	"encoding/json"
	"errors"
	"fmt"
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

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
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
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog).
		WithAuthManager(s.auth)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		MaxBodyBytes:   256 * 1024,
		CSRFSecret:     authTestCSRFSecret,
		BootstrapToken: token,

		ForcedScheme: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0, RateBurst: 0,
		SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithAuthAndScheme is newRouterWithAuth with --public-scheme forced, used to verify
// that invite links and API-token envelopes honour the override behind a TLS-terminating proxy.
func (s *AuthHandlerSuite) newRouterWithAuthAndScheme(forcedScheme string) http.Handler {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog).
		WithAuthManager(s.auth)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   authTestCSRFSecret,
		ForcedScheme: forcedScheme,

		BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0, RateBurst: 0,
		SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithAuthAndSessionTTL is newRouterWithAuth with a custom SessionTTL, used to verify
// the TOTP session cookie's Max-Age tracks the configured --session-ttl instead of a fixed value.
func (s *AuthHandlerSuite) newRouterWithAuthAndSessionTTL(ttl time.Duration) http.Handler {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog).
		WithAuthManager(s.auth)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   authTestCSRFSecret,
		SessionTTL:   ttl,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, DisableAPI: false, DirectRedirect: false,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

func (s *AuthHandlerSuite) adminUser() *domain.User {
	return &domain.User{ID: testAdminID, Username: "admin", IsAdmin: true,
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
var testCSRFToken = adhttp.GenerateCSRFTokenForTest(authTestCSRFSecret) //nolint:gochecknoglobals // test fixture

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
// design, rejected there).
var csrfTokens sync.Map //nolint:gochecknoglobals // test fixture cache

func csrfTokenFor(sessionID string) string {
	if tok, ok := csrfTokens.Load(sessionID); ok {
		return tok.(string) //nolint:forcetypeassert // only strings are stored
	}

	tok, _ := csrfTokens.LoadOrStore(sessionID, adhttp.GenerateSessionCSRFTokenForTest(authTestCSRFSecret, sessionID))

	return tok.(string) //nolint:forcetypeassert // only strings are stored
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

// ── Login page ──

func (s *AuthHandlerSuite) TestLoginPage_TOTP_Mode() {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "totp-login") // TOTP form action
}

// TestLoginPage_AlreadyAuthenticated_RedirectsHome verifies a signed-in user is not asked
// to log in again — /login redirects home instead of rendering the form.
func (s *AuthHandlerSuite) TestLoginPage_AlreadyAuthenticated_RedirectsHome() {
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-ok").Return(&domain.User{ID: testUserID1,
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
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-ok").Return(&domain.User{ID: testUserID1,
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

// ── Sign out control and session replacement ──

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

// TestSignOutForm_NotRenderedForAnonymousPages — the login page has no session to end.
func (s *AuthHandlerSuite) TestSignOutForm_NotRenderedForAnonymousPages() {
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))

	s.Require().Equal(http.StatusOK, rec.Code)
	s.NotContains(rec.Body.String(), `action="/logout"`)
}

// TestTOTPLogin_ForwardsPresentedSessionForRevocation — signing in again hands the session the
// browser presented to the usecase, which revokes it; the response carries the replacement.
func (s *AuthHandlerSuite) TestTOTPLogin_ForwardsPresentedSessionForRevocation() {
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any(), "sess-old").
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
	s.True(adhttp.VerifyCSRFTokenForTest(csrfCookie.Value, authTestCSRFSecret, "sess-new"))
}

// ── TOTP login ──

func (s *AuthHandlerSuite) TestTOTPLogin_Success_SetsCookieAndRedirects() {
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any(), gomock.Any()).
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

	found := false

	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "padmark_session" && ck.Value == "sess-id-abc" {
			found = true
		}
	}

	s.True(found, "padmark_session cookie must be set")
}

func (s *AuthHandlerSuite) TestTOTPLogin_Invalid_RedirectsWithError() {
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "000000", gomock.Any(), gomock.Any(), gomock.Any()).
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
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any(), gomock.Any()).
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

// ── Logout ──

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

// ── Setup page ──

func (s *AuthHandlerSuite) TestSetupPage_WithInviteToken_RendersForm() {
	req := httptest.NewRequest(http.MethodGet, "/setup?invite=tok-abc", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "tok-abc")
}

func (s *AuthHandlerSuite) TestSetupPage_NoToken_EmptyDB_RendersFirstAdmin() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil)

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "first admin")
	s.Contains(rec.Body.String(), `name="bootstrap_token"`)
}

// TestSetupPage_NoBootstrapToken_ShowsDisabled: with no bootstrap token configured the first-admin
// form is not offered at all — nobody could prove ownership of the server (M-2).
func (s *AuthHandlerSuite) TestSetupPage_NoBootstrapToken_ShowsDisabled() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil)

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	req.Header.Set("Accept", "text/html")

	rec := httptest.NewRecorder()

	s.newRouterWithBootstrapToken("").ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
	s.Contains(rec.Body.String(), "PADMARK_BOOTSTRAP_TOKEN")
	s.NotContains(rec.Body.String(), `name="bootstrap_token"`)
}

// TestSetupHandler_FirstAdmin_BootstrapTokenRequired: a first-admin form post with a missing or
// wrong bootstrap token re-renders the form with 403 and never reaches AcceptFirstAdmin (M-2).
func (s *AuthHandlerSuite) TestSetupHandler_FirstAdmin_BootstrapTokenRequired() {
	for _, presented := range []string{"", "wrong-token", testBootstrapToken + "x"} {
		s.Run("token="+presented, func() {
			s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil)
			// AcceptFirstAdmin must NOT be called.

			form := withCSRF(url.Values{
				"username":         {"root"},
				"password":         {testPw},
				"password_confirm": {testPw},
				"bootstrap_token":  {presented},
			})
			req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "text/html")
			req.AddCookie(s.csrfCookie())

			rec := httptest.NewRecorder()

			s.router.ServeHTTP(rec, req)

			s.Equal(http.StatusForbidden, rec.Code)
			s.Contains(rec.Header().Get("Content-Type"), "text/html")
			s.Contains(rec.Body.String(), "Invalid bootstrap token.")
		})
	}
}

// TestSetupHandler_FirstAdmin_NoBootstrapTokenConfigured: with none configured even a form post
// carrying some token is refused with the disabled page.
func (s *AuthHandlerSuite) TestSetupHandler_FirstAdmin_NoBootstrapTokenConfigured() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil)

	form := withCSRF(url.Values{
		"username":         {"root"},
		"password":         {testPw},
		"password_confirm": {testPw},
		"bootstrap_token":  {testBootstrapToken},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.newRouterWithBootstrapToken("").ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
	s.Contains(rec.Body.String(), "Setup is disabled")
}

func (s *AuthHandlerSuite) TestSetupPage_NoToken_NotEmpty_Returns403() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(false, nil)

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	// Bootstrap is closed once an admin exists: /setup without an invite must error,
	// not redirect to login.
	s.Equal(http.StatusForbidden, rec.Code)
	s.Contains(rec.Body.String(), "Setup is closed")
}

// ── Setup handler ──

const testPw = "ValidP@ss12!"

func (s *AuthHandlerSuite) TestSetupHandler_WithInvite_ShowsQR() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "good-tok", "newuser", gomock.Any()).
		Return(&auth.Enrollment{QRCode: "data:image/png;base64,abc123",
			TOTPURI: "",
		}, nil)

	form := withCSRF(url.Values{
		"invite":           {"good-tok"},
		"username":         {"newuser"},
		"password":         {testPw},
		"password_confirm": {testPw},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "data:image/png;base64,")
}

func (s *AuthHandlerSuite) TestSetupHandler_FirstAdmin_ShowsQR() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil) // bootstrap allowed: empty DB
	s.auth.EXPECT().AcceptFirstAdmin(gomock.Any(), "root", gomock.Any()).
		Return(&auth.Enrollment{QRCode: "data:image/png;base64,xyz",
			TOTPURI: "",
		}, nil)

	form := withCSRF(url.Values{
		"username":         {"root"},
		"password":         {testPw},
		"password_confirm": {testPw},
		"bootstrap_token":  {testBootstrapToken},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Scan QR code")
}

// TestSetupHandler_NoInvite_NotEmpty_Returns403 verifies POST /setup without an invite is
// rejected once an admin exists (closed bootstrap), instead of attempting first-admin creation.
func (s *AuthHandlerSuite) TestSetupHandler_NoInvite_NotEmpty_Returns403() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(false, nil)
	// AcceptFirstAdmin must NOT be called.

	form := withCSRF(url.Values{
		"username":         {"intruder"},
		"password":         {testPw},
		"password_confirm": {testPw},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
	s.Contains(rec.Body.String(), "Setup is closed")
}

func (s *AuthHandlerSuite) TestSetupHandler_PasswordMismatch_ShowsError() {
	form := withCSRF(url.Values{
		"invite":           {"tok"},
		"username":         {"user"},
		"password":         {testPw},
		"password_confirm": {"DifferentP@ss12!"},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "do not match")
}

func (s *AuthHandlerSuite) TestSetupHandler_ExpiredInvite_ShowsError() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "expired", "user", gomock.Any()).
		Return(nil, domain.ErrInviteExpired)

	form := withCSRF(url.Values{
		"invite":           {"expired"},
		"username":         {"user"},
		"password":         {testPw},
		"password_confirm": {testPw},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "expired")
}

func (s *AuthHandlerSuite) TestSetupHandler_DuplicateUsername_ShowsError() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "tok", "taken", gomock.Any()).
		Return(nil, domain.ErrUserExists)

	form := withCSRF(url.Values{
		"invite":           {"tok"},
		"username":         {"taken"},
		"password":         {testPw},
		"password_confirm": {testPw},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "taken")
}

// ── Admin page ──

func (s *AuthHandlerSuite) TestAdminPage_NoSession_Returns303() {
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Sec-Fetch-Dest", "document") // simulate browser navigation

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	// no session → authMgr == only auth → middleware blocks (no token either) → redirect to login
	s.Equal(http.StatusSeeOther, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminPage_NonAdmin_Returns403() {
	nonAdmin := &domain.User{ID: testUserID1, Username: "alice", IsAdmin: false,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(s.sessionCookie("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminPage_Admin_ListsUsers() {
	usr := s.adminUser()
	users := []*domain.User{usr, {ID: testUserID2, Username: "bob",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}}

	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return(users, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(s.sessionCookie("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "bob")
}

func (s *AuthHandlerSuite) TestAdminPage_Admin_ListsAPIKeys() {
	usr := s.adminUser()
	tokens := []*auth.APITokenInfo{
		{ID: "abcdef0123456789deadbeef", UserID: testAdminID, Username: "admin", CreatedAt: time.Now(),
			ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
		},
	}

	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return(tokens, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(s.sessionCookie("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "abcdef012345", "token hash prefix (first 12 chars) is shown")
	s.Contains(rec.Body.String(), "never used", "unused token renders 'never used'")
}

// ── Admin invite ──

func (s *AuthHandlerSuite) TestAdminInvite_GeneratesLink() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("tok-xyz", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "tok-xyz")
}

// adminInviteRequest builds a POST /admin/invite carrying admin-sess and a CSRF cookie/field pair
// minted for csrfSession ("" = the anonymous fixture), plus optional extra headers.
func (s *AuthHandlerSuite) adminInviteRequest(csrfSession string, headers map[string]string) *http.Request {
	token := testCSRFToken
	if csrfSession != "" {
		token = csrfTokenFor(csrfSession)
	}

	form := url.Values{"csrf_token": {token}}
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(httptestutil.RequestCookie("padmark_csrf", token))

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	return req
}

// TestAdminInvite_CSRFTokenFromBeforeSignInIsRejected — a cookie/field pair minted for an
// anonymous visitor (what a cookie-tossing attacker can produce) must not authorise a POST made
// with a real session cookie.
func (s *AuthHandlerSuite) TestAdminInvite_CSRFTokenFromBeforeSignInIsRejected() {
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(s.adminUser(), nil).AnyTimes()

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, s.adminInviteRequest("", nil))

	s.Equal(http.StatusForbidden, rec.Code)
}

// TestAdminInvite_CrossSiteOriginIsRejected — a valid session-bound pair is still refused when
// the browser says the request came from another site; the same pair passes same-origin.
func (s *AuthHandlerSuite) TestAdminInvite_CrossSiteOriginIsRejected() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).AnyTimes()
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("tok-xyz", nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil).Times(1)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil).Times(1)

	for name, headers := range map[string]map[string]string{
		"foreign Origin":       {"Origin": "http://evil.test"},
		"cross-site fetch":     {"Sec-Fetch-Site": "cross-site"},
		"sibling-site fetch":   {"Sec-Fetch-Site": "same-site"},
		"opaque (null) Origin": {"Origin": "null"},
	} {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, s.adminInviteRequest("admin-sess", headers))
		s.Equal(http.StatusForbidden, rec.Code, name)
	}

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, s.adminInviteRequest("admin-sess",
		map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}))
	s.Equal(http.StatusOK, rec.Code, "same-origin request with a session-bound token must pass")
}

func (s *AuthHandlerSuite) TestAdminInvite_ForcedSchemeEmbedsHTTPS() {
	// Behind a TLS-terminating proxy the request arrives as plain HTTP, but --public-scheme=https
	// is forced, so the invite link the admin copies must use https regardless — otherwise the
	// invitee is sent to an http:// setup URL that the proxy may not even serve.
	s.router = s.newRouterWithAuthAndScheme("https")

	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("tok-xyz", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	// httptest.NewRequest defaults r.Host to example.com; the link is scheme://host/setup?invite=<token>.
	s.Contains(rec.Body.String(), "https://example.com/setup?invite=tok-xyz")
	s.NotContains(rec.Body.String(), "http://example.com/setup")
}

func (s *AuthHandlerSuite) TestAdminInvite_GenerateInviteFails_ShowsError() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("", errors.New("boom"))
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Failed to generate invite link")
}

func (s *AuthHandlerSuite) TestAdminInvite_AdminDataFails_Returns500() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return(nil, errors.New("boom"))

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusInternalServerError, rec.Code)
}

// ── Admin revoke ──

func (s *AuthHandlerSuite) TestAdminRevoke_RevokesAndRedirects() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeUser(gomock.Any(), testAdminID, testTargetID).Return(nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost,
		"/admin/users/"+testTargetID.String()+"/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/admin", rec.Header().Get("Location"))
}

func (s *AuthHandlerSuite) TestAdminRevoke_NonAdmin_Returns403() {
	nonAdmin := &domain.User{ID: testUserID1, Username: "alice", IsAdmin: false,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	form := withCSRFFor(url.Values{}, "sess1")
	req := httptest.NewRequest(http.MethodPost,
		"/admin/users/"+testTargetID.String()+"/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminRevoke_Forbidden_RedirectsWithSpecificMessage() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeUser(gomock.Any(), testAdminID, testTargetID).Return(domain.ErrForbidden)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost,
		"/admin/users/"+testTargetID.String()+"/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	loc := rec.Header().Get("Location")
	s.Contains(loc, "revoke_error=")
	s.Contains(loc, url.QueryEscape("self-revoke or last admin"))
}

func (s *AuthHandlerSuite) TestAdminRevoke_GenericError_RedirectsWithGenericMessage() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeUser(gomock.Any(), testAdminID, testTargetID).Return(errors.New("boom"))

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost,
		"/admin/users/"+testTargetID.String()+"/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	loc := rec.Header().Get("Location")
	s.Contains(loc, "revoke_error=")
	s.Contains(loc, url.QueryEscape("Failed to revoke user."))
}

// ── Admin API keys ──

// TestAdminCreateKey_Lifetime: the admin form's lifetime reaches the usecase as a session issue;
// an unparsable value is passed on as invalid and the refusal is shown on the page.
func (s *AuthHandlerSuite) TestAdminCreateKey_Lifetime() {
	usr := s.adminUser()

	for _, testCase := range []struct {
		form string
		ttl  int
		err  error
	}{
		{"90", 90, nil},
		{"", 0, nil},
		{"abc", -1, domain.ErrInvalidAPITokenTTL},
	} {
		s.Run("ttl_days="+testCase.form, func() {
			s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil)
			s.auth.EXPECT().CreateAPIToken(gomock.Any(), testAdminID, auth.Issuer{
				APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
			}, testCase.ttl).
				Return("plain-secret-key", testCase.err)
			s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
			s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

			form := withCSRFFor(url.Values{"ttl_days": {testCase.form}}, "admin-sess")
			req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(s.sessionCookie("admin-sess"))
			req.AddCookie(s.csrfCookieFor("admin-sess"))

			rec := httptest.NewRecorder()

			s.router.ServeHTTP(rec, req)

			s.Equal(http.StatusOK, rec.Code)

			if testCase.err != nil {
				s.Contains(rec.Body.String(), "Lifetime must be 0 (never expires) to 3650 days.")
			}
		})
	}
}

func (s *AuthHandlerSuite) TestAdminCreateKey_CreatesAndShowsKey() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testAdminID, gomock.Any(), gomock.Any()).Return("plain-secret-key", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)

	// The page shows a self-contained envelope (base URL + key), not the bare key: the raw key is
	// base64-packed inside it, so it must not leak verbatim, and the envelope must decode back to
	// the key plus the request's own URL (httptest defaults r.Host to example.com over http).
	envelope, err := domain.EncodeAPITokenEnvelope("http://example.com", "plain-secret-key")
	s.Require().NoError(err)
	s.Contains(rec.Body.String(), envelope, "envelope token is shown exactly once on the page")
	s.NotContains(rec.Body.String(), ">plain-secret-key<", "raw key must not appear outside the envelope")

	gotURL, gotKey, ok := domain.DecodeAPITokenEnvelope(envelope)
	s.True(ok)
	s.Equal("http://example.com", gotURL)
	s.Equal("plain-secret-key", gotKey)
}

func (s *AuthHandlerSuite) TestAdminCreateKey_ForcedSchemeEmbedsHTTPS() {
	// Behind a TLS-terminating proxy the request itself still arrives as plain HTTP, but the
	// operator has forced --public-scheme=https, so the envelope embedded in the page must use
	// https regardless — otherwise the CLI decodes an http:// URL from a key meant for https.
	s.router = s.newRouterWithAuthAndScheme("https")

	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testAdminID, gomock.Any(), gomock.Any()).Return("plain-secret-key", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)

	envelope, err := domain.EncodeAPITokenEnvelope("https://example.com", "plain-secret-key")
	s.Require().NoError(err)
	s.Contains(rec.Body.String(), envelope)

	gotURL, _, ok := domain.DecodeAPITokenEnvelope(envelope)
	s.True(ok)
	s.Equal("https://example.com", gotURL)
}

func (s *AuthHandlerSuite) TestAdminCreateKey_NonAdmin_Returns403() {
	nonAdmin := &domain.User{ID: testUserID1, Username: "alice", IsAdmin: false,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	form := withCSRFFor(url.Values{}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminRevokeKey_RevokesAndRedirects() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeAPIToken(gomock.Any(), testAdminID, "hash-123").Return(nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/hash-123/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/admin", rec.Header().Get("Location"))
}

func (s *AuthHandlerSuite) TestAdminRevokeKey_NonAdmin_Returns403() {
	nonAdmin := &domain.User{ID: testUserID1, Username: "alice", IsAdmin: false,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	form := withCSRFFor(url.Values{}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/hash-123/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminCreateKey_CreateFails_ShowsError() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testAdminID, gomock.Any(), gomock.Any()).
		Return("", domain.ErrFeatureNotSupported)
	s.auth.EXPECT().ListUsers(gomock.Any(), testAdminID).Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), testAdminID).Return([]*auth.APITokenInfo{}, nil)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Failed to create API key.", "create failure renders an inline error")
}

func (s *AuthHandlerSuite) TestAdminRevokeKey_RevokeFails_RedirectsWithError() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeAPIToken(gomock.Any(), testAdminID, "hash-123").Return(domain.ErrNotFound)

	form := withCSRFFor(url.Values{}, "admin-sess")
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/hash-123/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookieFor("admin-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Contains(rec.Header().Get("Location"), "key_error")
}

// ── Session auth middleware ──

func (s *AuthHandlerSuite) TestSessionAuth_ValidSession_PassesThrough() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	// GetSession called twice: once by middleware, once by handler (isAuthenticated check inside GetNote may differ)
	// For GET / (IndexPage) which is always served — just once by middleware
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(s.sessionCookie("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
}

func (s *AuthHandlerSuite) TestSessionAuth_ExpiredSession_RedirectsToLogin() {
	s.auth.EXPECT().GetSession(gomock.Any(), "bad-sess").Return(nil, domain.ErrSessionExpired)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.sessionCookie("bad-sess"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Contains(rec.Header().Get("Location"), "/login")
}

// ── API-token auth middleware ──

func (s *AuthHandlerSuite) TestAPITokenAuth_ValidBearer_PassesThrough() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer good-api-key")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
}

func (s *AuthHandlerSuite) TestAPITokenAuth_UnknownBearer_Returns401() {
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "bad-key").Return(nil, "", domain.ErrNotFound)

	// No Sec-Fetch-Dest / text-html Accept ⇒ treated as an API client ⇒ 401, not a redirect.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bad-key")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusUnauthorized, rec.Code)
}

// TestAPITokenAuth_AdminBearer_ReachesAdminPage proves the user resolved from the API token is
// injected into the request context: an admin key reaches the admin-only page and renders it.
// TestAPITokenAuth_AdminBearer_RefusedByAdminPanel: the admin panel is a browser UI — an admin's
// bearer API token must not reach it, or a leaked key could mint keys and invites through its
// forms without the step-up the JSON endpoints demand (M-6).
func (s *AuthHandlerSuite) TestAPITokenAuth_AdminBearer_RefusedByAdminPanel() {
	admin := s.adminUser()

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/admin"},
		{http.MethodPost, "/admin/api-keys"},
		{http.MethodPost, "/admin/invite"},
	} {
		s.Run(route.method+" "+route.path, func() {
			s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "admin-key").Return(admin, "token-hash", nil)
			// No ListUsers / CreateAPIToken / GenerateInvite: the handler is never reached.

			form := withCSRF(url.Values{"ttl_days": {"0"}})
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Authorization", "Bearer admin-key")
			req.AddCookie(s.csrfCookie())

			rec := httptest.NewRecorder()

			s.router.ServeHTTP(rec, req)

			s.Equal(http.StatusForbidden, rec.Code)
		})
	}
}

// TestAPITokenAuth_PrivateNote_ValidBearer_Served is a regression test: GET /notes/{id} is a
// "public" route (auth not required), so the middleware must still resolve an API-token Bearer
// key there — otherwise the CLI, whose only credential is that key, gets 401 on a private note.
func (s *AuthHandlerSuite) TestAPITokenAuth_PrivateNote_ValidBearer_Served() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	priv := domain.PrivacyAuthenticated
	note := newTestNote("secret", "# private")
	note.Privacy = &priv

	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().ViewPreloaded(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	req := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	req.Header.Set("Authorization", "Bearer good-api-key")
	req.Header.Set("Accept", "application/json")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code, "a valid API token must unlock a private note on the public GET route")
}

// TestAPITokenAuth_PrivateNote_UnknownBearer_Returns401 is the negative counterpart: an unknown
// API key must not reach a private note.
func (s *AuthHandlerSuite) TestAPITokenAuth_PrivateNote_UnknownBearer_Returns401() {
	priv := domain.PrivacyAuthenticated
	note := newTestNote("secret", "# private")
	note.Privacy = &priv

	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "bad-key").Return(nil, "", domain.ErrNotFound)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	req := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	req.Header.Set("Authorization", "Bearer bad-key")
	req.Header.Set("Accept", "application/json")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusUnauthorized, rec.Code)
}

// TestGetNote_NotFound_JSONClient_GetsJSONError is a regression test: a not-found note (e.g. an
// already-burned one) requested by an API/CLI client must return a JSON ErrorResponse
// ({"message": ...}) matching the OpenAPI spec, so the ogen client decodes it into a typed error
// instead of failing on an unexpected content type (was: text/plain, then text/html).
func (s *AuthHandlerSuite) TestGetNote_NotFound_JSONClient_GetsJSONError() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(nil, domain.ErrNotFound)

	req := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	req.Header.Set("Authorization", "Bearer good-api-key")
	req.Header.Set("Accept", "application/json")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusNotFound, rec.Code)
	s.Contains(rec.Header().Get("Content-Type"), "application/json",
		"an API client must get a JSON error body per the OpenAPI ErrorResponse schema")

	var body struct {
		Message string `json:"message"`
	}
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	s.Equal("not found", body.Message)
}

// TestGetNote_NotFound_BrowserClient_GetsHTMLPage locks in the other half of the negotiation:
// a browser still gets the styled HTML error page.
func (s *AuthHandlerSuite) TestGetNote_NotFound_BrowserClient_GetsHTMLPage() {
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(nil, domain.ErrNotFound)

	req := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	req.Header.Set("Accept", "text/html")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusNotFound, rec.Code)
	s.Contains(rec.Header().Get("Content-Type"), "text/html")
}

// ── Change password ──

func (s *AuthHandlerSuite) TestChangePasswordPage_Authenticated_RendersForm() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)

	req := httptest.NewRequest(http.MethodGet, "/change-password", nil)
	req.AddCookie(s.sessionCookie("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "old_password")
}

func (s *AuthHandlerSuite) TestChangePasswordHandler_Success_ShowsSuccessMessage() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "OldP@ss12!", "NewP@ss12!", "123456").Return("new-sess-id", nil)

	form := withCSRFFor(url.Values{
		"old_password":         {"OldP@ss12!"},
		"new_password":         {"NewP@ss12!"},
		"new_password_confirm": {"NewP@ss12!"},
		"totp_code":            {"123456"},
	}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Password changed successfully")

	// New session cookie must be set so old stolen cookies stop working.
	var newCookie *http.Cookie

	for _, c := range rec.Result().Cookies() {
		if c.Name == "padmark_session" {
			newCookie = c
		}
	}

	s.Require().NotNil(newCookie, "session cookie must be rotated after password change")
	s.Equal("new-sess-id", newCookie.Value)
}

func (s *AuthHandlerSuite) TestChangePasswordHandler_Mismatch_ShowsError() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)

	form := withCSRFFor(url.Values{
		"old_password":         {"OldP@ss12!"},
		"new_password":         {"NewP@ss12!"},
		"new_password_confirm": {"DifferentP@ss12!"},
	}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "do not match")
}

func (s *AuthHandlerSuite) TestChangePasswordHandler_WrongOldPassword_ShowsError() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "WrongP@ss12!", "NewP@ss12!", "123456").
		Return("", domain.ErrInvalidPassword)

	form := withCSRFFor(url.Values{
		"old_password":         {"WrongP@ss12!"},
		"new_password":         {"NewP@ss12!"},
		"new_password_confirm": {"NewP@ss12!"},
		"totp_code":            {"123456"},
	}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "incorrect")
}

func (s *AuthHandlerSuite) TestChangePasswordHandler_InvalidTOTP_ShowsError() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "OldP@ss12!", "NewP@ss12!", "badcode").
		Return("", domain.ErrInvalidTOTP)

	form := withCSRFFor(url.Values{
		"old_password":         {"OldP@ss12!"},
		"new_password":         {"NewP@ss12!"},
		"new_password_confirm": {"NewP@ss12!"},
		"totp_code":            {"badcode"},
	}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "TOTP code is invalid")
}

func (s *AuthHandlerSuite) TestSetupHandler_PasswordMismatch_DoesNotCallAuthMgr() {
	// authMgr must NOT be called when passwords don't match — verify via strict mock.
	// (No EXPECT() set on s.auth — any unexpected call would fail the test.)
	form := withCSRF(url.Values{
		"username":         {"newuser"},
		"password":         {testPw},
		"password_confirm": {"DifferentP@ss12!"},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "do not match")
}

// ── CSRF guard ──

func (s *AuthHandlerSuite) TestCSRFGuard_MissingToken_Returns403() {
	// POST without CSRF cookie must be rejected before reaching the handler.
	form := url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}}
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// no padmark_csrf cookie

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
	req.AddCookie(s.csrfCookie()) // cookie = testCSRFToken, field = "wrong-token"

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

// TestCSRFGuard_InvalidHMAC_Returns403 covers the branch where cookie and form field match
// (so the equality check passes) but the token's HMAC signature is invalid — e.g. a token
// minted with a different secret. The guard must still reject it via VerifyCSRFToken.
func (s *AuthHandlerSuite) TestCSRFGuard_InvalidHMAC_Returns403() {
	forgedSecret := []byte("a-different-csrf-secret-32bytes!!")
	forged := adhttp.GenerateCSRFTokenForTest(forgedSecret)

	form := url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}, "csrf_token": {forged}}
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Cookie == field (equality check passes) but the HMAC was signed with the wrong secret.
	req.AddCookie(httptestutil.RequestCookie("padmark_csrf", forged))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

// ── CSRF secret sharing across processes ──

// newRouterWithCSRFSecret builds an independent router instance with the given CSRF secret. Two
// of them stand in for two replicas, or for the same server before and after a restart.
func (s *AuthHandlerSuite) newRouterWithCSRFSecret(secret []byte) http.Handler {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog).
		WithAuthManager(s.auth)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   secret,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
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
		Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any(), gomock.Any()).
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
		Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any(), gomock.Any()).
		Return("sess-id", nil)

	before := s.newRouterWithCSRFSecret(nil)
	after := s.newRouterWithCSRFSecret(nil)

	cookie := s.issuedCSRFCookie(before)

	s.Equal(http.StatusForbidden, s.postTOTPLoginWithCSRF(after, cookie),
		"without a shared secret the other instance must reject the token")
	s.NotEqual(http.StatusForbidden, s.postTOTPLoginWithCSRF(before, cookie),
		"the minting instance must still accept its own token")
}

// ── Session expiry ──

func (s *AuthHandlerSuite) TestTOTPLogin_SessionCookieMaxAge_Is30Days() {
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any(), gomock.Any()).
		Return("sess-id", nil)

	form := withCSRF(url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	const expectedMaxAge = int(30 * 24 * time.Hour / time.Second)

	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "padmark_session" {
			s.Equal(expectedMaxAge, ck.MaxAge)
		}
	}
}

// TestTOTPLogin_SessionCookieMaxAge_TracksConfiguredSessionTTL covers RouterOptions.SessionTTL:
// the cookie's Max-Age must match whatever --session-ttl is configured to, not a value
// independent of it — otherwise the browser could drop the cookie before the server-side
// session actually expires (or keep sending it long after), see common.sessionMaxAge.
func (s *AuthHandlerSuite) TestTOTPLogin_SessionCookieMaxAge_TracksConfiguredSessionTTL() {
	const configuredTTL = 2 * time.Hour

	s.router = s.newRouterWithAuthAndSessionTTL(configuredTTL)

	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any(), gomock.Any()).
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

// ── Owner bypass for edit_code (session and API-token callers) ──

// TestUpdateNote_SessionOwner_BypassesEditCode verifies that a browser session (the ogen
// single-segment PUT /notes/{id} route) resolves the caller and lets the note's owner update it
// with no edit_code.
func (s *AuthHandlerSuite) TestUpdateNote_SessionOwner_BypassesEditCode() {
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-owner").Return(&domain.User{ID: testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)
	s.manager.EXPECT().Update(gomock.Any(), testID, "", testUserID1, gomock.Any()).
		Return(newTestNote("t", "c"), nil)

	body := `{"title":"t","content":"c"}`
	req := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.sessionCookie("sess-owner"))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
}

// TestUpdateNote_APITokenOwner_BypassesEditCode is the CLI/API-client counterpart: a Bearer API
// token resolves to its owning user the same way a session does.
func (s *AuthHandlerSuite) TestUpdateNote_APITokenOwner_BypassesEditCode() {
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "valid-key").Return(&domain.User{ID: testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, "token-hash", nil)
	s.manager.EXPECT().Update(gomock.Any(), testID, "", testUserID1, gomock.Any()).
		Return(newTestNote("t", "c"), nil)

	body := `{"title":"t","content":"c"}`
	req := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer valid-key")

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
}

// TestDeleteNote_SessionOwner_BypassesEditCode mirrors TestUpdateNote_SessionOwner_BypassesEditCode
// for DELETE.
func (s *AuthHandlerSuite) TestDeleteNote_SessionOwner_BypassesEditCode() {
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-owner").Return(&domain.User{ID: testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)
	s.manager.EXPECT().Delete(gomock.Any(), testID, "", testUserID1).Return(nil)

	req := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	req.AddCookie(s.sessionCookie("sess-owner"))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusNoContent, rec.Code)
}

// TestUpdateNote_SessionNonOwner_StillNeedsEditCode verifies the owner bypass never extends to
// another signed-in user: without the correct edit_code, a non-owner session gets the same
// domain.ErrInvalidEditCode → 403 as an anonymous caller.
func (s *AuthHandlerSuite) TestUpdateNote_SessionNonOwner_StillNeedsEditCode() {
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-other").Return(&domain.User{ID: testUserID2,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)
	s.manager.EXPECT().Update(gomock.Any(), testID, "", testUserID2, gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode)

	body := `{"title":"t","content":"c"}`
	req := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.sessionCookie("sess-other"))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

// TestUpdateNoteByPath_SessionOwner_BypassesEditCode is the native multi-segment-slug
// counterpart of TestUpdateNote_SessionOwner_BypassesEditCode (PUT /notes/{id...}).
func (s *AuthHandlerSuite) TestUpdateNoteByPath_SessionOwner_BypassesEditCode() {
	const mid = "project/GUIDE.md"

	s.auth.EXPECT().GetSession(gomock.Any(), "sess-owner").Return(&domain.User{ID: testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)
	s.manager.EXPECT().Update(gomock.Any(), mid, "", testUserID1, gomock.Any()).
		Return(newTestNote("Guide", "new body"), nil)

	body := `{"title":"Guide","content":"new body"}`
	req := httptest.NewRequest(http.MethodPut, "/notes/"+mid, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.sessionCookie("sess-owner"))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
}

func (s *AuthHandlerSuite) TestChangePasswordRevocationWarningKeepsReplacementSession() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "OldP@ss12!", "NewP@ss12!", "123456").
		Return("replacement-session", domain.ErrSessionRevocation)

	form := withCSRFFor(url.Values{
		"old_password": {"OldP@ss12!"}, "new_password": {"NewP@ss12!"},
		"new_password_confirm": {"NewP@ss12!"}, "totp_code": {"123456"},
	}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Password changed, but older sessions or API tokens could not be revoked")
	s.NotContains(rec.Body.String(), "Password changed successfully")

	found := false

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "padmark_session" {
			found = true

			s.Equal("replacement-session", cookie.Value)
		}
	}

	s.True(found)
}

// renderedCSRFField extracts the hidden csrf_token value from a rendered HTML form.
func (s *AuthHandlerSuite) renderedCSRFField(body string) string {
	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(body)
	s.Require().Len(match, 2, "rendered page must carry a csrf_token field")

	return match[1]
}

// TestChangePasswordHandler_RerenderCarriesRotatedCSRFToken is the regression for the page
// re-rendering the form with the pre-rotation CSRF token: a full or partial (revocation
// warning) password change rotates the CSRF cookie, so the re-rendered form must embed that
// new, verifiable token — otherwise resubmitting from the page fails the double-submit check.
func (s *AuthHandlerSuite) TestChangePasswordHandler_RerenderCarriesRotatedCSRFToken() {
	for _, changeErr := range []error{nil, domain.ErrSessionRevocation} {
		s.Run(fmt.Sprintf("err=%v", changeErr), func() {
			usr := &domain.User{ID: testUserID1, Username: "alice",
				CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
				KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
			}
			s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil)
			s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "OldP@ss12!", "NewP@ss12!", "123456").
				Return("new-sess-id", changeErr)

			form := withCSRFFor(url.Values{
				"old_password": {"OldP@ss12!"}, "new_password": {"NewP@ss12!"},
				"new_password_confirm": {"NewP@ss12!"}, "totp_code": {"123456"},
			}, "sess1")
			req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(s.sessionCookie("sess1"))
			req.AddCookie(s.csrfCookieFor("sess1"))

			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			s.Require().Equal(http.StatusOK, rec.Code)

			var csrfCookie *http.Cookie

			for _, cookie := range rec.Result().Cookies() {
				if cookie.Name == "padmark_csrf" {
					csrfCookie = cookie
				}
			}

			s.Require().NotNil(csrfCookie, "password change must rotate the CSRF cookie")
			s.NotEqual(csrfTokenFor("sess1"), csrfCookie.Value)

			field := s.renderedCSRFField(rec.Body.String())
			s.Equal(csrfCookie.Value, field, "re-rendered form must carry the rotated token")
			s.True(adhttp.VerifyCSRFTokenForTest(field, authTestCSRFSecret, "new-sess-id"),
				"the rotated token must be bound to the new session")
		})
	}
}

// TestChangePasswordHandler_FailureKeepsRequestCSRFToken — a rejected change does not rotate,
// so the re-rendered form keeps the request's (still valid) token and sets no new cookie.
func (s *AuthHandlerSuite) TestChangePasswordHandler_FailureKeepsRequestCSRFToken() {
	usr := &domain.User{ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "WrongP@ss12!", "NewP@ss12!", "123456").
		Return("", domain.ErrInvalidPassword)

	form := withCSRFFor(url.Values{
		"old_password": {"WrongP@ss12!"}, "new_password": {"NewP@ss12!"},
		"new_password_confirm": {"NewP@ss12!"}, "totp_code": {"123456"},
	}, "sess1")
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookieFor("sess1"))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	s.Require().Equal(http.StatusOK, rec.Code)

	for _, cookie := range rec.Result().Cookies() {
		s.NotEqual("padmark_csrf", cookie.Name, "a failed change must not rotate the CSRF cookie")
	}

	s.Equal(csrfTokenFor("sess1"), s.renderedCSRFField(rec.Body.String()))
}

// TestSetupHandler_InvalidUsername_ShowsValidationMessage — the HTML branch renders the
// username rule from domain.ErrInvalidUsername on the form. The usecase rejects the name before
// any storage access (pinned in usecases/auth TestOnboardingInvalidUsernameNeverTouchesStorage);
// the strict mock pins that the handler makes no other AuthManager call after the rejection.
func (s *AuthHandlerSuite) TestSetupHandler_InvalidUsername_ShowsValidationMessage() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "tok", "bad/name", testPw).
		Return(nil, fmt.Errorf("accept invite: %w", domain.ErrInvalidUsername))

	form := withCSRF(url.Values{
		"invite":           {"tok"},
		"username":         {"bad/name"},
		"password":         {testPw},
		"password_confirm": {testPw},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "username must be 1–64 characters")
	s.NotContains(rec.Body.String(), "Scan QR code")
}
