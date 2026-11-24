package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestTOTPLoginHandler_AccountsDisabled_Returns404: the account system is opt-in. Without
// HandlerConfig.Accounts, /totp-login must answer 404.
func (s *HandlerSuite) TestTOTPLoginHandler_AccountsDisabled_Returns404() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg) // Accounts is nil → accounts disabled.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/totp-login", nil)

	handler.TOTPLoginHandler(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// TestChangePasswordPage_AccountsDisabled_Returns404 is the test above for the GET
// /change-password page.
func (s *HandlerSuite) TestChangePasswordPage_AccountsDisabled_Returns404() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg) // Accounts is nil → accounts disabled.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/change-password", nil)

	handler.ChangePasswordPage(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// ── Auth middleware ──.

func (s *HandlerSuite) TestAuth_MissingToken_Browser() {
	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"should redirect to /login with ?next= param")
}

// TestAuth_InvalidToken_JSONError verifies a non-browser 401 (invalid bearer token) is a JSON
// body matching the same {"message": ...} shape as every other API error, not http.Error's
// plain text — the generated ogen client (used by the CLI) decodes API errors as JSON, so a
// plain-text body would surface as an opaque decode failure instead of a clean "unauthorized".
// /admin is a named route, so it is gated by the middleware itself, before any handler runs.
func (s *HandlerSuite) TestAuth_InvalidToken_JSONError() {
	router := s.newRouterWithTOTPAuth()
	s.authMgr.EXPECT().ResolveAPIToken(gomock.Any(), "bogus-token").Return(nil, "", domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.Header.Set("Authorization", "Bearer bogus-token")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
	s.Equal("application/json", w.Header().Get("Content-Type"))
	s.JSONEq(`{"message":"unauthorized"}`, w.Body.String())
}

func (s *HandlerSuite) TestAuth_PublicPaths() {
	router := s.newRouterWithTOTPAuth()

	for _, path := range []string{"/login", "/static/style.css", "/healthz"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)

		router.ServeHTTP(w, r)

		s.NotEqual(http.StatusUnauthorized, w.Code, "path %s should be public", path)
		s.NotEqual(http.StatusSeeOther, w.Code, "path %s should not redirect", path)
	}
}

func (s *HandlerSuite) TestAuth_PublicNoteView() {
	note := newTestNote("public", "body")
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

// TestAuth_Create_AnonymousAllowed verifies that with auth configured, an anonymous caller can
// still create a note: POST /notes is admitted by isPublicRoute, and the usecase itself rejects
// an owner-only privacy requested by an anonymous caller (tested via ValidateOwnership, not
// middleware).
func (s *HandlerSuite) TestAuth_Create_AnonymousAllowed() {
	router := s.newRouterWithTOTPAuth()
	note := newTestNote("t", "c")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

// TestAuth_Update_Anonymous_PublicNote_WrongEditCode verifies the restored edit_code workflow:
// with auth configured, an anonymous caller hitting PUT on a public note passes the middleware.
// The usecase admits an anonymous reader for a public note, and its edit-code verification
// decides the outcome — a wrong code yields 403, not the middleware's 401.
func (s *HandlerSuite) TestAuth_Update_Anonymous_PublicNote_WrongEditCode() {
	router := s.newRouterWithTOTPAuth()
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "x", noCaller(), gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode)

	w := httptest.NewRecorder()
	body := `{"title":"t","content":"c","edit_code":"x"}`
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestAuth_Delete_Anonymous_PublicNote_WrongEditCode is the DELETE counterpart of
// TestAuth_Update_Anonymous_PublicNote_WrongEditCode: a wrong edit code is a usecase-level 403,
// never a middleware-level 401.
func (s *HandlerSuite) TestAuth_Delete_Anonymous_PublicNote_WrongEditCode() {
	router := s.newRouterWithTOTPAuth()
	s.manager.EXPECT().
		Delete(gomock.Any(), testID, "wrong", noCaller()).
		Return(domain.ErrInvalidEditCode)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "wrong")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

func (s *HandlerSuite) TestGetNote_OpenInstance_ShowsEditButtons() {
	// No auth tokens configured → CanEdit is always true.
	note := newTestNote("public note", "content")
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(note, "<p>content</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, `href="/edit/`)
	s.Contains(body, "New")
}

func (s *HandlerSuite) TestGetNote_AuthInstance_Unauthenticated_ShowsEditButtons() {
	// Accounts enabled, user not authenticated → CanEdit is true: the edit code authorizes the save.
	// The handler reads the public note with ReadRendered and the anonymous reader.
	router := s.newRouterWithTOTPAuth()
	note := newTestNote("public note", "content")

	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(note, "<p>content</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, `href="/edit/`+testID+`"`,
		"an unauthenticated reader of a public note must see the edit button in TOTP mode")
	s.Contains(body, ">New note<")
}

// ── Login ──.

func (s *HandlerSuite) TestLoginPage() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `<h1 class="login-title">Sign in</h1>`)
}

func (s *HandlerSuite) TestLoginPage_Error() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login?error=1", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "That didn't work.")
}

func (s *HandlerSuite) TestLoginPage_WriteFail() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg)
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/login?error=1", nil)

	handler.LoginPage(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestLoginPage_WithNext() {
	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login?next=%2Fnotes%2Fabc", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `name="next"`)
	s.Contains(w.Body.String(), `/notes/abc`)
}

func (s *HandlerSuite) TestAuth_MissingToken_NextContainsOriginalURL() {
	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	// /change-password stays a protected page; /success is public (anonymous creates).
	r := httptest.NewRequest(http.MethodGet, "/change-password?foo=bar", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	loc := w.Header().Get("Location")
	s.True(strings.HasPrefix(loc, "/login?next="), "must redirect to login with next")
	s.Contains(loc, "%2Fchange-password")
}

func (s *HandlerSuite) TestAuth_APIClient_NoHeaders_Returns401() {
	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	// No Accept, no Sec-Fetch-Dest — typical API client (curl).
	r := httptest.NewRequest(http.MethodGet, "/change-password", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestAuth_Browser_SecFetchDestDocument_Returns303() {
	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/change-password", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login"))
}
