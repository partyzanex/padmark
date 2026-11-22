package web_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/slugs"
)

// Short links and files also answer at the bare GET /{id}: the resolver gives the kind of the
// slug, and the handler calls only the Manager of that kind. The strict mocks fail on a call to
// another Manager. The /r/{id} tests cover the link responses in detail.

func (s *HandlerSuite) TestGetNoteOrLink_NoteSlug_ReadsOnlyNote() {
	s.expectKind(testID, slugs.KindNote)
	s.manager.EXPECT().Read(gomock.Any(), testID, withBurn(true)).Return(newTestNote("Title", "body"), nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `"content":"body"`)
}

func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_DirectRedirect() {
	s.expectKind(testLinkID, slugs.KindLink)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, withBurn(true)).Return(newTestLink(), nil)

	w := httptest.NewRecorder()
	s.newLinkRouter(true).ServeHTTP(w, newLinkClickRequest("/"+testLinkID))

	s.Equal(http.StatusMovedPermanently, w.Code)
	s.Equal(testLinkTargetURL, w.Header().Get("Location"))
}

func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_Interstitial() {
	s.expectKind(testLinkID, slugs.KindLink)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, withBurn(true)).Return(newTestLink(), nil)

	w := httptest.NewRecorder()
	s.newLinkRouter(false).ServeHTTP(w, newLinkClickRequest("/"+testLinkID))

	s.Equal(http.StatusOK, w.Code)
	s.Empty(w.Header().Get("Location"))
	s.Contains(w.Body.String(), testLinkTargetURL)
}

// TestGetNoteOrLink_LinkSlug_ExpiredGone: an expired link keeps its namespace row until a delete,
// so the link Manager still answers, with 410.
func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_ExpiredGone() {
	s.expectKind(testLinkID, slugs.KindLink)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, withBurn(true)).
		Return(nil, fmt.Errorf("follow link: %w", domain.ErrExpired))

	w := httptest.NewRecorder()
	s.newLinkRouter(true).ServeHTTP(w, newLinkClickRequest("/"+testLinkID))

	s.Equal(http.StatusGone, w.Code)
}

// TestGetNoteOrLink_LinkSlug_NotFoundKeepsNoteAnswer: a link that the Manager does not find (a
// parallel delete) gets the negotiated not-found answer of a slug without a record.
func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_NotFoundKeepsNoteAnswer() {
	s.expectKind(testLinkID, slugs.KindLink)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, withBurn(true)).
		Return(nil, fmt.Errorf("follow link: %w", domain.ErrNotFound))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testLinkID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

// TestGetNoteOrLink_ImplicitAccess_BurnLinkRefused: a HEAD probe on the bare slug must not consume
// a burn_after_click link, the same as on /r/{id}.
func (s *HandlerSuite) TestGetNoteOrLink_ImplicitAccess_BurnLinkRefused() {
	s.expectKind(testLinkID, slugs.KindLink)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, withBurn(false)).
		Return(nil, fmt.Errorf("redirect: %w", domain.ErrBurnIntentRequired))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodHead, "/"+testLinkID, nil)
	r.Header.Set("Accept", "text/html")

	s.newLinkRouter(true).ServeHTTP(w, r)

	s.Equal(http.StatusConflict, w.Code)
	s.Empty(w.Header().Get("Location"))
}

func (s *HandlerSuite) TestGetNoteOrLink_PrivateLink_AnonymousGoesToLogin() {
	s.expectKind(testLinkID, slugs.KindLink)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, domain.Reader{
		CallerID: uuid.Nil, Authenticated: false, AllowBurn: true, SkipPrivacy: false,
	}).Return(nil, domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/" + testLinkID)
	r.Header.Set("Accept", "text/html")

	s.newLinkRouterWithTOTPAuth().ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="))
	s.NotContains(w.Body.String(), testLinkTargetURL)
}

// TestGetNoteOrLink_FileSlug_FilesOff_NotFound: a file slug on a server without object storage
// gets the not-found answer of a slug without a record. No Manager is called.
func (s *HandlerSuite) TestGetNoteOrLink_FileSlug_FilesOff_NotFound() {
	s.expectKind(testID, slugs.KindFile)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

// TestGetNoteOrLink_MissingSlug_JSON404: a slug that names nothing keeps the note read's
// negotiated not-found answer, so an API client still gets JSON. No Manager is called.
func (s *HandlerSuite) TestGetNoteOrLink_MissingSlug_JSON404() {
	s.slugs.EXPECT().KindOf(gomock.Any(), testID).Return(slugs.Kind(""), fmt.Errorf("slug kind: %w", domain.ErrNotFound))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

// TestGetNoteOrLink_MissingSlug_HTML404: a browser gets the not-found page.
func (s *HandlerSuite) TestGetNoteOrLink_MissingSlug_HTML404() {
	s.slugs.EXPECT().KindOf(gomock.Any(), testID).Return(slugs.Kind(""), domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "not found")
}

// TestGetNoteOrLink_ResolverError_500: a failed namespace read is a server error (fail-hard), and
// no Manager is called.
func (s *HandlerSuite) TestGetNoteOrLink_ResolverError_500() {
	s.slugs.EXPECT().KindOf(gomock.Any(), testID).Return(slugs.Kind(""), errors.New("database is closed"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
	s.NotContains(w.Body.String(), "database is closed")
}

// TestGetNoteOrLink_UnknownKind_500: a kind that the handler does not serve is a server error,
// and no Manager is called.
func (s *HandlerSuite) TestGetNoteOrLink_UnknownKind_500() {
	s.expectKind(testID, slugs.Kind("videos"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}

// TestEditPage_UnknownKind_500: the editor gives the server error page for such a kind.
func (s *HandlerSuite) TestEditPage_UnknownKind_500() {
	s.expectKind(testID, slugs.Kind("videos"))

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil))

	s.Equal(http.StatusInternalServerError, w.Code)
}

// TestGetNoteOrLink_MultiSegmentSlug_NoLinkLookup: a short link slug never contains "/", so the
// resolver gives a note for a multi-segment slug, and a missing note is answered without a link
// lookup (the strict mock fails on one).
func (s *HandlerSuite) TestGetNoteOrLink_MultiSegmentSlug_NoLinkLookup() {
	s.expectKind("docs/guide", slugs.KindNote)
	s.manager.EXPECT().Read(gomock.Any(), "docs/guide", gomock.Any()).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/docs/guide", nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// TestEditPage_MissingSlug_NotFound: /edit/{id} for a slug that names no record shows the
// "not found" page of the note editor without a Manager call.
func (s *HandlerSuite) TestEditPage_MissingSlug_NotFound() {
	s.slugs.EXPECT().KindOf(gomock.Any(), "missing").Return(slugs.Kind(""), domain.ErrNotFound)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/missing", nil))

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Body.String(), "not found")
}

// TestEditPage_LinkSlug_NotFound: a link that the Manager does not find gets the same page.
func (s *HandlerSuite) TestEditPage_LinkSlug_NotFound() {
	s.expectKind(testLinkID, slugs.KindLink)
	s.linkManager.EXPECT().Inspect(gomock.Any(), testLinkID, gomock.Any()).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testLinkID, nil))

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Body.String(), "not found")
}

// TestEditPage_FileSlug_FilesOff_NotFound: without object storage a file slug gets the same page.
func (s *HandlerSuite) TestEditPage_FileSlug_FilesOff_NotFound() {
	s.expectKind(testID, slugs.KindFile)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil))

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Body.String(), "not found")
}

// TestEditPage_ResolverError_500: a failed namespace read gives the server error page.
func (s *HandlerSuite) TestEditPage_ResolverError_500() {
	s.slugs.EXPECT().KindOf(gomock.Any(), testID).Return(slugs.Kind(""), errors.New("database is closed"))

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil))

	s.Equal(http.StatusInternalServerError, w.Code)
	s.NotContains(w.Body.String(), "database is closed")
}

// --- helpers ---

// expectKind makes the resolver give kind for slug once.
func (s *HandlerSuite) expectKind(slug string, kind slugs.Kind) {
	s.slugs.EXPECT().KindOf(gomock.Any(), slug).Return(kind, nil)
}
