package web_test

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// testCSRFSecret is a fixed 32-byte CSRF secret shared across all handler tests.
// Must match the secret set in newRouter's RouterOptions.
var testCSRFSecret = []byte("padmark-test-csrf-secret-32bytes") //nolint:gochecknoglobals // test fixture

// testNoteResponse mirrors the JSON note response (ogenapi.NoteResponse) for decoding test
// responses.
type testNoteResponse struct {
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Content          string     `json:"content"`
	ContentType      string     `json:"content_type"`
	EditCode         string     `json:"edit_code"`
	Views            int        `json:"views"`
	BurnAfterReading bool       `json:"burn_after_reading"`
}

type HandlerSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	manager     *MockNoteManager
	linkManager *MockLinkManager
	pinger      *MockPinger
	revealStore *MockRevealTokenStore
	authMgr     *MockAuthManager
	router      http.Handler
}

func TestHandler(t *testing.T) {
	suite.Run(t, new(HandlerSuite))
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// testHandlerConfig returns the HandlerConfig of most tests: the given managers and pinger, a
// discarding logger, no account system, no files, no reveal store and the default settings. A
// test sets the fields of its scenario on the returned value.
func testHandlerConfig(notes web.NoteManager, links web.LinkManager, pinger web.Pinger) *web.HandlerConfig {
	return &web.HandlerConfig{
		Notes: notes, Links: links, Pinger: pinger, Log: discardLog(),
		Accounts: nil, Files: nil, Reveal: nil,
		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, CSRFSecret: nil, SessionTTL: 0,
		FileTransfer:   web.FileTransferLimits{IdleTimeout: 0, MaxUploads: 0, MaxUploadsPerClient: 0},
		DirectRedirect: false,
	}
}

// newTestHandler builds the Handler of cfg and stops the test when NewHandler fails.
func newTestHandler(t *testing.T, cfg *web.HandlerConfig) *web.Handler {
	t.Helper()

	handler, err := web.NewHandler(cfg)
	require.NoError(t, err)

	return handler
}

func (s *HandlerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)
	s.revealStore = NewMockRevealTokenStore(s.ctrl)
	s.authMgr = NewMockAuthManager(s.ctrl)

	s.router = s.newRouter()
}

func (s *HandlerSuite) newRouter() http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.CSRFSecret = testCSRFSecret
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithOptions builds a router with the given trusted proxies and forced scheme,
// used to exercise scheme-detection precedence (auto-detect vs --public-scheme override).
func (s *HandlerSuite) newRouterWithOptions(trustedProxies []*net.IPNet, forcedScheme string) http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.CSRFSecret = testCSRFSecret
	handlerCfg.TrustedProxies = trustedProxies
	handlerCfg.ForcedScheme = forcedScheme
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithAllowedHosts builds a router whose main listener only serves the given Hosts.
func (s *HandlerSuite) newRouterWithAllowedHosts(hosts ...string) http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.CSRFSecret = testCSRFSecret
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		AllowedHosts: hosts,

		RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithTOTPAuth builds a router configured in TOTP-only mode: HandlerConfig.Accounts is
// s.authMgr.
// This is the deployment mode under test for private-note access and CanEdit fixes, and is
// also the standard way to force "auth is configured" for tests that only need an anonymous
// request to be treated as unauthenticated (no session/API-token credential presented).
func (s *HandlerSuite) newRouterWithTOTPAuth() http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.Accounts = s.authMgr
	handlerCfg.CSRFSecret = testCSRFSecret
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithReveal builds a router with RevealStore and auth configured so that
// unauthenticated HTML requests trigger the burn interstitial.
func (s *HandlerSuite) newRouterWithReveal() http.Handler {
	s.T().Helper()

	return s.newRouterWithRevealLog(discardLog())
}

// newRouterWithRevealLog is newRouterWithReveal with the given logger, for tests that examine
// the log records.
func (s *HandlerSuite) newRouterWithRevealLog(log *slog.Logger) http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.Log = log
	handlerCfg.Accounts = s.authMgr
	handlerCfg.Reveal = s.revealStore
	handlerCfg.CSRFSecret = testCSRFSecret
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

// csrf generates a valid HMAC-signed CSRF token using the fixed test secret.
func (s *HandlerSuite) csrf() string {
	return web.GenerateCSRFTokenForTest(testCSRFSecret)
}

// revealPOST builds a POST request for the burn-reveal routes with a valid CSRF cookie+field,
// since POST /{id} and POST /notes/{id} are now wrapped in CSRFGuard. body holds the non-CSRF
// form fields (may be empty); the matching csrf_token field is appended automatically.
func (s *HandlerSuite) revealPOST(path, body string) *http.Request {
	csrf := s.csrf()

	form := body
	if form != "" {
		form += "&"
	}

	form += "csrf_token=" + url.QueryEscape(csrf)

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.AddCookie(httptestutil.RequestCookie("padmark_csrf", csrf))

	return req
}

func (s *HandlerSuite) TearDownTest() {
	s.ctrl.Finish()
}

const testID = "abc-123"

func newTestNote(title, content string) *domain.Note {
	return &domain.Note{
		ID:          testID,
		Title:       title,
		Content:     content,
		ContentType: new(domain.ContentTypeMarkdown),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	}
}

// newTestUser builds a minimal user fixture: only the identity fields the auth context carries.
func newTestUser(name string) *domain.User {
	usr := new(domain.User)
	usr.ID = uuid.New()
	usr.Username = name

	return usr
}

// sessioned adds a valid session cookie to r for tests that need an authenticated caller; pair
// it with s.authMgr.EXPECT().GetSession(...).Return(user, nil).
func sessioned(r *http.Request) *http.Request {
	cookie := new(http.Cookie) //nolint:gosec // G124: test cookie; Secure/HttpOnly are response-only attrs
	cookie.Name = "padmark_session"
	cookie.Value = "valid-sess"
	r.AddCookie(cookie)

	return r
}

// expectBurnHeld sets the reads of a deliberate browser read of a burn note with a reveal
// store: the first read has no burn intent and stops at the burn note, then Inspect reads the
// note without a side effect.
func (s *HandlerSuite) expectBurnHeld(note *domain.Note) {
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, withBurn(false)).
		Return(nil, "", domain.ErrBurnIntentRequired)
	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)
}
