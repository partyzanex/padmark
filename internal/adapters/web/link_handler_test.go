package web_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/tests/httptestutil"
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

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		Views: 0, BurnAfterClick: false,
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
	s.T().Helper()

	handler := web.NewHandler(s.manager, s.linkManager, discardLog())
	ogen := web.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog())

	opts := web.RouterOptions{
		MaxBodyBytes:   256 * 1024,
		CSRFSecret:     testCSRFSecret,
		DirectRedirect: directRedirect,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newLinkRouterWithTOTPAuth is newLinkRouter(true) with a TOTP AuthManager attached, for privacy
// tests — none of them need the interstitial mode, only the 301 vs. denied distinction.
func (s *HandlerSuite) newLinkRouterWithTOTPAuth() http.Handler {
	s.T().Helper()

	handler := web.NewHandler(s.manager, s.linkManager, discardLog()).
		WithAuthManager(s.authMgr)
	ogen := web.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog())

	opts := web.RouterOptions{
		MaxBodyBytes:   256 * 1024,
		CSRFSecret:     testCSRFSecret,
		DirectRedirect: true,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// ── POST /links ──.

func (s *HandlerSuite) TestCreateLink_OK() {
	link := newTestLink()
	s.linkManager.EXPECT().Create(gomock.Any(), "https://example.com/dest", gomock.Any()).Return(link, nil)

	body := `{"target_url":"https://example.com/dest"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/links", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
	s.Equal("/"+testLinkID, w.Header().Get("Location"))

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

// ── POST /links/bulk ──.

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

// ── GET /links/{id} ──.

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

// TestGetLink_Expired exercises the real expiry path. links.Manager.Peek deliberately returns
// an expired link as-is — it is the pre-auth metadata read and applies no policy — so the
// handler is the only place that can turn it into the 410 the OpenAPI spec documents.
//
// The previous version of this test mocked Peek returning domain.ErrExpired, an outcome Peek's
// contract rules out. It passed while GET /links/{id} actually answered 200 for expired links,
// out of step with GET /r/{id}: coverage of a branch nothing reaches reads exactly like coverage
// of the real one.
func (s *HandlerSuite) TestGetLink_Expired() {
	link := newTestLink()
	expired := time.Now().Add(-time.Hour)
	link.ExpiresAt = &expired

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)

	var resp struct {
		Message string `json:"message"`
	}

	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal(domain.ErrExpired.Error(), resp.Message, "410 body must follow the ErrorResponse contract")
}

// TestGetLink_NotYetExpired_OK guards the boundary from the other side: a link with a future TTL
// must still resolve. A comparison slipped the wrong way would report every dated link as gone.
func (s *HandlerSuite) TestGetLink_NotYetExpired_OK() {
	link := newTestLink()
	future := time.Now().Add(time.Hour)
	link.ExpiresAt = &future

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

// TestGetLink_ExpiredAndPrivate_Anonymous_Unauthorized pins the order of the two gates: privacy
// is decided before expiry, mirroring LinkHandler.Redirect. A stranger asking about an expired
// owner-only link gets 401, not 410 — a 410 would confirm the link had existed.
func (s *HandlerSuite) TestGetLink_ExpiredAndPrivate_Anonymous_Unauthorized() {
	owner := uuid.New()

	link := newTestLink()
	link.Privacy = new(domain.PrivacyOwner)
	link.OwnerID = &owner
	expired := time.Now().Add(-time.Hour)
	link.ExpiresAt = &expired

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestGetLink_OwnerOnly_NonOwner_Unauthorized() {
	owner := &domain.User{
		ID: uuid.New(), Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	other := &domain.User{
		ID: uuid.New(), Username: "bob",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyOwner)
	link.OwnerID = &owner.ID

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(other, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// ── DELETE /links/{id} ──.

func (s *HandlerSuite) TestDeleteLink_OK() {
	s.expectLinkMutationGate()
	s.linkManager.EXPECT().Delete(gomock.Any(), testLinkID, "editcode1234", uuid.Nil).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/links/"+testLinkID, nil)
	r.Header.Set("X-Edit-Code", "editcode1234")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

// TestDeleteLink_QueryParamIgnored pins that the edit code is read from the X-Edit-Code header
// only: a ?edit_code= query string (which leaks into proxy logs and Referer) counts as no code.
func (s *HandlerSuite) TestDeleteLink_QueryParamIgnored() {
	s.expectLinkMutationGate()
	s.linkManager.EXPECT().Delete(gomock.Any(), testLinkID, "", uuid.Nil).Return(domain.ErrInvalidEditCode)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/links/"+testLinkID+"?edit_code=fromquery", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
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
	owner := &domain.User{
		ID: uuid.New(), Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	s.linkManager.EXPECT().Delete(gomock.Any(), testLinkID, "", owner.ID).Return(nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(owner, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/links/"+testLinkID, nil)
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))

	router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

// ── GET /r/{id} ──.

// newLinkClickRequest builds GET /r/{id} the way a browser sends a deliberate link click — the
// intent a burn_after_click link requires (see explicitRead).
func newLinkClickRequest(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	r.Header.Set("Sec-Fetch-Dest", "document")

	return r
}

// TestRedirectLink_ImplicitAccess_BurnLinkRefused: a preview fetch, scanner, HEAD probe or
// embedded <img> goes through RedirectWithoutBurn; for a burn link that is a 409 and the link
// stays intact — no target leaks.
func (s *HandlerSuite) TestRedirectLink_ImplicitAccess_BurnLinkRefused() {
	cases := []struct {
		name    string
		method  string
		headers map[string]string
	}{
		{"no Accept", http.MethodGet, map[string]string{}},
		{"wildcard", http.MethodGet, map[string]string{"Accept": "*/*"}},
		{"HEAD", http.MethodHead, map[string]string{"Accept": "text/html"}},
		{"img subresource", http.MethodGet, map[string]string{
			"Accept": "image/avif,image/webp,*/*;q=0.8", "Sec-Fetch-Dest": "image",
		}},
	}

	router := s.newLinkRouter(true)

	for _, testCase := range cases {
		s.Run(testCase.name, func() {
			s.linkManager.EXPECT().RedirectWithoutBurn(gomock.Any(), testLinkID).
				Return(nil, fmt.Errorf("redirect: %w", domain.ErrBurnIntentRequired))

			w := httptest.NewRecorder()
			r := httptest.NewRequest(testCase.method, "/r/"+testLinkID, nil)

			for name, value := range testCase.headers {
				r.Header.Set(name, value)
			}

			router.ServeHTTP(w, r)

			s.Equal(http.StatusConflict, w.Code)
			s.Empty(w.Header().Get("Location"))
			s.NotContains(w.Body.String(), testLinkTargetURL)

			if testCase.method == http.MethodGet {
				s.Contains(w.Body.String(), `"code":"burn_read_not_explicit"`)
			}
		})
	}
}

// TestRedirectLink_ImplicitAccess_OrdinaryLinkFollowed: an ordinary link keeps redirecting any
// client, curl included — only burn links need deliberate intent.
func (s *HandlerSuite) TestRedirectLink_ImplicitAccess_OrdinaryLinkFollowed() {
	s.linkManager.EXPECT().RedirectWithoutBurn(gomock.Any(), testLinkID).Return(newTestLink(), nil)

	router := s.newLinkRouter(true)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/r/"+testLinkID, nil)
	r.Header.Set("Accept", "*/*")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusMovedPermanently, w.Code)
	s.Equal(testLinkTargetURL, w.Header().Get("Location"))
}

func (s *HandlerSuite) TestRedirectLink_DirectMode_301() {
	link := newTestLink()
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouter(true)

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/" + testLinkID)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusMovedPermanently, w.Code)
	s.Equal("https://example.com/dest", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestRedirectLink_InterstitialMode_200() {
	link := newTestLink()
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouter(false)

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/" + testLinkID)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "https://example.com/dest")
	s.Contains(body, "Redirecting")
}

// TestRedirectLink_AnonymousLink_DefaultConfig_Interstitial pins the open-redirect hardening: with
// the default configuration (--direct-redirect off) a link created anonymously is shown behind the
// transparency interstitial, even when its creator asked for direct_redirect=true.
func (s *HandlerSuite) TestRedirectLink_AnonymousLink_DefaultConfig_Interstitial() {
	forceDirect := true
	link := newTestLink()
	link.DirectRedirect = &forceDirect
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouter(false)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, newLinkClickRequest("/r/"+testLinkID))

	s.Equal(http.StatusOK, w.Code)
	s.Empty(w.Header().Get("Location"))
	s.Contains(w.Body.String(), testLinkTargetURL)
}

// TestRedirectLink_OwnedLink_DefaultConfig_Direct: a link created by an authenticated owner is
// accountable, so it keeps the immediate 301 without any operator opt-in.
func (s *HandlerSuite) TestRedirectLink_OwnedLink_DefaultConfig_Direct() {
	ownerID := uuid.New()
	link := newTestLink()
	link.OwnerID = &ownerID
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(link, nil)

	router := s.newLinkRouter(false)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, newLinkClickRequest("/r/"+testLinkID))

	s.Equal(http.StatusMovedPermanently, w.Code)
	s.Equal(testLinkTargetURL, w.Header().Get("Location"))
}

// TestRedirectLink_AnonymousLink_FlagOn_Direct: an operator who sets --direct-redirect explicitly
// gets the old behaviour back for every link, anonymous ones included.
func (s *HandlerSuite) TestRedirectLink_AnonymousLink_FlagOn_Direct() {
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(newTestLink(), nil)

	router := s.newLinkRouter(true)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, newLinkClickRequest("/r/"+testLinkID))

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
	r := newLinkClickRequest("/r/" + testLinkID)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
}

func (s *HandlerSuite) TestRedirectLink_Expired() {
	s.linkManager.EXPECT().Redirect(gomock.Any(), testLinkID).Return(nil, domain.ErrExpired)

	router := s.newLinkRouter(true)

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/" + testLinkID)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
}

func (s *HandlerSuite) TestRedirectLink_NotFound() {
	s.linkManager.EXPECT().Redirect(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	router := s.newLinkRouter(true)

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/missing")

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
	router.ServeHTTP(first, newLinkClickRequest("/r/"+testLinkID))
	s.Equal(http.StatusMovedPermanently, first.Code)

	second := httptest.NewRecorder()
	router.ServeHTTP(second, newLinkClickRequest("/r/"+testLinkID))
	s.Equal(http.StatusNotFound, second.Code)
}

// TestRedirectLink_OwnerOnly_NonOwner_HTML_Forbidden mirrors the note-side IDOR regression
// (TestOwnerOnlyNote_OtherAuthenticatedUser_Forbidden): a privacy: owner link must deny a
// different authenticated caller, not just anonymous ones, and — being a browser route — answer
// with a page rather than a bare 401. 403 rather than the /login redirect, because this caller is
// already signed in and logging in again would land them right back here.
func (s *HandlerSuite) TestRedirectLink_OwnerOnly_NonOwner_HTML_Forbidden() {
	other := &domain.User{
		ID: uuid.New(), Username: "bob",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, links.FollowOptions{
		CallerID: other.ID, Authenticated: true, AllowBurn: true,
	}).Return(nil, domain.ErrForbidden)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(other, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/" + testLinkID)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
	s.Empty(w.Header().Get("Location"), "a settled 403 must not bounce the caller anywhere")
	s.NotContains(w.Body.String(), testLinkTargetURL,
		"the link's target must never reach a non-owner's response body")
}

// TestRedirectLink_OwnerOnly_Owner_Authorized is the positive counterpart: the link's own owner
// may always follow it.
func (s *HandlerSuite) TestRedirectLink_OwnerOnly_Owner_Authorized() {
	owner := &domain.User{
		ID: uuid.New(), Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyOwner)
	link.OwnerID = &owner.ID

	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, links.FollowOptions{
		CallerID: owner.ID, Authenticated: true, AllowBurn: true,
	}).Return(link, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(owner, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/" + testLinkID)
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))

	router.ServeHTTP(w, r)

	s.Equal(http.StatusMovedPermanently, w.Code)
}

// TestRedirectLink_OwnerOnly_Anonymous_Unauthorized mirrors TestOwnerOnlyNote_Anonymous_
// Unauthorized: a privacy: owner link denies an anonymous (no-credential) caller, distinctly
// from denying a different authenticated caller — completes the privacy × caller matrix alongside
// TestRedirectLink_OwnerOnly_NonOwner_HTML_Forbidden and _Owner_Authorized above.
func (s *HandlerSuite) TestRedirectLink_OwnerOnly_Anonymous_Unauthorized() {
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, links.FollowOptions{
		CallerID: uuid.Nil, Authenticated: false, AllowBurn: true,
	}).Return(nil, domain.ErrForbidden)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/" + testLinkID)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// TestRedirectLink_OwnerOnly_Anonymous_HTML_RedirectToLogin is the HTML-negotiated counterpart to
// the anonymous case above — mirrors TestOwnerOnlyNote_Anonymous_HTML_RedirectToLogin.
func (s *HandlerSuite) TestRedirectLink_OwnerOnly_Anonymous_HTML_RedirectToLogin() {
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, links.FollowOptions{
		CallerID: uuid.Nil, Authenticated: false, AllowBurn: true,
	}).Return(nil, domain.ErrForbidden)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/" + testLinkID)
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
	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, links.FollowOptions{
		CallerID: uuid.Nil, Authenticated: false, AllowBurn: true,
	}).Return(nil, domain.ErrForbidden)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/" + testLinkID)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="))
}

// TestRedirectLink_PrivacyAuthenticated_Authenticated mirrors TestPrivateNote_TOTPMode_
// Authenticated: any valid session — not just the link's owner — may follow a privacy:
// authenticated link.
func (s *HandlerSuite) TestRedirectLink_PrivacyAuthenticated_Authenticated() {
	usr := &domain.User{
		ID: uuid.New(), Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyAuthenticated)

	s.linkManager.EXPECT().Follow(gomock.Any(), testLinkID, links.FollowOptions{
		CallerID: usr.ID, Authenticated: true, AllowBurn: true,
	}).Return(link, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := newLinkClickRequest("/r/" + testLinkID)
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))

	router.ServeHTTP(w, r)

	s.Equal(http.StatusMovedPermanently, w.Code)
}

// TestGetLink_PrivacyAuthenticated_Authenticated confirms OgenHandler.GetLink's own privacy gate
// (added as a bug fix in the HTTP handlers section) also grants access to any authenticated
// caller for privacy: authenticated, not just the link's owner.
func (s *HandlerSuite) TestGetLink_PrivacyAuthenticated_Authenticated() {
	usr := &domain.User{
		ID: uuid.New(), Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	link := newTestLink()
	link.Privacy = new(domain.PrivacyAuthenticated)

	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil).Times(1)

	router := s.newLinkRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/links/"+testLinkID, nil)
	r.AddCookie(httptestutil.RequestCookie("padmark_session", "valid-sess"))

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
