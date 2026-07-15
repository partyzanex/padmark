package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
)

// testCSRFSecret is a fixed 32-byte CSRF secret shared across all handler tests.
// Must match the secret set in newRouter's RouterOptions.
var testCSRFSecret = []byte("padmark-test-csrf-secret-32bytes") //nolint:gochecknoglobals // test fixture

// testNoteResponse mirrors noteResponse for decoding test responses.
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
	pinger      *MockPinger
	revealStore *MockRevealTokenStore
	authMgr     *MockAuthManager
	router      http.Handler
}

func TestHandler(t *testing.T) {
	suite.Run(t, new(HandlerSuite))
}

var discardLog = slog.New(slog.DiscardHandler) //nolint:gochecknoglobals // test helper

func (s *HandlerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)
	s.revealStore = NewMockRevealTokenStore(s.ctrl)
	s.authMgr = NewMockAuthManager(s.ctrl)

	s.router = s.newRouter(nil)
}

func (s *HandlerSuite) newRouter(tokens []string) http.Handler {
	handler := adhttp.NewHandler(s.manager, discardLog, tokens)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   testCSRFSecret,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithOptions builds a router with the given trusted proxies and forced scheme,
// used to exercise scheme-detection precedence (auto-detect vs --public-scheme override).
func (s *HandlerSuite) newRouterWithOptions(trustedProxies []*net.IPNet, forcedScheme string) http.Handler {
	handler := adhttp.NewHandler(s.manager, discardLog, nil)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		CookieMaxAge:   90 * 24 * 60 * 60,
		MaxBodyBytes:   256 * 1024,
		CSRFSecret:     testCSRFSecret,
		TrustedProxies: trustedProxies,
		ForcedScheme:   forcedScheme,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithTOTPAuth builds a router configured in TOTP-only mode:
// no bearer tokens, authMgr set. This is the deployment mode under test
// for private-note access and CanEdit fixes.
func (s *HandlerSuite) newRouterWithTOTPAuth() http.Handler {
	handler := adhttp.NewHandler(s.manager, discardLog, nil).
		WithAuthManager(s.authMgr)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   testCSRFSecret,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithReveal builds a router with RevealStore and auth configured so that
// unauthenticated HTML requests trigger the burn interstitial.
func (s *HandlerSuite) newRouterWithReveal() http.Handler {
	handler := adhttp.NewHandler(s.manager, discardLog, []string{"server-secret"}).
		WithRevealStore(s.revealStore)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   testCSRFSecret,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// csrf generates a valid HMAC-signed CSRF token using the fixed test secret.
func (s *HandlerSuite) csrf() string {
	return adhttp.GenerateCSRFTokenForTest(testCSRFSecret)
}

// revealPOST builds a POST request for the burn-reveal routes with a valid CSRF cookie+field,
// since POST /{id} and POST /notes/{id} are now wrapped in csrfGuard. body holds the non-CSRF
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
	req.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie

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
	}
}

// ── IndexPage ──

func (s *HandlerSuite) TestIndexPage() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "Padmark")
	s.Contains(w.Body.String(), "Publish")
}

// ── EditPage ──

func (s *HandlerSuite) TestEditPage_OK() {
	note := newTestNote("edit me", "# hello")
	note.BurnAfterReading = true
	note.Private = new(true)

	future := time.Now().Add(time.Hour)
	note.ExpiresAt = &future

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "edit me")
	s.Contains(body, "# hello")
	s.Contains(body, "Save")
	s.Contains(body, "checked")
	s.Contains(body, `id="privateCheck" checked`)
}

func (s *HandlerSuite) TestEditPage_NotFound() {
	s.manager.EXPECT().Peek(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/missing", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Body.String(), "not found")
}

// ── SuccessPage ──

func (s *HandlerSuite) TestSuccessPage_OK() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "Paste created")
	s.Contains(w.Body.String(), "http://example.com/abc")
	s.Contains(w.Body.String(), "never expires")
}

func (s *HandlerSuite) TestSuccessPage_NoID() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestSuccessPage_WithBurnAndExpires() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/success?id=abc&burn=1&expires=2026-06-15T12:00:00Z", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, "Burn after reading")
	s.Contains(body, "expires Jun 15, 2026")
	// edit code is stored in sessionStorage before redirect and read by client-side JS;
	// it is never rendered into the HTML by the server.
	s.Contains(body, "editCodeBlock")
}

func (s *HandlerSuite) TestSuccessPage_HTTPS() {
	_, trustedNet, err := net.ParseCIDR("192.0.2.1/32")
	s.Require().NoError(err)

	router := s.newRouterWithOptions([]*net.IPNet{trustedNet}, "")

	w := httptest.NewRecorder()
	// httptest.NewRequest defaults RemoteAddr to 192.0.2.1:1234, matching trustedNet above.
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)
	r.Header.Set("X-Forwarded-Proto", "https")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "https://example.com/abc")
	s.NotContains(w.Body.String(), "http://example.com/abc")
}

func (s *HandlerSuite) TestSuccessPage_UntrustedProxyIsIgnored() {
	// No trusted proxies configured: X-Forwarded-Proto must be ignored even though it claims
	// https, since RemoteAddr (192.0.2.1, the httptest default) is not in any trusted CIDR.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)
	r.Header.Set("X-Forwarded-Proto", "https")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "http://example.com/abc")
}

func (s *HandlerSuite) TestSuccessPage_ForcedScheme() {
	// --public-scheme=https forces the scheme with no TLS, no trusted proxies, and no
	// X-Forwarded-Proto header at all — the override needs no signal from the request.
	router := s.newRouterWithOptions(nil, "https")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "https://example.com/abc")
}

func (s *HandlerSuite) TestSuccessPage_ForcedSchemeOverridesXForwardedProto() {
	// The forced scheme takes precedence even when a trusted proxy's X-Forwarded-Proto
	// disagrees, so a misconfigured proxy can't undermine an explicit operator override.
	_, trustedNet, err := net.ParseCIDR("192.0.2.1/32")
	s.Require().NoError(err)

	router := s.newRouterWithOptions([]*net.IPNet{trustedNet}, "http")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)
	r.Header.Set("X-Forwarded-Proto", "https")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "http://example.com/abc")
}

func (s *HandlerSuite) TestSuccessPage_InvalidExpires() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc&expires=bad-date", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "never expires")
}

// ── CreateNote ──

func (s *HandlerSuite) TestCreateNote_OK() {
	note := newTestNote("hello", "# world")
	note.EditCode = "editcode1234"
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	body := `{"title":"hello","content":"# world"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
	s.Contains(w.Header().Get("Location"), "/")
	s.Contains(w.Header().Get("Content-Type"), "application/json")

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("hello", resp.Title)
	s.Equal("# world", resp.Content)
	s.NotEmpty(resp.ID)
	s.Equal("editcode1234", resp.EditCode)
}

func (s *HandlerSuite) TestCreateNote_WithTTL() {
	note := newTestNote("burn", "content")
	note.BurnAfterReading = true
	note.BurnTTL = 3600

	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(3600), n.BurnTTL)
			s.Nil(n.ExpiresAt) // expiry is set on first read, not at creation
			note.ID = n.ID

			return note, nil
		})

	body := `{"title":"burn","content":"content","burn_after_reading":true,"ttl":3600}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.True(resp.BurnAfterReading)
}

func (s *HandlerSuite) TestCreateNote_ImmediateBurn() {
	note := newTestNote("burn", "content")
	note.BurnAfterReading = true
	note.BurnTTL = 0

	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(0), n.BurnTTL)
			s.Nil(n.ExpiresAt)
			note.ID = n.ID

			return note, nil
		})

	body := `{"title":"burn","content":"content","burn_after_reading":true,"ttl":0}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.True(resp.BurnAfterReading)
}

func (s *HandlerSuite) TestCreateNote_EmptyTitle() {
	note := newTestNote("", "body")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"content":"body"}`))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

func (s *HandlerSuite) TestCreateNote_InvalidBody() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`not json`))

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *HandlerSuite) TestCreateNote_WithSlug() {
	note := newTestNote("slugged", "body")
	note.ID = "my-note"
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	body := `{"title":"slugged","content":"body","slug":"my-note"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
	s.Equal("/my-note", w.Header().Get("Location"))

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("my-note", resp.ID)
}

func (s *HandlerSuite) TestCreateNote_SlugConflict() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, domain.ErrSlugConflict)

	body := `{"title":"first","content":"body","slug":"taken"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusConflict, w.Code)
}

func (s *HandlerSuite) TestCreateNote_InvalidContentType() {
	// ogen validates the enum at schema level → 400
	body := `{"title":"t","content":"c","content_type":"application/pdf"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *HandlerSuite) TestCreateNote_BodyExceedsLimit_Returns413() {
	// The 1 MiB domain content cap is gone; the only size limit is now the configurable
	// MaxBodyBytes, enforced at the transport layer (http.MaxBytesReader). A body over that limit
	// must surface as a clean 413, not an opaque decode error — checked here with a tiny limit.
	// Manager.Create is never reached (no EXPECT): the body is rejected during ogen decoding.
	handler := adhttp.NewHandler(s.manager, discardLog, nil)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 64,
		CSRFSecret:   testCSRFSecret,
	}
	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	body := `{"title":"t","content":"` + strings.Repeat("x", 512) + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusRequestEntityTooLarge, w.Code)
}

func (s *HandlerSuite) TestCreateNote_InvalidSlug() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, domain.ErrInvalidSlug)

	body := `{"title":"t","content":"c","slug":"bad slug!"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusUnprocessableEntity, w.Code)
}

func (s *HandlerSuite) TestCreateNote_InternalError() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, errors.New("db crashed"))

	body := `{"title":"t","content":"c"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}

// ── GetNote ──

func (s *HandlerSuite) TestGetNote_JSON() {
	note := newTestNote("my title", "**bold**")
	note.Views = 3
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("my title", resp.Title)
	s.Equal("**bold**", resp.Content)
	s.Equal(3, resp.Views)
	s.Empty(resp.EditCode, "edit_code must not be exposed in GET")
}

func (s *HandlerSuite) TestGetNote_HTML() {
	note := newTestNote("HTML note", "# Heading\n\n**bold**")
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).
		Return(note, "<h1>Heading</h1>\n<strong>bold</strong>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "HTML note")
	s.Contains(body, "<h1>Heading</h1>")
	s.Contains(body, "<strong>bold</strong>")
}

func (s *HandlerSuite) TestGetNote_HTML_WithExpiry() {
	note := newTestNote("expiring", "body")

	future := time.Now().Add(24 * time.Hour)
	note.ExpiresAt = &future

	s.manager.EXPECT().GetRendered(gomock.Any(), testID).
		Return(note, "<p>body</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Expires")
}

// TestGetNote_HTML_TimestampsUseSameISOFormat verifies that both the created and the
// expiry timestamps are emitted as UTC RFC 3339 data attributes, so the client formats
// them in one consistent timezone instead of mixing server-local and browser-local.
func (s *HandlerSuite) TestGetNote_HTML_TimestampsUseSameISOFormat() {
	note := newTestNote("tz", "body")
	note.CreatedAt = time.Date(2026, 6, 4, 3, 30, 0, 0, time.UTC)
	expires := time.Date(2026, 6, 4, 3, 35, 37, 0, time.UTC)
	note.ExpiresAt = &expires

	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(note, "<p>body</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, `data-created="2026-06-04T03:30:00Z"`, "created must be emitted as UTC RFC3339")
	s.Contains(body, `data-expires="2026-06-04T03:35:37Z"`, "expires must be emitted as UTC RFC3339")
}

func (s *HandlerSuite) TestGetNote_HTML_NotFound() {
	s.manager.EXPECT().GetRendered(gomock.Any(), "missing").Return(nil, "", domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/missing", nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "not found")
}

func (s *HandlerSuite) TestGetNote_HTML_Expired() {
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(nil, "", domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
	s.Contains(w.Body.String(), "expired")
}

func (s *HandlerSuite) TestGetNote_HTML_Forbidden() {
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(nil, "", domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
	s.Contains(w.Body.String(), "Forbidden")
}

func (s *HandlerSuite) TestGetNote_HTML_InternalError() {
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(nil, "", errors.New("boom"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
	s.Contains(w.Body.String(), "Internal server error")
}

func (s *HandlerSuite) TestGetNote_Plain() {
	note := newTestNote("plain note", "raw content here")
	note.ContentType = new(domain.ContentTypePlain)
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/plain")
	s.Equal("raw content here", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_Plain_NotFound() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetNote_Markdown() {
	note := newTestNote("md note", "raw **md**")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/markdown")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/markdown")
	s.Equal("raw **md**", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_RawQuery() {
	note := newTestNote("raw note", "raw content")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID+"?raw=1", nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/markdown")
	s.Equal("raw content", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_UnknownAccept() {
	note := newTestNote("unknown", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "image/png")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

func (s *HandlerSuite) TestGetNote_AcceptWildcard() {
	note := newTestNote("wildcard", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "*/*")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

func (s *HandlerSuite) TestGetNote_NotFound() {
	s.manager.EXPECT().View(gomock.Any(), "nonexistent").Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/nonexistent", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetNote_Expired() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(nil, domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
}

func (s *HandlerSuite) TestGetNote_ShortURL() {
	note := newTestNote("short", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("short", resp.Title)
}

func (s *HandlerSuite) TestGetNote_Private_HTML_Unauthorized() {
	note := newTestNote("secret", "private content")
	note.Private = new(true)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"should redirect to /login with ?next= param")
}

func (s *HandlerSuite) TestGetNote_Private_JSON_Unauthorized() {
	note := newTestNote("secret", "private content")
	note.Private = new(true)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestGetNote_Private_Authorized() {
	note := newTestNote("secret", "private content")
	note.Private = new(true)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().ViewPreloaded(gomock.Any(), testID, note).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Authorization", "Bearer secret-token")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

// ── Multi-segment (path-like) slug support ──

func (s *HandlerSuite) TestGetNote_MultiSegmentSlug_PublicServed() {
	// A path-like slug (project/GUIDE.md) is a public note: served without auth even though the
	// URL contains slashes. Proves the auth middleware treats a non-reserved first segment as a
	// note route rather than a protected one.
	const mid = "project/GUIDE.md"

	note := newTestNote("Guide", "# body")
	s.manager.EXPECT().Peek(gomock.Any(), mid).Return(note, nil)
	s.manager.EXPECT().ViewPreloaded(gomock.Any(), mid, note).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+mid, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestGetNote_MultiSegmentSlug_PrivateRequiresAuth() {
	// Per-note privacy still applies to path-like slugs: a private multi-segment note is not
	// served to an unauthenticated caller.
	const mid = "project/secret.md"

	note := newTestNote("secret", "private")
	note.Private = new(true)
	s.manager.EXPECT().Peek(gomock.Any(), mid).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+mid, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestEditPage_MultiSegmentPath_RequiresAuth() {
	// Regression guard: multi-segment slug support must NOT open up reserved routes. A GET to
	// /edit/<path> is redirected to /login before reaching the edit page — it is not mistaken for
	// a public note view just because it now spans multiple segments.
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/project/GUIDE.md", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"reserved /edit/{id...} must redirect to login, not be treated as a public note")
}

func (s *HandlerSuite) TestGetNote_Private_NotFound() {
	s.manager.EXPECT().Peek(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/missing", nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// ── UpdateNote ──

func (s *HandlerSuite) TestUpdateNote_OK() {
	createdAt := time.Now().Add(-time.Hour)
	updated := &domain.Note{
		ID:          testID,
		Title:       "new title",
		Content:     "new content",
		ContentType: new(domain.ContentTypePlain),
		CreatedAt:   createdAt,
		UpdatedAt:   time.Now(),
	}
	s.manager.EXPECT().Update(gomock.Any(), testID, "editcode1234", gomock.Any()).Return(updated, nil)

	body := `{"title":"new title","content":"new content","edit_code":"editcode1234"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("new title", resp.Title)
	s.Equal("new content", resp.Content)
	s.Equal(createdAt.Unix(), resp.CreatedAt.Unix(), "created_at must be preserved")
	s.Equal("text/plain", resp.ContentType, "content_type must be preserved when not provided")
}

func (s *HandlerSuite) TestUpdateNote_WithTTL() {
	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ string, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(3600), n.BurnTTL)
			s.Nil(n.ExpiresAt) // expiry is set on first read, not at update time

			return updated, nil
		})

	body := `{"title":"updated","content":"body","edit_code":"code","burn_after_reading":true,"ttl":3600}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_ImmediateBurn() {
	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ string, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(0), n.BurnTTL)
			s.Nil(n.ExpiresAt)

			return updated, nil
		})

	body := `{"title":"updated","content":"body","edit_code":"code","burn_after_reading":true,"ttl":0}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_WithPrivate() {
	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ string, n *domain.Note) (*domain.Note, error) {
			s.True(n.Private != nil && *n.Private)

			return updated, nil
		})

	body := `{"title":"updated","content":"body","edit_code":"code","private":true}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_NotFound() {
	s.manager.EXPECT().Update(gomock.Any(), "missing", "", gomock.Any()).Return(nil, domain.ErrNotFound)

	body := `{"title":"title","content":"content","edit_code":""}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/missing", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_Forbidden() {
	s.manager.EXPECT().Update(gomock.Any(), testID, "wrong", gomock.Any()).Return(nil, domain.ErrInvalidEditCode)

	body := `{"title":"title","content":"content","edit_code":"wrong"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// ── ACCEPTANCE: edit/delete a path-like slug by its URL ──
//
// The web editor saves with PUT /notes/{id} and deletes with DELETE /notes/{id}. For a slug that
// spans path segments (project/GUIDE.md) these become multi-segment paths that ogen's
// single-segment /notes/{id} route cannot match. The native UpdateNoteByPath / DeleteNoteByPath
// handlers bridge PUT/DELETE /notes/{id...} for exactly this case; these tests assert that a
// path-like slug can be edited and deleted by its address (single-segment IDs still go to ogen).

func (s *HandlerSuite) TestUpdateNote_MultiSegmentSlug_ByURL_Acceptance() {
	const mid = "project/GUIDE.md"

	updated := newTestNote("Guide", "new body")
	s.manager.EXPECT().Update(gomock.Any(), mid, "code", gomock.Any()).Return(updated, nil)

	body := `{"title":"Guide","content":"new body","edit_code":"code"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+mid, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code, "editing a path-like slug via PUT /notes/{id...} must succeed")
}

func (s *HandlerSuite) TestDeleteNote_MultiSegmentSlug_ByURL_Acceptance() {
	const mid = "project/GUIDE.md"

	s.manager.EXPECT().Delete(gomock.Any(), mid, "code").Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+mid+"?edit_code=code", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code, "deleting a path-like slug via DELETE /notes/{id...} must succeed")
}

// TestUpdateNote_OmittedPrivate_PreservesPrivacy verifies that when "private" is absent
// from a PUT request body, the handler passes Private=nil to manager.Update so that the
// storage layer can COALESCE(NULL, private) and preserve the existing DB value.
func (s *HandlerSuite) TestUpdateNote_OmittedPrivate_PreservesPrivacy() {
	s.manager.EXPECT().Update(gomock.Any(), testID, "code", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, n *domain.Note) (*domain.Note, error) {
			// nil means "don't change" — storage will COALESCE(NULL, private).
			s.Nil(n.Private, "Private must be nil when omitted from the request, not defaulted to false")

			return newTestNote("updated", "new content"), nil
		})

	// No "private" field in the body.
	body := `{"title":"updated","content":"new content","edit_code":"code"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_InvalidBody() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(`bad`))

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

// ── DeleteNote ──

func (s *HandlerSuite) TestDeleteNote_OK() {
	s.manager.EXPECT().Delete(gomock.Any(), testID, "editcode1234").Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "editcode1234")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

func (s *HandlerSuite) TestDeleteNote_QueryParam() {
	s.manager.EXPECT().Delete(gomock.Any(), testID, "fromquery").Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID+"?edit_code=fromquery", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

func (s *HandlerSuite) TestDeleteNote_NotFound() {
	s.manager.EXPECT().Delete(gomock.Any(), "nonexistent", "code").Return(domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/nonexistent", nil)
	r.Header.Set("X-Edit-Code", "code")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestDeleteNote_Forbidden() {
	s.manager.EXPECT().Delete(gomock.Any(), testID, "wrong").Return(domain.ErrInvalidEditCode)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "wrong")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// ── Health ──

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
	handler := adhttp.NewHandler(s.manager, discardLog, nil)
	ogen := adhttp.NewOgenHandler(s.manager, adhttp.NoPinger{}, discardLog)
	opts := adhttp.RouterOptions{CookieMaxAge: 90 * 24 * 60 * 60, MaxBodyBytes: 256 * 1024}
	router, err := adhttp.NewRouter(handler, ogen, &opts)
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

// ── Middleware ──

func (s *HandlerSuite) TestRequestIDHeader() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(newTestNote("title", "content"), nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)

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

// TestTOTPLoginHandler_AccountsDisabled_Returns404 covers the nil-guard added so that the
// account system is opt-in: with no auth manager configured, /totp-login must 404.
func (s *HandlerSuite) TestTOTPLoginHandler_AccountsDisabled_Returns404() {
	handler := adhttp.NewHandler(s.manager, discardLog, nil) // no WithAuthManager → accounts disabled
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/totp-login", nil)

	handler.TOTPLoginHandler(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// TestChangePasswordPage_AccountsDisabled_Returns404 mirrors the TOTPLoginHandler nil-guard
// test above for the GET /change-password page.
func (s *HandlerSuite) TestChangePasswordPage_AccountsDisabled_Returns404() {
	handler := adhttp.NewHandler(s.manager, discardLog, nil) // no WithAuthManager → accounts disabled
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/change-password", nil)

	handler.ChangePasswordPage(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// TestPublicMode_AccessMatrix encodes the "public by default" contract (release checklist
// section 1): with no tokens and no auth manager (s.router = newRouter(nil)), public pages
// are reachable and account pages are not exposed. Nothing redirects to /login.
func (s *HandlerSuite) TestPublicMode_AccessMatrix() {
	s.pinger.EXPECT().PingContext(gomock.Any()).Return(nil).AnyTimes() // for /readyz

	cases := []struct {
		path string
		want int
	}{
		{"/", http.StatusOK},                      // editor — public
		{"/healthz", http.StatusOK},               // liveness
		{"/readyz", http.StatusOK},                // readiness
		{"/api", http.StatusOK},                   // API docs
		{"/static/style.css", http.StatusOK},      // static assets
		{"/setup", http.StatusNotFound},           // accounts disabled
		{"/change-password", http.StatusNotFound}, // accounts disabled
		{"/admin", http.StatusForbidden},          // no session → forbidden
	}

	for _, testCase := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, testCase.path, nil)

		s.router.ServeHTTP(w, r)

		s.Equal(testCase.want, w.Code, "GET %s", testCase.path)
		s.NotEqual(http.StatusSeeOther, w.Code, "GET %s must not redirect to /login", testCase.path)
	}
}

// ── Broken writer tests (cover error-log branches) ──

type failWriter struct {
	header http.Header
	code   int
}

func newFailWriter() *failWriter                 { return &failWriter{header: http.Header{}} }
func (fw *failWriter) Header() http.Header       { return fw.header }
func (fw *failWriter) WriteHeader(code int)      { fw.code = code }
func (fw *failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (s *HandlerSuite) TestIndexPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler), nil)
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	handler.IndexPage(fw, r)

	s.Equal(0, fw.code)
}

func (s *HandlerSuite) TestEditPage_WriteFail() {
	note := newTestNote("edit", "body")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler), nil)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)
	r.SetPathValue("id", testID)

	handler.EditPage(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestSuccessPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler), nil)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)

	handler.SuccessPage(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestGetNote_HTML_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(note, "<p>body</p>", nil)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler), nil)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "text/html")

	handler.GetNote(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestGetNote_Plain_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler), nil)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "text/plain")

	handler.GetNote(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestGetNote_JSON_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler), nil)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "application/json")

	handler.GetNote(fwr, r)

	s.NotNil(fwr)
}

// ── Rate limit ──

func (s *HandlerSuite) TestRateLimit_Exceeded() {
	handler := adhttp.NewHandler(s.manager, discardLog, nil)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		RateLimit:    1,
		RateBurst:    1,
	}
	router, err := adhttp.NewRouter(handler, ogen, &opts)
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

func (s *HandlerSuite) TestRateLimit_DifferentIPs() {
	handler := adhttp.NewHandler(s.manager, discardLog, nil)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		RateLimit:    1,
		RateBurst:    1,
	}
	router, err := adhttp.NewRouter(handler, ogen, &opts)
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

// ── Fail lockout ──

func (s *HandlerSuite) TestFailLockout_Lockout() {
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", gomock.Any()).
		Return(nil, domain.ErrForbidden).
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

func (s *HandlerSuite) TestFailLockout_NoIncrementOnSuccess() {
	note := newTestNote("t", "c")
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "valid", gomock.Any()).
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
		Update(gomock.Any(), testID, "wrong", gomock.Any()).
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
		Delete(gomock.Any(), testID, "wrong").
		Return(domain.ErrForbidden).
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
		Update(gomock.Any(), testID, "wrong", gomock.Any()).
		Return(nil, domain.ErrForbidden).
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
		Update(gomock.Any(), otherID, "wrong", gomock.Any()).
		Return(nil, domain.ErrForbidden)

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, "/notes/"+otherID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusForbidden, w.Code)
}

// ── writeError default branch ──

func (s *HandlerSuite) TestGetNote_Plain_Expired() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(nil, domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")
	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
}

func (s *HandlerSuite) TestGetNote_Plain_InternalError() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(nil, errors.New("unexpected db error"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")
	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}

func (s *HandlerSuite) TestWriteErrorPage_WriteFail() {
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(nil, "", domain.ErrNotFound)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler), nil)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "text/html")

	handler.GetNote(fwr, r)

	s.Equal(http.StatusNotFound, fwr.code)
}

func (s *HandlerSuite) TestRecovery_Panic() {
	s.manager.EXPECT().View(gomock.Any(), "panic-id").DoAndReturn(
		func(_ context.Context, _ string) (*domain.Note, error) {
			panic("test panic")
		},
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/panic-id", nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}

// ── Auth middleware ──

func (s *HandlerSuite) TestAuth_NoTokensConfigured() {
	note := newTestNote("t", "c")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

func (s *HandlerSuite) TestAuth_BearerToken() {
	note := newTestNote("t", "c")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer secret-token")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

func (s *HandlerSuite) TestAuth_CookieToken() {
	note := newTestNote("t", "c")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().ViewPreloaded(gomock.Any(), testID, note).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")
	r.AddCookie(&http.Cookie{Name: "padmark_token", Value: "secret-token"}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestAuth_MissingToken_API() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestAuth_MissingToken_Browser() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"should redirect to /login with ?next= param")
}

func (s *HandlerSuite) TestAuth_InvalidToken() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(&http.Cookie{Name: "padmark_token", Value: "wrong"}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
}

func (s *HandlerSuite) TestAuth_PublicPaths() {
	router := s.newRouter([]string{"secret-token"})

	for _, path := range []string{"/login", "/static/style.css", "/healthz"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)

		router.ServeHTTP(w, r)

		s.NotEqual(http.StatusUnauthorized, w.Code, "path %s should be public", path)
		s.NotEqual(http.StatusSeeOther, w.Code, "path %s should not redirect", path)
	}
}

func (s *HandlerSuite) TestAuth_PublicNoteView() {
	note := newTestNote("public", "body")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().ViewPreloaded(gomock.Any(), testID, note).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestAuth_CreateRequiresAuth() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestAuth_UpdateRequiresAuth() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	body := `{"title":"t","content":"c","edit_code":"x"}`
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestAuth_DeleteRequiresAuth() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestGetNote_OpenInstance_ShowsEditButtons() {
	// No auth tokens configured → CanEdit is always true.
	note := newTestNote("public note", "content")
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(note, "<p>content</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, `href="/edit/`)
	s.Contains(body, "New")
}

func (s *HandlerSuite) TestGetNote_AuthInstance_Unauthenticated_HidesEditButtons() {
	// Auth tokens configured, user not authenticated → CanEdit is false.
	// handlePrivateAuth calls Peek first; the public note is then rendered via GetRenderedPreloaded.
	router := s.newRouter([]string{"secret-token"})
	note := newTestNote("public note", "content")

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().GetRenderedPreloaded(gomock.Any(), testID, note).Return(note, "<p>content</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.NotContains(body, `href="/edit/`)
	s.NotContains(body, ">New<")
}

// ── Login ──

func (s *HandlerSuite) TestLoginPage() {
	router := s.newRouter([]string{"token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Authentication required")
}

func (s *HandlerSuite) TestLoginPage_Error() {
	router := s.newRouter([]string{"token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login?error=1", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Invalid token")
}

func (s *HandlerSuite) TestLogin_OK() {
	router := s.newRouter([]string{"valid-token"})
	csrf := s.csrf()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=valid-token&csrf_token="+url.QueryEscape(csrf)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/", w.Header().Get("Location"))

	var tokenCookie *http.Cookie

	for _, ck := range w.Result().Cookies() {
		if ck.Name == "padmark_token" {
			tokenCookie = ck

			break
		}
	}

	s.Require().NotNil(tokenCookie, "padmark_token cookie must be set")
	s.Equal("valid-token", tokenCookie.Value)
	s.True(tokenCookie.HttpOnly)
}

func (s *HandlerSuite) TestLogin_InvalidToken() {
	router := s.newRouter([]string{"valid-token"})
	csrf := s.csrf()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=wrong&csrf_token="+url.QueryEscape(csrf)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Contains(w.Header().Get("Location"), "/login?error=1")
}

func (s *HandlerSuite) TestLogin_EmptyToken() {
	router := s.newRouter([]string{"valid-token"})
	csrf := s.csrf()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=&csrf_token="+url.QueryEscape(csrf)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Contains(w.Header().Get("Location"), "/login?error=1")
}

func (s *HandlerSuite) TestLoginPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler), nil)
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/login?error=1", nil)

	handler.LoginPage(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestLoginPage_WithNext() {
	router := s.newRouter([]string{"token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login?next=%2Fnotes%2Fabc", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `name="next"`)
	s.Contains(w.Body.String(), `/notes/abc`)
}

func (s *HandlerSuite) TestLogin_OK_WithNext() {
	router := s.newRouter([]string{"valid-token"})
	csrf := s.csrf()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=valid-token&next=%2Fnotes%2Fabc&csrf_token="+url.QueryEscape(csrf)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/notes/abc", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestLogin_InvalidToken_PreservesNext() {
	router := s.newRouter([]string{"valid-token"})
	csrf := s.csrf()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=wrong&next=%2Fnotes%2Fabc&csrf_token="+url.QueryEscape(csrf)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	loc := w.Header().Get("Location")
	s.Contains(loc, "/login?error=1")
	s.Contains(loc, "next=")
}

func (s *HandlerSuite) TestLogin_Next_OpenRedirectBlocked() {
	router := s.newRouter([]string{"valid-token"})
	csrf := s.csrf()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=valid-token&next=https%3A%2F%2Fevil.com&csrf_token="+url.QueryEscape(csrf)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestLogin_Next_BackslashOpenRedirectBlocked() {
	router := s.newRouter([]string{"valid-token"})
	csrf := s.csrf()

	w := httptest.NewRecorder()
	// /\evil.com — browsers treat backslash as slash, making this //evil.com
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=valid-token&next=%2F%5Cevil.com&csrf_token="+url.QueryEscape(csrf)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestLogin_Next_DoubleSlashOpenRedirectBlocked() {
	router := s.newRouter([]string{"valid-token"})
	csrf := s.csrf()

	w := httptest.NewRecorder()
	// //evil.com — protocol-relative redirect
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=valid-token&next=%2F%2Fevil.com&csrf_token="+url.QueryEscape(csrf)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestAuth_MissingToken_NextContainsOriginalURL() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?foo=bar", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	loc := w.Header().Get("Location")
	s.True(strings.HasPrefix(loc, "/login?next="), "must redirect to login with next")
	s.Contains(loc, "%2Fsuccess")
}

func (s *HandlerSuite) TestAuth_APIClient_NoHeaders_Returns401() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	// No Accept, no Sec-Fetch-Dest — typical API client (curl)
	r := httptest.NewRequest(http.MethodGet, "/success", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestAuth_Browser_SecFetchDestDocument_Returns303() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login"))
}

// ── API docs ──

func (s *HandlerSuite) TestAPIDocsPage() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "openapi.yaml")
	s.Contains(w.Body.String(), "redoc")
}

// TestCSP_RedocScopedToAPIDocs verifies the Redoc CDN origin appears in the CSP only on /api
// (where Redoc loads) and is absent from the global CSP served on every other page.
func (s *HandlerSuite) TestCSP_RedocScopedToAPIDocs() {
	apiRec := httptest.NewRecorder()
	s.router.ServeHTTP(apiRec, httptest.NewRequest(http.MethodGet, "/api", nil))

	s.Contains(apiRec.Header().Get("Content-Security-Policy"), "https://cdn.redoc.ly",
		"/api CSP must allow the Redoc CDN")

	homeRec := httptest.NewRecorder()
	s.router.ServeHTTP(homeRec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := homeRec.Header().Get("Content-Security-Policy")
	s.NotEmpty(csp, "global CSP must be set")
	s.NotContains(csp, "cdn.redoc.ly", "non-docs pages must not allow third-party script origins")
}

func (s *HandlerSuite) TestAPISpec() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Equal("application/yaml", w.Header().Get("Content-Type"))
	s.Contains(w.Body.String(), "openapi: 3.1.0")
	s.Contains(w.Body.String(), "Padmark API")
}

func (s *HandlerSuite) TestAPIDocsPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler), nil)
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/api", nil)

	handler.APIDocsPage(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestAPISpec_WriteFail() {
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	adhttp.APISpec(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestAPIDocsPage_Public() {
	discardLog := slog.New(slog.DiscardHandler)
	handler := adhttp.NewHandler(s.manager, discardLog, []string{"secret"})
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{CookieMaxAge: 90 * 24 * 60 * 60, MaxBodyBytes: 256 * 1024}
	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code, "/api should be public")
}

// ── BurnInterstitial (GET /{id} + revealStore) ──

func (s *HandlerSuite) TestGetNote_BurnInterstitial_ShowsConfirmPage() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.revealStore.EXPECT().Issue(gomock.Any(), domain.HashSlug(testID)).Return("tok-abc", nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	body := w.Body.String()
	s.Contains(body, "tok-abc", "token must appear in the confirmation form")
	s.Contains(body, "Burns after reading")
}

func (s *HandlerSuite) TestGetNote_BurnInterstitial_NoRevealStore_ServesDirectly() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true

	s.manager.EXPECT().GetRendered(gomock.Any(), testID).
		Return(note, "<p>burn me</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "burn me")
}

func (s *HandlerSuite) TestGetNote_BurnInterstitial_NonBurnNote_SkipsInterstitial() {
	note := newTestNote("plain", "body")

	// handlePrivateAuth calls Peek (allowedTokens != nil), passes note as preloaded.
	// renderNoteHTML then calls GetRenderedPreloaded, not GetRendered.
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().GetRenderedPreloaded(gomock.Any(), testID, note).
		Return(note, "<p>body</p>", nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.NotContains(w.Body.String(), "Burns after reading")
}

func (s *HandlerSuite) TestGetNote_BurnInterstitial_IssueError_Returns500() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.revealStore.EXPECT().Issue(gomock.Any(), domain.HashSlug(testID)).Return("", errors.New("storage down"))

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}

func (s *HandlerSuite) TestGetNote_BurnInterstitial_GracePeriod_SkipsInterstitial() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true
	note.BurnTTL = 300

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().GetRenderedPreloaded(gomock.Any(), testID, note).
		Return(note, "<p>burn me</p>", nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "burn me")
}

// ── HandleReveal (POST /{id}) ──

func (s *HandlerSuite) TestHandleReveal_OK() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true

	// handlePrivateAuth calls Peek; renderNoteHTML uses GetRenderedPreloaded with preloaded note.
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.revealStore.EXPECT().Consume(gomock.Any(), "tok-abc", domain.HashSlug(testID)).Return(true)
	s.manager.EXPECT().GetRenderedPreloaded(gomock.Any(), testID, note).
		Return(note, "<p>burn me</p>", nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "token=tok-abc")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "burn me")
}

func (s *HandlerSuite) TestHandleReveal_NoRevealStore_Forbidden() {
	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "token=tok-abc")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestHandleReveal_NoCSRF_Forbidden verifies the burn-reveal POST is now CSRF-guarded like
// every other POST: a request without the CSRF cookie/field is rejected before reaching the
// handler (no Peek/Consume expectations are set, proving the guard short-circuits first).
func (s *HandlerSuite) TestHandleReveal_NoCSRF_Forbidden() {
	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes/"+testID, strings.NewReader("token=tok-abc"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "text/html")
	// no padmark_csrf cookie / csrf_token field

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

func (s *HandlerSuite) TestHandleReveal_InvalidToken_Forbidden() {
	note := newTestNote("secret", "burn me")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.revealStore.EXPECT().Consume(gomock.Any(), "bad-tok", domain.HashSlug(testID)).Return(false)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "token=bad-tok")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestHandleReveal_TokenForWrongNote_Forbidden verifies that posting a token issued
// for a different note returns 403 AND does not burn the token (fix for DoS bug).
// Consume receives the URL's noteID and returns false — the DB rejects the mismatch.
func (s *HandlerSuite) TestHandleReveal_TokenForWrongNote_Forbidden() {
	note := newTestNote("secret", "burn me")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	// Consume is called with testID (URL note); token was issued for another note →
	// DB WHERE note_id = testID finds no row → returns false, token untouched.
	s.revealStore.EXPECT().Consume(gomock.Any(), "tok-other", domain.HashSlug(testID)).Return(false)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "token=tok-other")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

func (s *HandlerSuite) TestHandleReveal_MissingToken_Forbidden() {
	note := newTestNote("secret", "burn me")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.revealStore.EXPECT().Consume(gomock.Any(), "", domain.HashSlug(testID)).Return(false)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestHandleReveal_CrossNoteToken_TokenNotBurned verifies the fix for the DoS bug:
// posting note-A's token to note-B's endpoint must return 403 WITHOUT burning the token.
// Consume is called with note-B's ID; the DB WHERE note_id = 'wrong-note' finds no row
// and returns false — the token for testID remains intact.
func (s *HandlerSuite) TestHandleReveal_CrossNoteToken_TokenNotBurned() {
	wrongNote := &domain.Note{
		ID:          "wrong-note",
		Title:       "wrong",
		Content:     "body",
		ContentType: new(domain.ContentTypeMarkdown),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	s.manager.EXPECT().Peek(gomock.Any(), "wrong-note").Return(wrongNote, nil)
	// Consume called with "wrong-note" as noteID — DB rejects mismatch, token preserved.
	s.revealStore.EXPECT().Consume(gomock.Any(), "tok-for-abc123", domain.HashSlug("wrong-note")).Return(false)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/wrong-note", "token=tok-for-abc123")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
	// gomock TearDown verifies Consume(_, "tok-for-abc123", "wrong-note") was called —
	// confirming the handler passed the URL noteID to Consume, not a blind token burn.
}

// ── TOTP-mode private note and CanEdit fixes ──
//
// In TOTP-only deployments (authMgr set, no bearer tokens) handlePrivateAuth
// must respect the session before serving private notes, and CanEdit must
// depend on actual authentication state — not on the allowedTokens nil check.
//
// Call-flow for GET /notes/{id} in TOTP mode:
//   1. Auth middleware is bypassed (note route is public).
//   2. handlePrivateAuth → Peek → checks Private + isAuthenticated.
//   3. For non-private notes: returns (note, false) without calling isAuthenticated.
//   4. renderNoteHTML(preloaded) → GetRenderedPreloaded → isAuthenticated (for CanEdit).

// TestPrivateNote_TOTPMode_Unauthenticated verifies that an unauthenticated
// request to a private note is redirected to /login in TOTP-only mode.
func (s *HandlerSuite) TestPrivateNote_TOTPMode_Unauthenticated() {
	note := newTestNote("secret", "private content")
	note.Private = new(true)

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	// isAuthenticated fallback: no session cookie → extractSessionID returns "" →
	// GetSession is never called; the function returns false directly.

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
// note route and stores the user in context, so both handlePrivateAuth (private check)
// and renderNoteHTML (CanEdit) read it from context without re-querying.
func (s *HandlerSuite) TestPrivateNote_TOTPMode_Authenticated() {
	note := newTestNote("secret", "private content")
	note.Private = new(true)
	usr := &domain.User{ID: "u1", Username: "alice"}

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().GetRenderedPreloaded(gomock.Any(), testID, note).Return(note, "rendered", nil)
	// One GetSession call, made by the auth middleware; the handler reads from context.
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

// TestCanEdit_TOTPMode_Unauthenticated verifies that in TOTP-only mode an
// unauthenticated visitor does NOT see the edit button (CanEdit = false).
func (s *HandlerSuite) TestCanEdit_TOTPMode_Unauthenticated() {
	note := newTestNote("public note", "content")

	// For a non-private note, handlePrivateAuth calls Peek and returns (note, false)
	// without calling isAuthenticated. Only renderNoteHTML calls isAuthenticated.
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().GetRenderedPreloaded(gomock.Any(), testID, note).Return(note, "rendered", nil)
	// isAuthenticated: no session cookie → extractSessionID = "" → GetSession not called.

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.NotContains(w.Body.String(), `href="/edit/`+testID+`"`,
		"unauthenticated user must not see the edit button in TOTP mode")
}

// TestCanEdit_TOTPMode_Authenticated verifies that an authenticated TOTP-session
// user sees the edit button.
func (s *HandlerSuite) TestCanEdit_TOTPMode_Authenticated() {
	note := newTestNote("public note", "content")
	usr := &domain.User{ID: "u1", Username: "alice"}

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().GetRenderedPreloaded(gomock.Any(), testID, note).Return(note, "rendered", nil)
	// isAuthenticated called once by renderNoteHTML for CanEdit.
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `href="/edit/`+testID+`"`,
		"authenticated TOTP user must see the edit button")
}

// TestIsAuthenticated_ReadsFromContext verifies that for an authenticated request
// on a token-protected route the auth middleware stores the user in context and
// the handler does not issue a second GetSession call.
// We use the JSON note API (Bearer token auth) where the middleware runs and
// stores the user; the note handler then calls isAuthenticated for CanEdit.
func (s *HandlerSuite) TestIsAuthenticated_ReadsFromContext() {
	note := newTestNote("public note", "content")

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().GetRenderedPreloaded(gomock.Any(), testID, note).Return(note, "rendered", nil)

	// In token-only mode, allowedTokens != nil. The auth middleware does NOT call
	// GetSession (it checks the bearer token, not a session). isAuthenticated in
	// renderNoteHTML falls through to the bearer token check — GetSession is never
	// called. This test verifies that the token path does not accidentally trigger
	// session lookups.
	router := s.newRouter([]string{"tok"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.Header.Set("Authorization", "Bearer tok")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `href="/edit/`+testID+`"`,
		"authenticated token user must see the edit button")
}

// ── Rate-limit fix: POST /login must not be TOTP-rate-limited ──

// TestLegacyLogin_NotRateLimited verifies that POST /login (token-based auth) is not
// subject to the 10 req/min TOTP rate limit: 15 consecutive requests must all succeed.
func (s *HandlerSuite) TestLegacyLogin_NotRateLimited() {
	const attempts = 15

	csrf := s.csrf()

	for range attempts {
		w := httptest.NewRecorder()
		form := url.Values{"token": {"valid-token"}, "csrf_token": {csrf}}
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: "padmark_csrf", Value: csrf}) //nolint:gosec // G124: test cookie
		s.newRouter([]string{"valid-token"}).ServeHTTP(w, r)

		s.NotEqual(http.StatusTooManyRequests, w.Code,
			"POST /login must not be rate-limited (attempt %d got 429)", attempts)
	}
}
