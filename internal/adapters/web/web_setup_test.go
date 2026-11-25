package web_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// ── Setup page ──.

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

// ── Setup handler ──.

const testPw = "ValidP@ss12!"

func (s *AuthHandlerSuite) TestSetupHandler_WithInvite_ShowsQR() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "good-tok", "newuser", gomock.Any()).
		Return(&auth.Enrollment{
			QRCode:  "data:image/png;base64,abc123",
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
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil) // bootstrap allowed: empty DB.
	s.auth.EXPECT().AcceptFirstAdmin(gomock.Any(), "root", gomock.Any()).
		Return(&auth.Enrollment{
			QRCode:  "data:image/png;base64,xyz",
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

func (s *AuthHandlerSuite) TestSetupHandler_PasswordMismatch_DoesNotCallAuthMgr() {
	// The account system must NOT be called when passwords don't match — verify via strict mock.
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
