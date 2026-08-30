package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
)

const (
	testLinkID        = "link-abc123"
	testLinkTargetURL = "https://example.com/dest"
)

func newTestLink() *domain.Link {
	return &domain.Link{
		ID:        testLinkID,
		TargetURL: testLinkTargetURL,
		EditCode:  "editcode1234",
		CreatedAt: newTestNote("", "").CreatedAt,
	}
}

// testLinkResponse mirrors the LinkResponse/CreateLinkResponse JSON shape for decoding.
type testLinkResponse struct {
	ID             string `json:"id"`
	TargetURL      string `json:"target_url"`
	EditCode       string `json:"edit_code"`
	Views          int    `json:"views"`
	BurnAfterClick bool   `json:"burn_after_click"`
}

// newLinkRouter builds a router with DirectRedirect set explicitly, since RouterOptions'
// zero value (false) would otherwise always exercise the interstitial path.
func (s *HandlerSuite) newLinkRouter(directRedirect bool) http.Handler {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		MaxBodyBytes:   256 * 1024,
		CSRFSecret:     testCSRFSecret,
		DirectRedirect: directRedirect,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newLinkRouterWithTOTPAuth is newLinkRouter(true) with a TOTP AuthManager attached, for privacy
// tests — none of them need the interstitial mode, only the 301 vs. denied distinction.
func (s *HandlerSuite) newLinkRouterWithTOTPAuth() http.Handler {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog).
		WithAuthManager(s.authMgr)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		MaxBodyBytes:   256 * 1024,
		CSRFSecret:     testCSRFSecret,
		DirectRedirect: true,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// ── POST /links ──

func (s *HandlerSuite) TestCreateLink_OK() {
	link := newTestLink()
	s.linkManager.EXPECT().Create(gomock.Any(), "https://example.com/dest", gomock.Any()).Return(link, nil)

	body := `{"target_url":"https://example.com/dest"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/links", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
	s.Equal("/r/"+testLinkID, w.Header().Get("Location"))

	var resp testLinkResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal(testLinkID, resp.ID)
	s.Equal("https://example.com/dest", resp.TargetURL)
	s.Equal("editcode1234", resp.EditCode)
}

func (s *HandlerSuite) TestCreateLink_InvalidURL() {
	s.linkManager.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, domain.ErrInvalidURL)

	body := `{"target_url":"javascript:alert(1)"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/links", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *HandlerSuite) TestCreateLink_SlugConflict() {
	s.linkManager.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, domain.ErrSlugConflict)

	body := `{"target_url":"https://example.com","slug":"taken"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/links", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusConflict, w.Code)
}

// ── POST /links/bulk ──

// testBulkLinkResult mirrors BulkLinkResult for decoding test responses.
type testBulkLinkResult struct {
	TargetURL string `json:"target_url"`
	Error     string `json:"error"`
	ID        string `json:"id"`
	EditCode  string `json:"edit_code"`
}

func (s *HandlerSuite) TestCreateLinksBulk_PartialSuccess() {
	ok := newTestLink()
	gomock.InOrder(
		s.linkManager.EXPECT().Create(gomock.Any(), "https://example.com/ok", gomock.Any()).Return(ok, nil),
		s.linkManager.EXPECT().Create(gomock.Any(), "javascript:alert(1)", gomock.Any()).
			Return(nil, domain.ErrInvalidURL),
	)

	body := `{"links":[{"target_url":"https://example.com/ok"},{"target_url":"javascript:alert(1)"}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/links/bulk", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	var resp struct {
		Results []testBulkLinkResult `json:"results"`
	}
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Require().Len(resp.Results, 2)

	s.Equal(testLinkID, resp.Results[0].ID)
	s.Empty(resp.Results[0].Error)

	s.Empty(resp.Results[1].ID)
	s.Equal(domain.ErrInvalidURL.Error(), resp.Results[1].Error)
}

func (s *HandlerSuite) TestCreateLinksBulk_TooManyItems_Returns400() {
	items := make([]string, 101)
	for i := range items {
		items[i] = `{"target_url":"https://example.com"}`
	}

	body := `{"links":[` + strings.Join(items, ",") + `]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/links/bulk", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

// ── GET /links/{id} ──

func (s *HandlerSuite) TestGetLink_OK() {
	link := newTestLink()
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	var resp testLinkResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("https://example.com/dest", resp.TargetURL)
	// GET /links/{id} is a metadata-only read: it must never return the edit_code.
	s.Empty(resp.EditCode)
}

func (s *HandlerSuite) TestGetLink_NotFound() {
	s.linkManager.EXPECT().Peek(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/missing", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetLink_Expired() {
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(nil, domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
}

func (s *HandlerSuite) TestGetLink_OwnerOnly_NonOwner_Unauthorized() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}
	other := &domain.User{ID: uuid.New(), Username: "bob"}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyOwner)
	link.OwnerID = &owner.ID

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(other, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// ── DELETE /links/{id} ──

func (s *HandlerSuite) TestDeleteLink_OK() {
	s.expectLinkMutationGate()
	s.linkManager.EXPECT().Delete(gomock.Any(), testLinkID, "editcode1234", uuid.Nil).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/links/"+testLinkID, nil)
	r.Header.Set("X-Edit-Code", "editcode1234")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

func (s *HandlerSuite) TestDeleteLink_Forbidden() {
	s.expectLinkMutationGate()
	s.linkManager.EXPECT().Delete(gomock.Any(), testLinkID, "wrong", uuid.Nil).
		Return(domain.ErrInvalidEditCode)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/links/"+testLinkID, nil)
	r.Header.Set("X-Edit-Code", "wrong")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestDeleteLink_Gated_PrivateLink verifies the anonymous link-mutation gate: with auth
// configured, an anonymous DELETE of a privacy=authenticated link is refused with 401 before
// the usecase runs (Delete having no EXPECT guarantees that) — an anonymous caller could not
// read the link, so they may not mutate it via an edit code either.
func (s *HandlerSuite) TestDeleteLink_Gated_PrivateLink() {
	router := s.newLinkRouterWithTOTPAuth()
	link := newTestLink()
	link.Privacy = new(domain.PrivacyAuthenticated)
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/links/"+testLinkID, nil)
	r.Header.Set("X-Edit-Code", "editcode1234")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
	// DELETE /links/{id} 401 has no schema in the ogen spec → empty DeleteLinkUnauthorized.
	s.Empty(w.Body.String())
}

// TestDeleteLink_Gated_PublicLink_WrongEditCode verifies the gate passes for a link an
// anonymous caller could read: the wrong edit code then surfaces as the usecase's 403 —
// never the middleware-level 401.
func (s *HandlerSuite) TestDeleteLink_Gated_PublicLink_WrongEditCode() {
	router := s.newLinkRouterWithTOTPAuth()
	s.expectLinkMutationGate()
	s.linkManager.EXPECT().Delete(gomock.Any(), testLinkID, "wrong", uuid.Nil).
		Return(domain.ErrInvalidEditCode)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/links/"+testLinkID, nil)
	r.Header.Set("X-Edit-Code", "wrong")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

func (s *HandlerSuite) TestDeleteLink_OwnerBypass_NoEditCode() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}

	s.linkManager.EXPECT().Delete(gomock.Any(), testLinkID, "", owner.ID).Return(nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(owner, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/links/"+testLinkID, nil)
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie

	router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

// ── GET /r/{id} ──

func (s *HandlerSuite) TestRedirectLink_DirectMode_301() {
	link := newTestLink()
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouter(true)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusMovedPermanently, w.Code)
	s.Equal("https://example.com/dest", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestRedirectLink_InterstitialMode_200() {
	link := newTestLink()
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouter(false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "https://example.com/dest")
	s.Contains(body, "Redirecting")
}

// TestRedirectLink_PerLinkOverride_ForcesDirectDespiteServerDefault verifies a link's own
// DirectRedirect=true wins even when the server default (RouterOptions.DirectRedirect) is false.
func (s *HandlerSuite) TestRedirectLink_PerLinkOverride_ForcesDirectDespiteServerDefault() {
	forceDirect := true
	link := newTestLink()
	link.DirectRedirect = &forceDirect
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouter(false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusMovedPermanently, w.Code)
	s.Equal(testLinkTargetURL, w.Header().Get("Location"))
}

// TestRedirectLink_PerLinkOverride_ForcesInterstitialDespiteServerDefault is the inverse: a
// link's own DirectRedirect=false wins even when the server default is true.
func (s *HandlerSuite) TestRedirectLink_PerLinkOverride_ForcesInterstitialDespiteServerDefault() {
	forceInterstitial := false
	link := newTestLink()
	link.DirectRedirect = &forceInterstitial
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouter(true)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
}

func (s *HandlerSuite) TestRedirectLink_Expired() {
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(nil, domain.ErrExpired)

	router := s.newLinkRouter(true)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
}

func (s *HandlerSuite) TestRedirectLink_NotFound() {
	s.linkManager.EXPECT().Redirect(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	router := s.newLinkRouter(true)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/missing", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// TestRedirectLink_BurnAfterClick_ConsumedOnce verifies that a burn-after-click link redirects
// exactly once, then a second GET gets 404 — links.Manager.Redirect's atomic consumeLink is what
// actually enforces "exactly once"; this test only verifies the handler surfaces that correctly
// on both calls (the mock stands in for the atomicity, already covered at the manager-test level).
func (s *HandlerSuite) TestRedirectLink_BurnAfterClick_ConsumedOnce() {
	link := newTestLink()
	link.BurnAfterClick = true

	gomock.InOrder(
		s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil),
		s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(nil, domain.ErrNotFound),
	)

	router := s.newLinkRouter(true)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil))
	s.Equal(http.StatusMovedPermanently, first.Code)

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil))
	s.Equal(http.StatusNotFound, second.Code)
}

// TestRedirectLink_OwnerOnly_NonOwner_HTML_RedirectToLogin mirrors the note-side IDOR regression
// (TestOwnerOnlyNote_OtherAuthenticatedUser_Unauthorized): a privacy: owner link must deny a
// different authenticated caller, not just anonymous ones, and — being a browser route — redirect
// to /login rather than returning a bare 401.
func (s *HandlerSuite) TestRedirectLink_OwnerOnly_NonOwner_HTML_RedirectToLogin() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}
	other := &domain.User{ID: uuid.New(), Username: "bob"}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyOwner)
	link.OwnerID = &owner.ID

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(other, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"a different authenticated caller must not follow an owner-only link")
}

// TestRedirectLink_OwnerOnly_Owner_Authorized is the positive counterpart: the link's own owner
// may always follow it.
func (s *HandlerSuite) TestRedirectLink_OwnerOnly_Owner_Authorized() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyOwner)
	link.OwnerID = &owner.ID

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(owner, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie

	router.ServeHTTP(w, r)

	s.Equal(http.StatusMovedPermanently, w.Code)
}

// TestRedirectLink_OwnerOnly_Anonymous_Unauthorized mirrors TestOwnerOnlyNote_Anonymous_
// Unauthorized: a privacy: owner link denies an anonymous (no-credential) caller, distinctly
// from denying a different authenticated caller — completes the privacy × caller matrix alongside
// TestRedirectLink_OwnerOnly_NonOwner_HTML_RedirectToLogin and _Owner_Authorized above.
func (s *HandlerSuite) TestRedirectLink_OwnerOnly_Anonymous_Unauthorized() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyOwner)
	link.OwnerID = &owner.ID

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// TestRedirectLink_OwnerOnly_Anonymous_HTML_RedirectToLogin is the HTML-negotiated counterpart to
// the anonymous case above — mirrors TestOwnerOnlyNote_Anonymous_HTML_RedirectToLogin.
func (s *HandlerSuite) TestRedirectLink_OwnerOnly_Anonymous_HTML_RedirectToLogin() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyOwner)
	link.OwnerID = &owner.ID

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="))
}

// TestRedirectLink_PrivacyAuthenticated_Unauthenticated mirrors TestPrivateNote_TOTPMode_
// Unauthenticated: a privacy: authenticated link denies any caller with no credential at all —
// unlike privacy: owner, any logged-in caller (not just the creator) is enough, but anonymous
// never is.
func (s *HandlerSuite) TestRedirectLink_PrivacyAuthenticated_Unauthenticated() {
	link := newTestLink()
	link.Privacy = new(domain.PrivacyAuthenticated)

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="))
}

// TestRedirectLink_PrivacyAuthenticated_Authenticated mirrors TestPrivateNote_TOTPMode_
// Authenticated: any valid session — not just the link's owner — may follow a privacy:
// authenticated link.
func (s *HandlerSuite) TestRedirectLink_PrivacyAuthenticated_Authenticated() {
	usr := &domain.User{ID: uuid.New(), Username: "alice"}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyAuthenticated)

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie

	router.ServeHTTP(w, r)

	s.Equal(http.StatusMovedPermanently, w.Code)
}

// TestGetLink_PrivacyAuthenticated_Authenticated confirms OgenHandler.GetLink's own privacy gate
// (added as a bug fix in the HTTP handlers section) also grants access to any authenticated
// caller for privacy: authenticated, not just the link's owner.
func (s *HandlerSuite) TestGetLink_PrivacyAuthenticated_Authenticated() {
	usr := &domain.User{ID: uuid.New(), Username: "alice"}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyAuthenticated)

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

// TestGetLink_PrivacyAuthenticated_Anonymous_Unauthorized is the negative counterpart: no
// credential at all must not satisfy privacy: authenticated.
func (s *HandlerSuite) TestGetLink_PrivacyAuthenticated_Anonymous_Unauthorized() {
	link := newTestLink()
	link.Privacy = new(domain.PrivacyAuthenticated)

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}
