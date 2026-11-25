package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
)

// ── Change password ──.

func (s *AuthHandlerSuite) TestChangePasswordPage_Authenticated_RendersForm() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
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

func (s *AuthHandlerSuite) TestChangePasswordHandler_Mismatch_ShowsError() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
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

func (s *AuthHandlerSuite) TestChangePasswordHandler_InvalidTOTP_ShowsError() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
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

// TestChangePasswordHandler_StoreFailure_AsksRetry: a failed change writes nothing (Finding 5),
// so the page asks for a retry, keeps the current session cookie and tells nobody to call an
// administrator.
func (s *AuthHandlerSuite) TestChangePasswordHandler_StoreFailure_AsksRetry() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "OldP@ss12!", "NewP@ss12!", "123456").
		Return("", errors.New("change password: database is locked"))

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

	body := rec.Body.String()
	s.Contains(body, "Something went wrong. Please try again.")
	s.NotContains(body, "Password changed")
	s.NotContains(body, "administrator")
	s.NotContains(body, "database is locked", "internal errors must not reach the page")

	for _, cookie := range rec.Result().Cookies() {
		s.NotEqual("padmark_session", cookie.Name, "a failed change must keep the current session cookie")
	}
}

// TestChangePasswordHandler_Success_ShowsMessageAndRotatesSessionAndCSRF: a password change shows
// the success message and sets a new session cookie, so old stolen cookies stop working. It also
// rotates the CSRF cookie, and the re-rendered form embeds that new token, bound to the new
// session. This is the regression for a form with the old token: a resubmit from the page would
// fail the double-submit check.
func (s *AuthHandlerSuite) TestChangePasswordHandler_Success_ShowsMessageAndRotatesSessionAndCSRF() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess1").Return(usr, nil)
	s.auth.EXPECT().ChangePassword(gomock.Any(), "sess1", "OldP@ss12!", "NewP@ss12!", "123456").
		Return("new-sess-id", nil)

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
	s.Contains(rec.Body.String(), "Password changed successfully")

	var sessionCookie, csrfCookie *http.Cookie

	for _, cookie := range rec.Result().Cookies() {
		switch cookie.Name {
		case "padmark_session":
			sessionCookie = cookie
		case "padmark_csrf":
			csrfCookie = cookie
		}
	}

	s.Require().NotNil(sessionCookie, "session cookie must be rotated after password change")
	s.Equal("new-sess-id", sessionCookie.Value)
	s.Require().NotNil(csrfCookie, "password change must rotate the CSRF cookie")
	s.NotEqual(csrfTokenFor("sess1"), csrfCookie.Value)

	field := s.renderedCSRFField(rec.Body.String())
	s.Equal(csrfCookie.Value, field, "re-rendered form must carry the rotated token")
	s.True(web.VerifyCSRFTokenForTest(field, authTestCSRFSecret, "new-sess-id"),
		"the rotated token must be bound to the new session")
}

// TestChangePasswordHandler_WrongOldPassword_ShowsErrorKeepsCSRFToken — a wrong old password
// shows an error. A rejected change does not rotate, so the re-rendered form keeps the request's
// (still valid) token and sets no new cookie.
func (s *AuthHandlerSuite) TestChangePasswordHandler_WrongOldPassword_ShowsErrorKeepsCSRFToken() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
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
	s.Contains(rec.Body.String(), "incorrect")

	for _, cookie := range rec.Result().Cookies() {
		s.NotEqual("padmark_csrf", cookie.Name, "a failed change must not rotate the CSRF cookie")
	}

	s.Equal(csrfTokenFor("sess1"), s.renderedCSRFField(rec.Body.String()))
}
