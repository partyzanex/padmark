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
)

// Short links and files also answer at the bare GET /{id}: the handler reads the note first, and
// when no note has the slug the same read gives the kind of the record that has it
// (domain.OtherKindError). The handler then calls only the Manager of that kind. The strict mocks
// fail on a call to another Manager. The /r/{id} tests cover the link responses in detail.

func (s *HandlerSuite) TestGetNoteOrLink_NoteSlug_ReadsOnlyNote() {
	s.manager.EXPECT().Read(gomock.Any(), testID, withBurn(true)).Return(newTestNote("Title", "body"), nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `"title":"Title"`)
	s.Contains(w.Body.String(), `"content":"body"`)
}

func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_DirectRedirect() {
	expectOtherKind(s.manager, testLinkID, domain.KindLink)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, withBurn(true)).Return(newTestLink(), nil)

	w := httptest.NewRecorder()
	s.newLinkRouter(true).ServeHTTP(w, newLinkClickRequest("/"+testLinkID))

	s.Equal(http.StatusMovedPermanently, w.Code)
	s.Equal(testLinkTargetURL, w.Header().Get("Location"))
}

func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_Interstitial() {
	expectOtherKind(s.manager, testLinkID, domain.KindLink)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, withBurn(true)).Return(newTestLink(), nil)

	w := httptest.NewRecorder()
	s.newLinkRouter(false).ServeHTTP(w, newLinkClickRequest("/"+testLinkID))

	s.Equal(http.StatusOK, w.Code)
	s.Empty(w.Header().Get("Location"))
	s.Contains(w.Body.String(), testLinkTargetURL)
}

// TestGetNoteOrLink_LinkSlug_JSON: an API client gets the link through the note read in JSON
// format, and the link Manager answers.
func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_JSON() {
	s.manager.EXPECT().Read(gomock.Any(), testLinkID, withBurn(true)).Return(nil, otherKindError(domain.KindLink))
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, withBurn(true)).Return(newTestLink(), nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testLinkID, nil)
	r.Header.Set("Accept", "application/json")

	s.newLinkRouter(true).ServeHTTP(w, r)

	s.Equal(http.StatusMovedPermanently, w.Code)
	s.Equal(testLinkTargetURL, w.Header().Get("Location"))
}

// TestGetNoteOrLink_LinkSlug_ExpiredGone: an expired link keeps its namespace row until a delete,
// so the note read gives its kind and the link Manager answers, with 410.
func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_ExpiredGone() {
	expectOtherKind(s.manager, testLinkID, domain.KindLink)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, withBurn(true)).
		Return(nil, fmt.Errorf("follow link: %w", domain.ErrExpired))

	w := httptest.NewRecorder()
	s.newLinkRouter(true).ServeHTTP(w, newLinkClickRequest("/"+testLinkID))

	s.Equal(http.StatusGone, w.Code)
}

// TestGetNoteOrLink_LinkSlug_NotFoundKeepsNoteAnswer: a link that the Manager does not find (a
// parallel delete) gets the negotiated not-found answer of a slug without a record.
func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_NotFoundKeepsNoteAnswer() {
	expectOtherKind(s.manager, testLinkID, domain.KindLink)
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
	expectOtherKind(s.manager, testLinkID, domain.KindLink)
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
	expectOtherKind(s.manager, testLinkID, domain.KindLink)
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
// gets the not-found answer of a slug without a record. No other Manager is called.
func (s *HandlerSuite) TestGetNoteOrLink_FileSlug_FilesOff_NotFound() {
	expectOtherKind(s.manager, testID, domain.KindFile)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

// TestGetNoteOrLink_UnknownKind_NotFound: a kind that the handler does not serve gets the
// not-found answer, and no other Manager is called. Storage gives only links and files
// (domain.NoteNotFound), so only a code fault can give another kind.
func (s *HandlerSuite) TestGetNoteOrLink_UnknownKind_NotFound() {
	expectOtherKind(s.manager, testID, domain.RecordKind("videos"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

// TestGetNoteOrLink_MissingSlug_JSON404: a slug that names nothing keeps the note read's
// negotiated not-found answer, so an API client still gets JSON. No other Manager is called.
func (s *HandlerSuite) TestGetNoteOrLink_MissingSlug_JSON404() {
	expectNoteReadError(s.manager, testID, gomock.Any(), fmt.Errorf("inspect note: %w", domain.ErrNotFound))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

// TestGetNoteOrLink_MissingSlug_HTML404: a browser gets the not-found page.
func (s *HandlerSuite) TestGetNoteOrLink_MissingSlug_HTML404() {
	expectNoteReadError(s.manager, testID, gomock.Any(), domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "not found")
}

// TestGetNoteOrLink_ReadError_500: a failed note read is a server error (fail-hard), and no other
// Manager is called.
func (s *HandlerSuite) TestGetNoteOrLink_ReadError_500() {
	expectNoteReadError(s.manager, testID, gomock.Any(), errors.New("database is closed"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
	s.NotContains(w.Body.String(), "database is closed")
}

// TestGetNote_NotesPath_OtherKind_NotFound: GET /notes/{id} serves only notes. The slug of a short
// link or a file gets the not-found answer of a missing note in each format, the body does not
// tell the kind, and no other Manager is called.
func (s *HandlerSuite) TestGetNote_NotesPath_OtherKind_NotFound() {
	for _, kind := range []domain.RecordKind{domain.KindLink, domain.KindFile} {
		for _, accept := range []string{"application/json", "text/html", "text/markdown"} {
			s.Run(string(kind)+" "+accept, func() {
				expectOtherKind(s.manager, testLinkID, kind)

				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/notes/"+testLinkID, nil)
				r.Header.Set("Accept", accept)

				s.router.ServeHTTP(w, r)

				s.Equal(http.StatusNotFound, w.Code)
				s.NotContains(w.Body.String(), "record of kind", "the answer must not tell the kind")
			})
		}
	}
}

// TestGetNoteOrLink_MultiSegmentSlug_NoLinkLookup: a short link slug never contains "/", so no
// namespace row tells another kind for a multi-segment slug, and a missing note is answered
// without a link lookup (the strict mock fails on one).
func (s *HandlerSuite) TestGetNoteOrLink_MultiSegmentSlug_NoLinkLookup() {
	s.manager.EXPECT().Read(gomock.Any(), "docs/guide", gomock.Any()).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/docs/guide", nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// TestEditPage_NotFound: /edit/{id} for a slug that names no record shows the "not found" page of
// the note editor after the note read, without a link or file Manager call.
func (s *HandlerSuite) TestEditPage_NotFound() {
	s.manager.EXPECT().Inspect(gomock.Any(), "missing", gomock.Any()).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/missing", nil))

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Body.String(), "not found")
}

// TestEditPage_LinkSlug_NotFound: a link that the Manager does not find gets the same page.
func (s *HandlerSuite) TestEditPage_LinkSlug_NotFound() {
	expectOtherKindEditor(s.manager, testLinkID, domain.KindLink)
	s.linkManager.EXPECT().Inspect(gomock.Any(), testLinkID, gomock.Any()).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testLinkID, nil))

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Body.String(), "not found")
}

// TestEditPage_FileSlug_FilesOff_NotFound: without object storage a file slug gets the same page.
func (s *HandlerSuite) TestEditPage_FileSlug_FilesOff_NotFound() {
	expectOtherKindEditor(s.manager, testID, domain.KindFile)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil))

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Body.String(), "not found")
}

// TestEditPage_UnknownKind_NotFound: the editor gives the same page for a kind that it does not
// serve.
func (s *HandlerSuite) TestEditPage_UnknownKind_NotFound() {
	expectOtherKindEditor(s.manager, testID, domain.RecordKind("videos"))

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil))

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Body.String(), "not found")
}

// TestEditPage_ReadError_500: a failed note read gives the server error page.
func (s *HandlerSuite) TestEditPage_ReadError_500() {
	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(nil, errors.New("database is closed"))

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil))

	s.Equal(http.StatusInternalServerError, w.Code)
	s.NotContains(w.Body.String(), "database is closed")
}
