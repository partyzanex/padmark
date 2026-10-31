package web_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

// Short links also answer at the bare GET /{id}: the catch-all reads the note first and follows
// the link only when no note has the slug. The /r/{id} tests cover the link responses in detail.

func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_DirectRedirect() {
	s.manager.EXPECT().Peek(gomock.Any(), testLinkID).Return(nil, domain.ErrNotFound)
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(newTestLink(), nil)

	w := httptest.NewRecorder()
	s.newLinkRouter(true).ServeHTTP(w, newLinkClickRequest("/"+testLinkID))

	s.Equal(http.StatusMovedPermanently, w.Code)
	s.Equal(testLinkTargetURL, w.Header().Get("Location"))
}

func (s *HandlerSuite) TestGetNoteOrLink_LinkSlug_Interstitial() {
	s.manager.EXPECT().Peek(gomock.Any(), testLinkID).Return(nil, domain.ErrNotFound)
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(newTestLink(), nil)

	w := httptest.NewRecorder()
	s.newLinkRouter(false).ServeHTTP(w, newLinkClickRequest("/"+testLinkID))

	s.Equal(http.StatusOK, w.Code)
	s.Empty(w.Header().Get("Location"))
	s.Contains(w.Body.String(), testLinkTargetURL)
}

// TestGetNoteOrLink_ImplicitAccess_BurnLinkRefused: a HEAD probe on the bare slug must not consume
// a burn_after_click link, the same as on /r/{id}.
func (s *HandlerSuite) TestGetNoteOrLink_ImplicitAccess_BurnLinkRefused() {
	s.manager.EXPECT().Peek(gomock.Any(), testLinkID).Return(nil, domain.ErrNotFound)
	s.linkManager.EXPECT().RedirectWithoutBurn(gomock.Any(), testLinkID).
		Return(nil, fmt.Errorf("redirect: %w", domain.ErrBurnIntentRequired))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodHead, "/"+testLinkID, nil)
	r.Header.Set("Accept", "text/html")

	s.newLinkRouter(true).ServeHTTP(w, r)

	s.Equal(http.StatusConflict, w.Code)
	s.Empty(w.Header().Get("Location"))
}

func (s *HandlerSuite) TestGetNoteOrLink_PrivateLink_AnonymousGoesToLogin() {
	s.manager.EXPECT().Peek(gomock.Any(), testLinkID).Return(nil, domain.ErrNotFound)
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, links.FollowOptions{
		CallerID: uuid.Nil, Authenticated: false, AllowBurn: true,
	}).Return(nil, domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/" + testLinkID)
	r.Header.Set("Accept", "text/html")

	s.newLinkRouterWithTOTPAuth().ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="))
	s.NotContains(w.Body.String(), testLinkTargetURL)
}

// TestGetNoteOrLink_NeitherNoteNorLink_JSON404: a slug that names nothing keeps the note read's
// negotiated not-found answer, so an API client still gets JSON.
func (s *HandlerSuite) TestGetNoteOrLink_NeitherNoteNorLink_JSON404() {
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(nil, domain.ErrNotFound)
	s.linkManager.EXPECT().Redirect(gomock.Any(), testID).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

// TestGetNoteOrLink_MultiSegmentSlug_NoLinkLookup: a short link slug never contains "/", so a
// missing multi-segment note is answered without a link lookup (the strict mock fails on one).
func (s *HandlerSuite) TestGetNoteOrLink_MultiSegmentSlug_NoLinkLookup() {
	s.manager.EXPECT().Peek(gomock.Any(), "docs/guide").Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/docs/guide", nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}
