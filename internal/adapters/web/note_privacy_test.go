package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// ── TOTP-mode private note and CanEdit fixes ──
//
// In TOTP-only deployments (HandlerConfig.Accounts set) the usecase must respect the session before serving
// private notes, and CanEdit must follow the note's visibility to the caller.
//
// Call-flow for GET /notes/{id} in TOTP mode:
//   1. The auth middleware resolves the session of the note route and puts the user in context.
//   2. The handler builds the domain.Reader from the context and calls ReadRendered.
//   3. The usecase checks privacy (ErrForbidden) before any side effect.
//   4. The page sets CanEdit with Note.VisibleTo(reader).

// TestPrivateNote_TOTPMode_Unauthenticated verifies that an unauthenticated
// request to a private note is redirected to /login in TOTP-only mode. The usecase refuses an
// anonymous reader for each private level (authenticated and owner), so one case covers both.
func (s *HandlerSuite) TestPrivateNote_TOTPMode_Unauthenticated() {
	// No session cookie: the reader is anonymous and the usecase refuses it.
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, anonymousReader()).Return(nil, "", domain.ErrForbidden)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"unauthenticated request to private note in TOTP mode must redirect to /login")
}

// TestPrivateNote_TOTPMode_Authenticated verifies that a valid session grants
// access to a private note in TOTP-only mode.
// GetSession is called exactly once: the auth middleware resolves the session for the
// note route and stores the user in context, so the reader and CanEdit read it from context
// without re-querying.
func (s *HandlerSuite) TestPrivateNote_TOTPMode_Authenticated() {
	note := newTestNote("secret", "private content")
	note.Privacy = new(domain.PrivacyAuthenticated)
	usr := &domain.User{
		ID: uuid.New(), Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(note, "rendered", nil)
	// One GetSession call, made by the auth middleware; the handler reads from context.
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))
	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

// TestOwnerOnlyNote_Owner_Authorized verifies that a note with privacy: owner is readable by
// the exact authenticated session that created it.
func (s *HandlerSuite) TestOwnerOnlyNote_Owner_Authorized() {
	owner := &domain.User{
		ID: uuid.New(), Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner.ID

	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(note, "rendered", nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(owner, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))
	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

// TestOwnerOnlyNote_OtherAuthenticatedUser_Forbidden verifies that a note with privacy: owner is
// NOT readable by a different authenticated caller, even though they are logged in — owner-only
// is scoped to the exact creator, unlike privacy: authenticated.
//
// The answer is 403, not the /login redirect an anonymous browser gets: this caller already has a
// credential, just not the right one, so a login page would send them straight back here.
func (s *HandlerSuite) TestOwnerOnlyNote_OtherAuthenticatedUser_Forbidden() {
	other := &domain.User{
		ID: uuid.New(), Username: "bob",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	// The usecase refuses the signed-in caller who is not the owner.
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, signedInReader(other.ID)).Return(nil, "", domain.ErrForbidden)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(other, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))
	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
	s.Empty(w.Header().Get("Location"), "a settled 403 must not bounce the caller anywhere")
	s.NotContains(w.Body.String(), "owner-only content",
		"the note's plaintext must never reach a non-owner's response body")
}

// TestOwnerOnlyNote_Anonymous_Unauthorized verifies that a JSON client without credentials gets
// 401 for a private note. The usecase refuses an anonymous reader for each private level
// (authenticated and owner), so one case covers both.
func (s *HandlerSuite) TestOwnerOnlyNote_Anonymous_Unauthorized() {
	s.manager.EXPECT().Read(gomock.Any(), testID, anonymousReader()).Return(nil, domain.ErrForbidden)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")
	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// TestEditPage_OwnerOnly_NonOwner_Forbidden verifies that GET /edit/{id} for a privacy: owner
// note has the same privacy rule as GET /notes/{id} — a different authenticated caller must not see the
// note's plaintext content in the editor form, even though they are logged in.
func (s *HandlerSuite) TestEditPage_OwnerOnly_NonOwner_Forbidden() {
	other := &domain.User{
		ID: uuid.New(), Username: "bob",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	// The editor reads with Inspect; the usecase refuses the signed-in caller who is not the owner.
	s.manager.EXPECT().Inspect(gomock.Any(), testID, signedInReader(other.ID)).Return(nil, domain.ErrForbidden)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(other, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))
	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code,
		"a signed-in non-owner is not missing a credential, so /login would be a dead end")
	s.Empty(w.Header().Get("Location"), "a settled 403 must not bounce the caller anywhere")
	s.NotContains(w.Body.String(), "owner-only content",
		"the note's plaintext must never reach a non-owner's response body")
}

// TestEditPage_OwnerOnly_Owner_OK verifies that GET /edit/{id} for a privacy: owner note still
// works normally for the exact authenticated session that created it.
func (s *HandlerSuite) TestEditPage_OwnerOnly_Owner_OK() {
	owner := &domain.User{
		ID: uuid.New(), Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner.ID

	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(owner, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))
	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "owner-only content")
}

// TestCanEdit_TOTPMode_Authenticated verifies that an authenticated TOTP-session
// user sees the edit button.
func (s *HandlerSuite) TestCanEdit_TOTPMode_Authenticated() {
	note := newTestNote("public note", "content")
	usr := &domain.User{
		ID: uuid.New(), Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(note, "rendered", nil)
	// The auth middleware calls GetSession once; renderNoteHTML reads the caller from the context.
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))
	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `href="/edit/`+testID+`"`,
		"authenticated TOTP user must see the edit button")
}

// TestCanEdit_NoAuthConfigured_OwnerOnlyNote_ShowsEditButton: without accounts no privacy level
// applies (domain.Reader.SkipPrivacy), so each reader of an owner-only note sees the Edit
// button, as for any other note; the save still needs the edit code. Before the Reader change
// the button was hidden in this case.
func (s *HandlerSuite) TestCanEdit_NoAuthConfigured_OwnerOnlyNote_ShowsEditButton() {
	owner := uuid.New()
	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner

	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, openReader()).Return(note, "rendered", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `href="/edit/`+testID+`"`)
}
