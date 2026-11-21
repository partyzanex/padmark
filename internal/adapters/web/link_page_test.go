package web_test

import (
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// linkPageRequest is a browser navigation to the page of the test link.
func linkPageRequest(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Sec-Fetch-Dest", "document")

	return req
}

// linkPageUser returns a signed-in user for the privacy tests of the link page.
func linkPageUser(name string) *domain.User {
	return &domain.User{
		ID: uuid.New(), Username: name,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
}

// TestLinkPage_Browser: a browser on /links/{id} gets the page of the link, with the frame and
// the actions of the note page. Only Peek is expected: the page does not count a view and does
// not burn the link (gomock fails on a Follow or Redirect call).
func (s *HandlerSuite) TestLinkPage_Browser() {
	link := newTestLink()
	link.BurnAfterClick = true
	link.Views = 3
	expires := time.Date(2030, time.March, 4, 5, 6, 0, 0, time.UTC)
	link.ExpiresAt = &expires
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, linkPageRequest("/links/"+testLinkID))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Equal("text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	s.Equal("private, no-store", rec.Header().Get("Cache-Control"))
	s.Contains(body, `class="card article-card"`)
	s.Contains(body, `id="linkTitle">example.com/`+testLinkID+`<`)
	s.Contains(body, `href="`+testLinkTargetURL+`" class="copy-value" id="targetLink"`)
	s.Contains(body, `data-url="http://example.com/`+testLinkID+`"`)
	s.Contains(body, `href="/edit/`+testLinkID+`" class="meta-btn" id="editLinkBtn"`)
	s.Contains(body, "3 views")
	s.Contains(body, "Burns after the first click")
	s.Contains(body, "Shows the destination first")
	s.Contains(body, `data-expires="2030-03-04T05:06:00Z"`)
	s.Contains(body, `href="/links" class="btn-primary header-new" aria-label="New link"`)
	s.NotContains(body, "editcode1234", "the page must never show the edit code")
}

// TestLinkPage_DirectRedirectLabel: the page names the redirect mode that /{id} applies.
func (s *HandlerSuite) TestLinkPage_DirectRedirectLabel() {
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(newTestLink(), nil)

	rec := httptest.NewRecorder()
	s.newLinkRouter(true).ServeHTTP(rec, linkPageRequest("/links/"+testLinkID))

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "Opens the destination at once")
}

// TestLinkPage_APIStaysJSON: a client that does not prefer HTML keeps the JSON read.
func (s *HandlerSuite) TestLinkPage_APIStaysJSON() {
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(newTestLink(), nil)

	req := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)
	req.Header.Set("Accept", "*/*")

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Header().Get("Content-Type"), "application/json")
	s.Contains(rec.Body.String(), `"target_url":"`+testLinkTargetURL+`"`)
}

func (s *HandlerSuite) TestLinkPage_Errors() {
	s.Run("not found", func() {
		s.linkManager.EXPECT().Peek(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, linkPageRequest("/links/missing"))

		s.Equal(http.StatusNotFound, rec.Code)
		s.Contains(rec.Header().Get("Content-Type"), "text/html")
	})

	s.Run("expired", func() {
		link := newTestLink()
		past := time.Now().Add(-time.Hour)
		link.ExpiresAt = &past
		s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, linkPageRequest("/links/"+testLinkID))

		s.Equal(http.StatusGone, rec.Code)
		s.NotContains(rec.Body.String(), testLinkTargetURL)
	})
}

// TestLinkPage_Privacy: the page applies the privacy rules of the note page. An anonymous
// browser goes to the sign-in page, a signed-in user who is not the owner gets 403, and the
// owner gets the page. The destination never reaches a caller who failed the check.
func (s *HandlerSuite) TestLinkPage_Privacy() {
	owner := linkPageUser("alice")
	link := newTestLink()
	link.Privacy = new(domain.PrivacyOwner)
	link.OwnerID = &owner.ID

	s.Run("anonymous", func() {
		s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

		rec := httptest.NewRecorder()
		s.newLinkRouterWithTOTPAuth().ServeHTTP(rec, linkPageRequest("/links/"+testLinkID))

		s.Equal(http.StatusSeeOther, rec.Code)
		s.Equal("/login?next=%2Flinks%2F"+testLinkID, rec.Header().Get("Location"))
		s.NotContains(rec.Body.String(), testLinkTargetURL)
	})

	s.Run("other user", func() {
		other := linkPageUser("bob")

		s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)
		s.authMgr.EXPECT().GetSession(gomock.Any(), "other-sess").Return(other, nil)

		req := linkPageRequest("/links/" + testLinkID)
		req.AddCookie(httptestutil.RequestCookie("padmark_session", "other-sess"))

		rec := httptest.NewRecorder()
		s.newLinkRouterWithTOTPAuth().ServeHTTP(rec, req)

		s.Equal(http.StatusForbidden, rec.Code)
		s.NotContains(rec.Body.String(), testLinkTargetURL)
	})

	s.Run("owner", func() {
		s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)
		s.authMgr.EXPECT().GetSession(gomock.Any(), "owner-sess").Return(owner, nil)

		req := linkPageRequest("/links/" + testLinkID)
		req.AddCookie(httptestutil.RequestCookie("padmark_session", "owner-sess"))

		rec := httptest.NewRecorder()
		s.newLinkRouterWithTOTPAuth().ServeHTTP(rec, req)

		s.Require().Equal(http.StatusOK, rec.Code)
		s.Contains(rec.Body.String(), testLinkTargetURL)
		s.Contains(rec.Body.String(), "Owner only")
		s.Contains(rec.Body.String(), `href="/edit/`+testLinkID+`"`)
	})
}

// TestEditPage_Link: /edit/{id} opens the short-link editor when the slug names a link, with
// the labels of the note editor. It reads the link with Peek only.
func (s *HandlerSuite) TestEditPage_Link() {
	s.manager.EXPECT().Peek(gomock.Any(), testLinkID).Return(nil, domain.ErrNotFound)
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(newTestLink(), nil)

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, linkPageRequest("/edit/"+testLinkID))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(body, `id="targetInput"`)
	s.Contains(body, `>`+testLinkTargetURL+`</textarea>`)
	s.Contains(body, `id="slugFixed"`)
	s.Contains(body, `id="burnFixed"`)
	s.NotContains(body, `id="slugInput"`, "the slug of a link is fixed after create")
	s.Contains(body, `data-kbd="Ctrl ↵">Save</button>`)
	s.Contains(body, `placeholder="Paste your edit code"`)
	s.Contains(body, "Edit code <span class=\"optional\">· required</span>")
	s.Contains(body, "Saving replaces the destination and the settings of this link.")
	s.Contains(body, `id="deleteBtn"`)
}

// TestEditPage_LinkPrivate: the link editor at /edit/{id} keeps the privacy check of the link.
func (s *HandlerSuite) TestEditPage_LinkPrivate() {
	link := newTestLink()
	link.Privacy = new(domain.PrivacyAuthenticated)

	s.manager.EXPECT().Peek(gomock.Any(), testLinkID).Return(nil, domain.ErrNotFound)
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	rec := httptest.NewRecorder()
	s.newLinkRouterWithTOTPAuth().ServeHTTP(rec, linkPageRequest("/edit/"+testLinkID))

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/login?next=%2Fedit%2F"+testLinkID, rec.Header().Get("Location"))
	s.NotContains(rec.Body.String(), testLinkTargetURL)
}

// TestEditPage_LinkOldURL: the old editor URL sends the browser to /edit/{id}.
func (s *HandlerSuite) TestEditPage_LinkOldURL() {
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, linkPageRequest("/links/"+testLinkID+"/edit"))

	s.Equal(http.StatusMovedPermanently, rec.Code)
	s.Equal("/edit/"+testLinkID, rec.Header().Get("Location"))
}
