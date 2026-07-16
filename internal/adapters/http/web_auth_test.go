package http_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

type AuthHandlerSuite struct {
	suite.Suite

	ctrl    *gomock.Controller
	manager *MockNoteManager
	pinger  *MockPinger
	auth    *MockAuthManager
	router  http.Handler
}

func TestAuthHandler(t *testing.T) {
	suite.Run(t, new(AuthHandlerSuite))
}

func (s *AuthHandlerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)
	s.auth = NewMockAuthManager(s.ctrl)
	s.router = s.newRouterWithAuth()
}

func (s *AuthHandlerSuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *AuthHandlerSuite) newRouterWithAuth() http.Handler {
	handler := adhttp.NewHandler(s.manager, discardLog, nil).
		WithAuthManager(s.auth)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   authTestCSRFSecret,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithAuthAndScheme is newRouterWithAuth with --public-scheme forced, used to verify
// that invite links and API-token envelopes honour the override behind a TLS-terminating proxy.
func (s *AuthHandlerSuite) newRouterWithAuthAndScheme(forcedScheme string) http.Handler {
	handler := adhttp.NewHandler(s.manager, discardLog, nil).
		WithAuthManager(s.auth)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   authTestCSRFSecret,
		ForcedScheme: forcedScheme,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithAuthAndSessionTTL is newRouterWithAuth with a custom SessionTTL, used to verify
// the TOTP session cookie's Max-Age tracks the configured --session-ttl instead of a fixed value.
func (s *AuthHandlerSuite) newRouterWithAuthAndSessionTTL(ttl time.Duration) http.Handler {
	handler := adhttp.NewHandler(s.manager, discardLog, nil).
		WithAuthManager(s.auth)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   authTestCSRFSecret,
		SessionTTL:   ttl,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

func (s *AuthHandlerSuite) adminUser() *domain.User {
	return &domain.User{ID: "admin-id", Username: "admin", IsAdmin: true}
}

// authTestCSRFSecret is a fixed 32-byte secret for auth handler tests.
// Must match the CSRFSecret set in newRouterWithAuth's RouterOptions.
var authTestCSRFSecret = []byte("padmark-test-csrf-secret-32bytes") //nolint:gochecknoglobals // test fixture

// testCSRFToken is a valid HMAC-signed CSRF token generated from authTestCSRFSecret.
// Generated once at test-binary init; all auth tests share it via csrfCookie/withCSRF.
var testCSRFToken = adhttp.GenerateCSRFTokenForTest(authTestCSRFSecret) //nolint:gochecknoglobals // test fixture

func (s *AuthHandlerSuite) sessionCookie(value string) *http.Cookie {
	return &http.Cookie{Name: "padmark_session", Value: value} //nolint:gosec // G124: test cookie
}

func (s *AuthHandlerSuite) csrfCookie() *http.Cookie {
	return &http.Cookie{Name: "padmark_csrf", Value: testCSRFToken} //nolint:gosec // G124: test cookie
}

func withCSRF(form url.Values) url.Values {
	form.Set("csrf_token", testCSRFToken)

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
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-ok").Return(&domain.User{ID: "u1"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(s.sessionCookie("sess-ok"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/", rec.Header().Get("Location"))
}

// TestLoginPage_AlreadyAuthenticated_RedirectsToNext honours a safe ?next= target.
func (s *AuthHandlerSuite) TestLoginPage_AlreadyAuthenticated_RedirectsToNext() {
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-ok").Return(&domain.User{ID: "u1"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/login?next=/edit/abc", nil)
	req.AddCookie(s.sessionCookie("sess-ok"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/edit/abc", rec.Header().Get("Location"))
}

// ── TOTP login ──

func (s *AuthHandlerSuite) TestTOTPLogin_Success_SetsCookieAndRedirects() {
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any()).
		Return("sess-id-abc", nil)

	form := withCSRF(url.Values{"username": {"alice"}, "password": {"ValidP@ss12!"}, "code": {"123456"}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "000000", gomock.Any(), gomock.Any()).
		Return("", domain.ErrInvalidTOTP)

	form := withCSRF(url.Values{"username": {"alice"}, "password": {"ValidP@ss12!"}, "code": {"000000"}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Contains(rec.Header().Get("Location"), "/login?error=1")
}

func (s *AuthHandlerSuite) TestTOTPLogin_WithNext_RedirectsToNext() {
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any()).
		Return("sess-id", nil)

	next := url.QueryEscape("/admin")
	form := withCSRF(url.Values{
		"username": {"alice"}, "password": {"ValidP@ss12!"}, "code": {"123456"}, "next": {"/admin"},
	})
	req := httptest.NewRequest(http.MethodPost, "/totp-login?next="+next, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess-xyz"))
	req.AddCookie(s.csrfCookie())

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

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess-xyz"))
	req.AddCookie(s.csrfCookie())

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
		Return("data:image/png;base64,abc123", nil)

	form := withCSRF(url.Values{
		"invite":           {"good-tok"},
		"username":         {"newuser"},
		"password":         {testPw},
		"password_confirm": {testPw},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "data:image/png;base64,")
}

func (s *AuthHandlerSuite) TestSetupHandler_FirstAdmin_ShowsQR() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil) // bootstrap allowed: empty DB
	s.auth.EXPECT().AcceptFirstAdmin(gomock.Any(), "root", gomock.Any()).
		Return("data:image/png;base64,xyz", nil)

	form := withCSRF(url.Values{
		"username":         {"root"},
		"password":         {testPw},
		"password_confirm": {testPw},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "do not match")
}

func (s *AuthHandlerSuite) TestSetupHandler_ExpiredInvite_ShowsError() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "expired", "user", gomock.Any()).
		Return("", domain.ErrInviteExpired)

	form := withCSRF(url.Values{
		"invite":           {"expired"},
		"username":         {"user"},
		"password":         {testPw},
		"password_confirm": {testPw},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "expired")
}

func (s *AuthHandlerSuite) TestSetupHandler_DuplicateUsername_ShowsError() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "tok", "taken", gomock.Any()).
		Return("", domain.ErrUserExists)

	form := withCSRF(url.Values{
		"invite":           {"tok"},
		"username":         {"taken"},
		"password":         {testPw},
		"password_confirm": {testPw},
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	nonAdmin := &domain.User{ID: "u1", Username: "alice", IsAdmin: false}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(s.sessionCookie("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminPage_Admin_ListsUsers() {
	usr := s.adminUser()
	users := []*domain.User{usr, {ID: "u2", Username: "bob"}}

	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return(users, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), "admin-id").Return([]*auth.APITokenInfo{}, nil)

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
		{ID: "abcdef0123456789deadbeef", UserID: "admin-id", Username: "admin", CreatedAt: time.Now()},
	}

	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), "admin-id").Return(tokens, nil)

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
	s.auth.EXPECT().GenerateInvite(gomock.Any(), "admin-id").Return("tok-xyz", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), "admin-id").Return([]*auth.APITokenInfo{}, nil)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "tok-xyz")
}

func (s *AuthHandlerSuite) TestAdminInvite_ForcedSchemeEmbedsHTTPS() {
	// Behind a TLS-terminating proxy the request arrives as plain HTTP, but --public-scheme=https
	// is forced, so the invite link the admin copies must use https regardless — otherwise the
	// invitee is sent to an http:// setup URL that the proxy may not even serve.
	s.router = s.newRouterWithAuthAndScheme("https")

	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), "admin-id").Return("tok-xyz", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), "admin-id").Return([]*auth.APITokenInfo{}, nil)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

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
	s.auth.EXPECT().GenerateInvite(gomock.Any(), "admin-id").Return("", errors.New("boom"))
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), "admin-id").Return([]*auth.APITokenInfo{}, nil)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Failed to generate invite link")
}

func (s *AuthHandlerSuite) TestAdminInvite_AdminDataFails_Returns500() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return(nil, errors.New("boom"))

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusInternalServerError, rec.Code)
}

// ── Admin revoke ──

func (s *AuthHandlerSuite) TestAdminRevoke_RevokesAndRedirects() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeUser(gomock.Any(), "admin-id", "target-id").Return(nil)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/users/target-id/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/admin", rec.Header().Get("Location"))
}

func (s *AuthHandlerSuite) TestAdminRevoke_NonAdmin_Returns403() {
	nonAdmin := &domain.User{ID: "u1", Username: "alice", IsAdmin: false}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/users/target-id/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminRevoke_Forbidden_RedirectsWithSpecificMessage() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeUser(gomock.Any(), "admin-id", "target-id").Return(domain.ErrForbidden)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/users/target-id/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

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
	s.auth.EXPECT().RevokeUser(gomock.Any(), "admin-id", "target-id").Return(errors.New("boom"))

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/users/target-id/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	loc := rec.Header().Get("Location")
	s.Contains(loc, "revoke_error=")
	s.Contains(loc, url.QueryEscape("Failed to revoke user."))
}

// ── Admin API keys ──

func (s *AuthHandlerSuite) TestAdminCreateKey_CreatesAndShowsKey() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), "admin-id").Return("plain-secret-key", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), "admin-id").Return([]*auth.APITokenInfo{}, nil)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

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
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), "admin-id").Return("plain-secret-key", nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), "admin-id").Return([]*auth.APITokenInfo{}, nil)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

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
	nonAdmin := &domain.User{ID: "u1", Username: "alice", IsAdmin: false}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminRevokeKey_RevokesAndRedirects() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeAPIToken(gomock.Any(), "admin-id", "hash-123").Return(nil)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/hash-123/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/admin", rec.Header().Get("Location"))
}

func (s *AuthHandlerSuite) TestAdminRevokeKey_NonAdmin_Returns403() {
	nonAdmin := &domain.User{ID: "u1", Username: "alice", IsAdmin: false}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(nonAdmin, nil).Times(1)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/hash-123/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *AuthHandlerSuite) TestAdminCreateKey_CreateFails_ShowsError() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), "admin-id").Return("", domain.ErrFeatureNotSupported)
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return([]*domain.User{usr}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), "admin-id").Return([]*auth.APITokenInfo{}, nil)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Failed to create API key.", "create failure renders an inline error")
}

func (s *AuthHandlerSuite) TestAdminRevokeKey_RevokeFails_RedirectsWithError() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "admin-sess").Return(usr, nil).Times(1)
	s.auth.EXPECT().RevokeAPIToken(gomock.Any(), "admin-id", "hash-123").Return(domain.ErrNotFound)

	form := withCSRF(url.Values{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/hash-123/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("admin-sess"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Contains(rec.Header().Get("Location"), "key_error")
}

// ── Session auth middleware ──

func (s *AuthHandlerSuite) TestSessionAuth_ValidSession_PassesThrough() {
	usr := &domain.User{ID: "u1", Username: "alice"}
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
	usr := &domain.User{ID: "u1", Username: "alice"}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer good-api-key")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
}

func (s *AuthHandlerSuite) TestAPITokenAuth_UnknownBearer_Returns401() {
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "bad-key").Return(nil, domain.ErrNotFound)

	// No Sec-Fetch-Dest / text-html Accept ⇒ treated as an API client ⇒ 401, not a redirect.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bad-key")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusUnauthorized, rec.Code)
}

// TestAPITokenAuth_AdminBearer_ReachesAdminPage proves the user resolved from the API token is
// injected into the request context: an admin key reaches the admin-only page and renders it.
func (s *AuthHandlerSuite) TestAPITokenAuth_AdminBearer_ReachesAdminPage() {
	admin := s.adminUser()
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "admin-key").Return(admin, nil)
	s.auth.EXPECT().ListUsers(gomock.Any(), "admin-id").Return([]*domain.User{admin}, nil)
	s.auth.EXPECT().ListAPITokens(gomock.Any(), "admin-id").Return([]*auth.APITokenInfo{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Authorization", "Bearer admin-key")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
}

// TestAPITokenAuth_PrivateNote_ValidBearer_Served is a regression test: GET /notes/{id} is a
// "public" route (auth not required), so the middleware must still resolve an API-token Bearer
// key there — otherwise the CLI, whose only credential is that key, gets 401 on a private note.
func (s *AuthHandlerSuite) TestAPITokenAuth_PrivateNote_ValidBearer_Served() {
	usr := &domain.User{ID: "u1", Username: "alice"}
	priv := true
	note := newTestNote("secret", "# private")
	note.Private = &priv

	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, nil)
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
	priv := true
	note := newTestNote("secret", "# private")
	note.Private = &priv

	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "bad-key").Return(nil, domain.ErrNotFound)
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
	usr := &domain.User{ID: "u1", Username: "alice"}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, nil)
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
	usr := &domain.User{ID: "u1", Username: "alice"}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)

	req := httptest.NewRequest(http.MethodGet, "/change-password", nil)
	req.AddCookie(s.sessionCookie("sess1"))

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "old_password")
}

func (s *AuthHandlerSuite) TestChangePasswordHandler_Success_ShowsSuccessMessage() {
	usr := &domain.User{ID: "u1", Username: "alice"}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "OldP@ss12!", "NewP@ss12!", "123456").Return("new-sess-id", nil)

	form := withCSRF(url.Values{
		"old_password":         {"OldP@ss12!"},
		"new_password":         {"NewP@ss12!"},
		"new_password_confirm": {"NewP@ss12!"},
		"totp_code":            {"123456"},
	})
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookie())

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
	usr := &domain.User{ID: "u1", Username: "alice"}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)

	form := withCSRF(url.Values{
		"old_password":         {"OldP@ss12!"},
		"new_password":         {"NewP@ss12!"},
		"new_password_confirm": {"DifferentP@ss12!"},
	})
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "do not match")
}

func (s *AuthHandlerSuite) TestChangePasswordHandler_WrongOldPassword_ShowsError() {
	usr := &domain.User{ID: "u1", Username: "alice"}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "WrongP@ss12!", "NewP@ss12!", "123456").
		Return("", domain.ErrInvalidPassword)

	form := withCSRF(url.Values{
		"old_password":         {"WrongP@ss12!"},
		"new_password":         {"NewP@ss12!"},
		"new_password_confirm": {"NewP@ss12!"},
		"totp_code":            {"123456"},
	})
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "incorrect")
}

func (s *AuthHandlerSuite) TestChangePasswordHandler_InvalidTOTP_ShowsError() {
	usr := &domain.User{ID: "u1", Username: "alice"}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "OldP@ss12!", "NewP@ss12!", "badcode").
		Return("", domain.ErrInvalidTOTP)

	form := withCSRF(url.Values{
		"old_password":         {"OldP@ss12!"},
		"new_password":         {"NewP@ss12!"},
		"new_password_confirm": {"NewP@ss12!"},
		"totp_code":            {"badcode"},
	})
	req := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess1"))
	req.AddCookie(s.csrfCookie())

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
	req.AddCookie(s.csrfCookie()) // cookie = testCSRFToken, field = "wrong-token"

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

// TestCSRFGuard_InvalidHMAC_Returns403 covers the branch where cookie and form field match
// (so the equality check passes) but the token's HMAC signature is invalid — e.g. a token
// minted with a different secret. The guard must still reject it via verifyCSRFToken.
func (s *AuthHandlerSuite) TestCSRFGuard_InvalidHMAC_Returns403() {
	forgedSecret := []byte("a-different-csrf-secret-32bytes!!")
	forged := adhttp.GenerateCSRFTokenForTest(forgedSecret)

	form := url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}, "csrf_token": {forged}}
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Cookie == field (equality check passes) but the HMAC was signed with the wrong secret.
	req.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: forged}) //nolint:gosec // G124: test cookie

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

// ── Session expiry ──

func (s *AuthHandlerSuite) TestTOTPLogin_SessionCookieMaxAge_Is30Days() {
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any()).
		Return("sess-id", nil)

	form := withCSRF(url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any()).
		Return("sess-id", nil)

	form := withCSRF(url.Values{"username": {"alice"}, "password": {testPw}, "code": {"123456"}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
