package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"go.uber.org/mock/gomock"
)

// These tests cover security finding M-1: --public-scheme https must drive the cookie Secure
// attribute and HSTS, not only absolute-link building. The deployment under test is the one the
// flag help recommends — a TLS-terminating cloud LB or k8s ingress whose IP is not stable enough
// for --trusted-proxies — so every request below arrives as plain HTTP with no X-Forwarded-Proto.

// cookieByName returns the last Set-Cookie with the given name in the recorded response, or nil.
func cookieByName(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	var found *http.Cookie

	for _, ck := range rec.Result().Cookies() {
		if ck.Name == name {
			found = ck
		}
	}

	return found
}

// loginRequest builds a successful TOTP login POST (the mock expectation is registered by the
// caller) and returns the recorded response.
func (s *AuthHandlerSuite) loginRequest(router http.Handler) *httptest.ResponseRecorder {
	s.T().Helper()

	form := withCSRF(url.Values{"username": {"alice"}, "password": {"ValidP@ss12!"}, "code": {"123456"}})
	req := httptest.NewRequest(http.MethodPost, "/totp-login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(s.csrfCookie())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusSeeOther, rec.Code)

	return rec
}

func (s *AuthHandlerSuite) TestTOTPLogin_ForcedHTTPS_SessionCookieIsSecure() {
	s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any(), gomock.Any()).
		Return("sess-id-abc", nil)

	router := s.newRouterWithAuthAndScheme("https")
	rec := s.loginRequest(router)

	session := cookieByName(rec, "padmark_session")
	s.Require().NotNil(session, "padmark_session cookie must be set")
	s.True(session.Secure, "session cookie must be Secure under --public-scheme https")

	csrf := cookieByName(rec, "padmark_csrf")
	s.Require().NotNil(csrf, "padmark_csrf cookie must be rotated on login")
	s.True(csrf.Secure, "rotated CSRF cookie must be Secure under --public-scheme https")
}

func (s *AuthHandlerSuite) TestCSRFCookie_ForcedHTTPS_IsSecure() {
	router := s.newRouterWithAuthAndScheme("https")

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusOK, rec.Code)

	csrf := cookieByName(rec, "padmark_csrf")
	s.Require().NotNil(csrf, "padmark_csrf cookie must be issued on first GET")
	s.True(csrf.Secure, "CSRF cookie must be Secure under --public-scheme https")
}

func (s *AuthHandlerSuite) TestSecurityHeaders_ForcedHTTPS_SendsHSTS() {
	router := s.newRouterWithAuthAndScheme("https")

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusOK, rec.Code)
	s.NotEmpty(rec.Header().Get("Strict-Transport-Security"),
		"HSTS must be sent under --public-scheme https")
}

// TestForcedScheme_Unset_PlainHTTPStaysInsecure pins the existing behaviour: without the
// override (and without TLS or a trusted proxy) cookies stay non-Secure and HSTS is not sent,
// so plain-HTTP development setups keep working.
func (s *AuthHandlerSuite) TestForcedScheme_Unset_PlainHTTPStaysInsecure() {
	for _, scheme := range []string{"", "http"} {
		s.Run("scheme="+scheme, func() {
			s.auth.EXPECT().Login(gomock.Any(), "alice", gomock.Any(), "123456", gomock.Any(), gomock.Any(), gomock.Any()).
				Return("sess-id-abc", nil)

			router := s.newRouterWithAuthAndScheme(scheme)
			rec := s.loginRequest(router)

			session := cookieByName(rec, "padmark_session")
			s.Require().NotNil(session)
			s.False(session.Secure, "session cookie must not be Secure over plain HTTP")

			csrf := cookieByName(rec, "padmark_csrf")
			s.Require().NotNil(csrf)
			s.False(csrf.Secure, "CSRF cookie must not be Secure over plain HTTP")

			s.Empty(rec.Header().Get("Strict-Transport-Security"), "HSTS must not be sent over plain HTTP")
		})
	}
}

func (s *AuthHandlerSuite) TestLogout_ForcedHTTPS_ClearedCookiesAreSecure() {
	s.auth.EXPECT().Logout(gomock.Any(), "sess-xyz").Return(nil)

	router := s.newRouterWithAuthAndScheme("https")

	form := withCSRFFor(url.Values{}, "sess-xyz")
	req := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.sessionCookie("sess-xyz"))
	req.AddCookie(s.csrfCookieFor("sess-xyz"))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusSeeOther, rec.Code)

	session := cookieByName(rec, "padmark_session")
	s.Require().NotNil(session, "session cookie must be cleared on logout")
	s.LessOrEqual(session.MaxAge, 0)
	s.True(session.Secure, "cleared session cookie must be Secure under --public-scheme https")

	csrf := cookieByName(rec, "padmark_csrf")
	s.Require().NotNil(csrf, "CSRF cookie must be cleared on logout")
	s.LessOrEqual(csrf.MaxAge, 0)
	s.True(csrf.Secure, "cleared CSRF cookie must be Secure under --public-scheme https")
}

func (s *AuthHandlerSuite) TestChangePassword_ForcedHTTPS_RotatedCookiesAreSecure() {
	usr := s.adminUser()
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil).Times(1)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "OldP@ss12!", "NewP@ss12!", "123456").
		Return("new-sess-id", nil)

	router := s.newRouterWithAuthAndScheme("https")

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
	router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusOK, rec.Code)

	session := cookieByName(rec, "padmark_session")
	s.Require().NotNil(session, "session cookie must be rotated after password change")
	s.Equal("new-sess-id", session.Value)
	s.True(session.Secure, "rotated session cookie must be Secure under --public-scheme https")

	csrf := cookieByName(rec, "padmark_csrf")
	s.Require().NotNil(csrf, "CSRF cookie must be rotated after password change")
	s.True(csrf.Secure, "rotated CSRF cookie must be Secure under --public-scheme https")
}
