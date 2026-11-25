package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// ── Session auth middleware ──.

func (s *AuthHandlerSuite) TestSessionAuth_ValidSession_PassesThrough() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	// GET / (IndexPage) is always served; only the auth middleware calls GetSession.
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

// ── API-token auth middleware ──.

func (s *AuthHandlerSuite) TestAPITokenAuth_ValidBearer_PassesThrough() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
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
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	priv := domain.PrivacyAuthenticated
	note := newTestNote("secret", "# private")
	note.Privacy = &priv

	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)
	// The usecase gets the account of the API token as the reader.
	s.manager.EXPECT().Read(gomock.Any(), testID, signedInReader(usr.ID)).Return(note, nil)

	req := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	req.Header.Set("Authorization", "Bearer good-api-key")
	req.Header.Set("Accept", "application/json")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code, "a valid API token must unlock a private note on the public GET route")
}

// TestAPITokenAuth_PrivateNote_UnknownBearer_Returns401 is the negative counterpart: an unknown
// API key must not reach a private note. The usecase gets an anonymous reader and refuses it.
func (s *AuthHandlerSuite) TestAPITokenAuth_PrivateNote_UnknownBearer_Returns401() {
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "bad-key").Return(nil, "", domain.ErrNotFound)
	s.manager.EXPECT().Read(gomock.Any(), testID, anonymousReader()).Return(nil, domain.ErrForbidden)

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
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(nil, domain.ErrNotFound)

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
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(nil, "", domain.ErrNotFound)

	req := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	req.Header.Set("Accept", "text/html")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusNotFound, rec.Code)
	s.Contains(rec.Header().Get("Content-Type"), "text/html")
}

// ── Owner bypass for edit_code (session and API-token callers) ──.

// TestUpdateNote_SessionOwner_BypassesEditCode verifies that a browser session (the ogen
// single-segment PUT /notes/{id} route) resolves the caller and lets the note's owner update it
// with no edit_code.
func (s *AuthHandlerSuite) TestUpdateNote_SessionOwner_BypassesEditCode() {
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-owner").Return(&domain.User{
		ID:        testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)
	s.manager.EXPECT().Update(gomock.Any(), testID, "", caller(testUserID1), gomock.Any()).
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
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "valid-key").Return(&domain.User{
		ID:        testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, "token-hash", nil)
	s.manager.EXPECT().Update(gomock.Any(), testID, "", caller(testUserID1), gomock.Any()).
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
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-owner").Return(&domain.User{
		ID:        testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)
	s.manager.EXPECT().Delete(gomock.Any(), testID, "", caller(testUserID1)).Return(nil)

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
	s.auth.EXPECT().GetSession(gomock.Any(), "sess-other").Return(&domain.User{
		ID:        testUserID2,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)
	s.manager.EXPECT().Update(gomock.Any(), testID, "", caller(testUserID2), gomock.Any()).
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

	s.auth.EXPECT().GetSession(gomock.Any(), "sess-owner").Return(&domain.User{
		ID:        testUserID1,
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}, nil)
	s.manager.EXPECT().Update(gomock.Any(), mid, "", caller(testUserID1), gomock.Any()).
		Return(newTestNote("Guide", "new body"), nil)

	body := `{"title":"Guide","content":"new body"}`
	req := httptest.NewRequest(http.MethodPut, "/notes/"+mid, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.sessionCookie("sess-owner"))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
}
