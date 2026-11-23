package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
)

// TestAllowedHosts_EnforcedOnMainListener verifies RouterOptions.AllowedHosts is wired into the
// middleware chain: a foreign Host is refused with 421 before any handler, a listed one is
// served, and with no list configured every Host passes (the default).
func (s *HandlerSuite) TestAllowedHosts_EnforcedOnMainListener() {
	restricted := s.newRouterWithAllowedHosts("notes.example.com")

	foreign := httptest.NewRecorder()
	foreignReq := httptest.NewRequest(http.MethodGet, "/", nil)
	foreignReq.Host = "evil.test"
	restricted.ServeHTTP(foreign, foreignReq)
	s.Equal(http.StatusMisdirectedRequest, foreign.Code)

	listed := httptest.NewRecorder()
	listedReq := httptest.NewRequest(http.MethodGet, "/", nil)
	listedReq.Host = "Notes.Example.com:8443"
	restricted.ServeHTTP(listed, listedReq)
	s.Equal(http.StatusOK, listed.Code)

	open := httptest.NewRecorder()
	openReq := httptest.NewRequest(http.MethodGet, "/", nil)
	openReq.Host = "evil.test"
	s.router.ServeHTTP(open, openReq)
	s.Equal(http.StatusOK, open.Code, "no allow-list configured: every Host passes")
}

// ── Health ──.

func (s *HandlerSuite) TestHealthz() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestReadyz_OK() {
	s.pinger.EXPECT().PingContext(gomock.Any()).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestReadyz_NoPinger() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, web.NoPinger{})
	handler := newTestHandler(s.T(), handlerCfg)
	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}
	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestReadyz_Error() {
	s.pinger.EXPECT().PingContext(gomock.Any()).Return(errors.New("db down"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusServiceUnavailable, w.Code)
}

// ── Middleware ──.

func (s *HandlerSuite) TestRequestIDHeader() {
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(newTestNote("title", "content"), nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.NotEmpty(w.Header().Get("X-Request-ID"))
}

func (s *HandlerSuite) TestStaticAssets() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/css")
}

// TestStaticAsset_PasswordToggle verifies the show/hide-password script is embedded and served.
func (s *HandlerSuite) TestStaticAsset_PasswordToggle() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/static/password-toggle.js", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "pw-toggle", "toggle script must be served")
}

// TestStaticAsset_AutofillOverride guards the CSS that stops browser autofill from
// overriding the dark-theme field background and text colour.
func (s *HandlerSuite) TestStaticAsset_AutofillOverride() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	body := w.Body.String()
	s.Contains(body, ":-webkit-autofill", "autofill override rule must be present")
	s.Contains(body, "-webkit-text-fill-color", "autofill text colour must be forced")
}

// TestPublicMode_AccessMatrix encodes the "public by default" contract (release checklist
// section 1): with no tokens and no auth manager (s.router = newRouter(nil)), public pages
// are reachable and account pages are not exposed. Nothing redirects to /login.
func (s *HandlerSuite) TestPublicMode_AccessMatrix() {
	s.pinger.EXPECT().PingContext(gomock.Any()).Return(nil).AnyTimes() // for /readyz.

	cases := []struct {
		path string
		want int
	}{
		{"/", http.StatusOK},                      // editor — public.
		{"/healthz", http.StatusOK},               // liveness.
		{"/readyz", http.StatusOK},                // readiness.
		{"/api", http.StatusOK},                   // API docs.
		{"/static/style.css", http.StatusOK},      // static assets.
		{"/setup", http.StatusNotFound},           // accounts disabled.
		{"/change-password", http.StatusNotFound}, // accounts disabled.
		{"/admin", http.StatusForbidden},          // no session → forbidden.
	}

	for _, testCase := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, testCase.path, nil)

		s.router.ServeHTTP(w, r)

		s.Equal(testCase.want, w.Code, "GET %s", testCase.path)
		s.NotEqual(http.StatusSeeOther, w.Code, "GET %s must not redirect to /login", testCase.path)
	}
}

// ── Rate limit ──.

func (s *HandlerSuite) TestRateLimit_Exceeded() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handler := newTestHandler(s.T(), handlerCfg)
	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		RateLimit:    1,
		RateBurst:    1,

		AllowedHosts: nil, DisableAPI: false,
	}
	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	send := func() int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "203.0.113.1:5000"
		router.ServeHTTP(w, r)

		return w.Code
	}

	s.Equal(http.StatusOK, send())
	s.Equal(http.StatusTooManyRequests, send())
}

// TestRateLimit_Exceeded_POSTLinks confirms rate limiting is actually wired to /links, not just
// assumed from it sharing a mux with the already-tested / route: POST /links returns 429.
func (s *HandlerSuite) TestRateLimit_Exceeded_POSTLinks() {
	link := newTestLink()
	s.linkManager.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(link, nil)

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handler := newTestHandler(s.T(), handlerCfg)
	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		RateLimit:    1,
		RateBurst:    1,

		AllowedHosts: nil, DisableAPI: false,
	}
	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	send := func() int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/links",
			strings.NewReader(`{"target_url":"https://example.com"}`))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "203.0.113.9:5000"
		router.ServeHTTP(w, r)

		return w.Code
	}

	s.Equal(http.StatusCreated, send())
	s.Equal(http.StatusTooManyRequests, send())
}

func (s *HandlerSuite) TestRateLimit_DifferentIPs() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handler := newTestHandler(s.T(), handlerCfg)
	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		RateLimit:    1,
		RateBurst:    1,

		AllowedHosts: nil, DisableAPI: false,
	}
	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	send := func(ip string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = ip + ":5000"
		router.ServeHTTP(w, r)

		return w.Code
	}

	s.Equal(http.StatusOK, send("203.0.113.1"))
	s.Equal(http.StatusOK, send("203.0.113.2"))
}

// ── Fail lockout ──.

func (s *HandlerSuite) TestFailLockout_Lockout() {
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", noCaller(), gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode).
		Times(10)

	body := `{"title":"t","content":"c","edit_code":"wrong"}`

	for range 10 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, r)
		s.Equal(http.StatusForbidden, w.Code)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusTooManyRequests, w.Code)
}

// TestFailLockout_ResetsOnSuccess verifies a successful request clears the accumulated failure
// count: an owner who mistypes the edit code a few times before getting it right must not carry
// a stale near-threshold counter into their next legitimate edit.
func (s *HandlerSuite) TestFailLockout_ResetsOnSuccess() {
	note := newTestNote("t", "c")

	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", noCaller(), gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode).
		Times(9)
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "valid", noCaller(), gomock.Any()).
		Return(note, nil)

	wrongBody := `{"title":"t","content":"c","edit_code":"wrong"}`
	validBody := `{"title":"t","content":"c","edit_code":"valid"}`

	// 9 failures — one short of the 10-attempt lockout threshold.
	for range 9 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(wrongBody))
		r.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, r)
		s.Equal(http.StatusForbidden, w.Code)
	}

	// The 10th request succeeds — this must clear the counter, not just avoid incrementing it.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(validBody))
	r.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusOK, w.Code)

	// A further wrong attempt must be treated as failure #1, not #10 — no lockout yet.
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", noCaller(), gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode)

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(wrongBody))
	r.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusForbidden, w.Code, "counter must have reset on the earlier success, not carried toward lockout")
}

func (s *HandlerSuite) TestFailLockout_NoIncrementOnSuccess() {
	note := newTestNote("t", "c")
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "valid", noCaller(), gomock.Any()).
		Return(note, nil).
		Times(11)

	body := `{"title":"t","content":"c","edit_code":"valid"}`

	for range 11 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, r)
		s.Equal(http.StatusOK, w.Code)
	}
}

func (s *HandlerSuite) TestFailLockout_NoIncrementOnNonForbiddenError() {
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", noCaller(), gomock.Any()).
		Return(nil, domain.ErrNotFound).
		Times(11)

	body := `{"title":"t","content":"c","edit_code":"wrong"}`

	for range 11 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, r)
		s.Equal(http.StatusNotFound, w.Code)
	}
}

func (s *HandlerSuite) TestFailLockout_DeleteAlsoLocked() {
	s.manager.EXPECT().
		Delete(gomock.Any(), testID, "wrong", noCaller()).
		Return(domain.ErrInvalidEditCode).
		Times(10)

	for range 10 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
		r.Header.Set("X-Edit-Code", "wrong")
		s.router.ServeHTTP(w, r)
		s.Equal(http.StatusForbidden, w.Code)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "wrong")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusTooManyRequests, w.Code)
}

func (s *HandlerSuite) TestFailLockout_IndependentPerNoteID() {
	body := `{"title":"t","content":"c","edit_code":"wrong"}`

	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", noCaller(), gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode).
		Times(10)

	for range 10 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, r)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusTooManyRequests, w.Code)

	const otherID = "other-note"

	s.manager.EXPECT().
		Update(gomock.Any(), otherID, "wrong", noCaller(), gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode)

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, "/notes/"+otherID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusForbidden, w.Code)
}

func (s *HandlerSuite) TestRecovery_Panic() {
	s.manager.EXPECT().Read(gomock.Any(), "panic-id", gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, _ domain.Reader) (*domain.Note, error) {
			panic("test panic")
		},
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/panic-id", nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}
