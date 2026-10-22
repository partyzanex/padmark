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

	"github.com/google/uuid"
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
	linkManager *MockLinkManager
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
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)
	s.revealStore = NewMockRevealTokenStore(s.ctrl)
	s.authMgr = NewMockAuthManager(s.ctrl)

	s.router = s.newRouter()
}

func (s *HandlerSuite) newRouter() http.Handler {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
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
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		MaxBodyBytes:   256 * 1024,
		CSRFSecret:     testCSRFSecret,
		TrustedProxies: trustedProxies,
		ForcedScheme:   forcedScheme,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// newRouterWithTOTPAuth builds a router configured in TOTP-only mode: authMgr set.
// This is the deployment mode under test for private-note access and CanEdit fixes, and is
// also the standard way to force "auth is configured" for tests that only need an anonymous
// request to be treated as unauthenticated (no session/API-token credential presented).
func (s *HandlerSuite) newRouterWithTOTPAuth() http.Handler {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog).
		WithAuthManager(s.authMgr)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
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
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog).
		WithAuthManager(s.authMgr).
		WithRevealStore(s.revealStore)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
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

// expectMutationGate installs the Peek expectation that the anonymous mutation gate performs
// before a mocked PUT/DELETE reaches the usecase: it returns a public (nil privacy) note, so
// the gate lets the request through. The returned ID mirrors the requested one so per-ID
// assertions stay possible.
func (s *HandlerSuite) expectMutationGate() {
	s.manager.EXPECT().Peek(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string) (*domain.Note, error) {
			note := newTestNote("gate", "c") // nil privacy → public
			note.ID = id

			return note, nil
		},
	).AnyTimes()
}

// expectLinkMutationGate is the links counterpart of expectMutationGate.
func (s *HandlerSuite) expectLinkMutationGate() {
	s.linkManager.EXPECT().Peek(gomock.Any(), gomock.Any()).Return(newTestLink(), nil).AnyTimes()
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
	note.Privacy = new(domain.PrivacyAuthenticated)

	future := time.Now().Add(time.Hour)
	note.ExpiresAt = &future

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().EnforceExpiry(gomock.Any(), testID, note).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "edit me")
	s.Contains(body, "# hello")
	s.Contains(body, "Save")
	s.Contains(body, `value="authenticated" selected`)
}

// TestEditPage_BurnNote_ContentWithheld: the editor never burns, so it must not pre-fill a
// burn-after-reading note's content — otherwise /edit/{id} reads it any number of times (H-1).
func (s *HandlerSuite) TestEditPage_BurnNote_ContentWithheld() {
	note := newTestNote("edit me", "# burn-only-once")
	note.BurnAfterReading = true

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().EnforceExpiry(gomock.Any(), testID, note).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, "edit me")
	s.NotContains(body, "burn-only-once")
	s.Contains(body, "Burn-after-reading content is not shown here")
	s.Contains(body, "checked")
}

// TestEditPage_Expired_Gone: Peek ignores expiry, so the editor enforces it itself — an expired
// note is deleted (by the usecase) and answered 410 instead of being served (H-1).
func (s *HandlerSuite) TestEditPage_Expired_Gone() {
	note := newTestNote("old", "# stale-content")

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().EnforceExpiry(gomock.Any(), testID, note).Return(domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
	s.NotContains(w.Body.String(), "stale-content")
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
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
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

// ── ogen decode errors (pre-handler failures) ──

// decodeErrorBody posts body to /notes with the given Content-Type and returns the status and
// the decoded error envelope. It reads into a map rather than a typed struct so an absent
// "message" is visible as an absent key instead of an empty string, which is the exact defect
// these tests guard: ogen's DefaultErrorHandler wrote "error_message" and left "message" unset.
func (s *HandlerSuite) decodeErrorBody(contentType, body string) (int, map[string]any) {
	s.T().Helper()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)

	s.router.ServeHTTP(w, r)

	var decoded map[string]any
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&decoded))

	return w.Code, decoded
}

// TestOgenDecodeErrors_UseErrorResponseShape covers every failure ogen raises before a request
// reaches a handler method. Two things are pinned per case: the body uses the contract's
// ErrorResponse shape (ogen's native {"error_message": ...} declares none of the required
// "message" field), and the message itself is the sanitised one — ogen's raw string chains its
// own decode path and names generated request types, which tells a caller nothing it can act on.
// Manager.Create is never reached in any case (no EXPECT): each request dies during decoding.
func (s *HandlerSuite) TestOgenDecodeErrors_UseErrorResponseShape() {
	cases := map[string]struct {
		contentType string
		body        string
		wantStatus  int
		wantMessage string
	}{
		"malformed json": {
			"application/json", `{"content":`, http.StatusBadRequest,
			"invalid request body",
		},
		// The one case carrying detail worth forwarding: the field name is schema-defined, so
		// naming it helps the caller without exposing anything the spec doesn't already publish.
		"missing required field": {
			"application/json", `{}`, http.StatusBadRequest,
			"invalid request body: content (field required)",
		},
		"wrong field type": {
			"application/json", `{"content": 12345}`, http.StatusBadRequest,
			"invalid request body",
		},
		"unsupported media type": {
			"text/plain", `hello`, http.StatusUnsupportedMediaType,
			"unsupported content type: text/plain",
		},
	}

	for name, want := range cases {
		s.Run(name, func() {
			status, decoded := s.decodeErrorBody(want.contentType, want.body)

			s.Equal(want.wantStatus, status)
			s.Contains(decoded, "message", "ErrorResponse requires a message field")
			s.NotContains(decoded, "error_message", "ogen's native error shape must not leak through")
			s.Equal(want.wantMessage, decoded["message"])
		})
	}
}

// TestOgenDecodeErrors_DoNotLeakInternals is the same surface checked from the other side: no
// reply may name a generated type, an operation ID, or ogen's decode chain, whatever the input.
// Stated separately from the exact-message assertions above so that rewording a message later
// cannot quietly drop this guarantee along with it.
func (s *HandlerSuite) TestOgenDecodeErrors_DoNotLeakInternals() {
	bodies := []string{`{"content":`, `{}`, `{"content": 12345}`, `[]`, `{"content": [1,2]}`}

	for _, body := range bodies {
		_, decoded := s.decodeErrorBody("application/json", body)

		message, _ := decoded["message"].(string)
		s.NotEmpty(message)

		for _, leak := range []string{"CreateNoteRequest", "operation CreateNote", "decode request", "callback"} {
			s.NotContains(message, leak, "internal detail %q leaked for body %s", leak, body)
		}
	}
}

// TestOgenDecodeErrors_LongContentTypeIsTruncated bounds what a caller can make the server echo.
// The Content-Type header is limited only by MaxHeaderBytes, so quoting it back whole would let
// a caller inflate the error response far beyond the request that caused it.
func (s *HandlerSuite) TestOgenDecodeErrors_LongContentTypeIsTruncated() {
	longType := "text/" + strings.Repeat("z", 4096)

	_, decoded := s.decodeErrorBody(longType, `{}`)

	message, _ := decoded["message"].(string)
	s.Less(len(message), 200, "the echoed content type must be capped, got %d bytes", len(message))
	s.Contains(message, "unsupported content type")
}

// TestCreateNote_BodyExceedsLimit_UsesErrorResponseShape is the 413 counterpart: that branch
// already wrote the right shape before the handler was replaced, so this pins it against a
// regression while the surrounding code changes.
func (s *HandlerSuite) TestCreateNote_BodyExceedsLimit_UsesErrorResponseShape() {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
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

	var decoded map[string]any
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&decoded))
	s.Contains(decoded, "message")
	s.NotContains(decoded, "error_message")
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
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().ViewPreloaded(gomock.Any(), testID, note).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "image/png")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

func (s *HandlerSuite) TestGetNote_AcceptWildcard() {
	note := newTestNote("wildcard", "body")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().ViewPreloaded(gomock.Any(), testID, note).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "*/*")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

// TestGetNote_ImplicitBurnRead_Refused: a burn-after-reading note read without explicit intent
// (no Accept, wildcards only, HEAD, an <img> subresource) is answered 409 and never reaches
// View — the note is neither consumed nor its timer started (H-1).
func (s *HandlerSuite) TestGetNote_ImplicitBurnRead_Refused() {
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		burnTTL int64
	}{
		{"no Accept", http.MethodGet, map[string]string{}, 0},
		{"wildcard", http.MethodGet, map[string]string{"Accept": "*/*"}, 0},
		{"partial wildcard", http.MethodGet, map[string]string{"Accept": "text/*"}, 0},
		{"HEAD with explicit Accept", http.MethodHead, map[string]string{"Accept": "application/json"}, 0},
		{"img subresource", http.MethodGet, map[string]string{
			"Accept": "image/avif,image/webp,*/*;q=0.8", "Sec-Fetch-Dest": "image",
		}, 0},
		{"iframe with text/html", http.MethodGet, map[string]string{
			"Accept": "text/html", "Sec-Fetch-Dest": "iframe",
		}, 0},
		{"grace-period timer not started", http.MethodGet, map[string]string{"Accept": "*/*"}, 3600},
	}

	for _, testCase := range cases {
		s.Run(testCase.name, func() {
			note := newTestNote("burn", "burn-body")
			note.BurnAfterReading = true
			note.BurnTTL = testCase.burnTTL

			s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(testCase.method, "/"+testID, nil)

			for name, value := range testCase.headers {
				r.Header.Set(name, value)
			}

			s.router.ServeHTTP(w, r)

			s.Equal(http.StatusConflict, w.Code)
			s.NotContains(w.Body.String(), "burn-body")

			if testCase.method == http.MethodGet {
				s.Contains(w.Body.String(), `"code":"burn_read_not_explicit"`)
			}
		})
	}
}

// TestGetNote_ExplicitBurnRead_Consumes: naming the format exactly is the intent a burning read
// needs — the request goes straight to View, which consumes the note.
func (s *HandlerSuite) TestGetNote_ExplicitBurnRead_Consumes() {
	for _, accept := range []string{"application/json", "text/markdown", "application/json, */*;q=0.1"} {
		s.Run(accept, func() {
			note := newTestNote("burn", "burn-body")
			note.BurnAfterReading = true

			s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
			r.Header.Set("Accept", accept)
			r.Header.Set("Sec-Fetch-Dest", "empty")

			s.router.ServeHTTP(w, r)

			s.Equal(http.StatusOK, w.Code)
			s.Contains(w.Body.String(), "burn-body")
		})
	}
}

// TestGetNote_ImplicitRead_PeekNotFound: the implicit-read gate surfaces Peek's error as is.
func (s *HandlerSuite) TestGetNote_ImplicitRead_PeekNotFound() {
	s.manager.EXPECT().Peek(gomock.Any(), "nonexistent").Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/nonexistent", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetNote_NotFound() {
	s.manager.EXPECT().View(gomock.Any(), "nonexistent").Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/nonexistent", nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetNote_Expired() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(nil, domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

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
	note.Privacy = new(domain.PrivacyAuthenticated)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	router := s.newRouterWithTOTPAuth()

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
	note.Privacy = new(domain.PrivacyAuthenticated)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
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

	router := s.newRouterWithTOTPAuth()

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
	note.Privacy = new(domain.PrivacyAuthenticated)
	s.manager.EXPECT().Peek(gomock.Any(), mid).Return(note, nil)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+mid, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// TestEditPage_MultiSegmentPath_GatedByPrivacy verifies the /edit/{id...} route's gating: GET
// /edit/<multi-segment-slug> is admitted by isPublicRoute (the editor's save still requires the
// edit code on the PUT), but per-note privacy is enforced inside EditPage via handlePrivateAuth —
// a public note renders the form, a privacy=owner note redirects an anonymous caller to /login.
func (s *HandlerSuite) TestEditPage_MultiSegmentPath_GatedByPrivacy() {
	router := s.newRouterWithTOTPAuth()

	public := newTestNote("guide", "# guide")
	private := newTestNote("secret", "hidden")
	private.Privacy = new(domain.PrivacyOwner)

	s.manager.EXPECT().Peek(gomock.Any(), "project/GUIDE.md").Return(public, nil)
	s.manager.EXPECT().EnforceExpiry(gomock.Any(), "project/GUIDE.md", public).Return(nil)
	s.manager.EXPECT().Peek(gomock.Any(), "project/secret.md").Return(private, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/project/GUIDE.md", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `const noteID = "abc-123"`)

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/edit/project/secret.md", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"privacy=owner note must redirect an anonymous caller to login, not render the editor")
}

func (s *HandlerSuite) TestGetNote_Private_NotFound() {
	s.manager.EXPECT().Peek(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/missing", nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// ── UpdateNote ──

func (s *HandlerSuite) TestUpdateNote_OK() {
	s.expectMutationGate()

	createdAt := time.Now().Add(-time.Hour)
	updated := &domain.Note{
		ID:          testID,
		Title:       "new title",
		Content:     "new content",
		ContentType: new(domain.ContentTypePlain),
		CreatedAt:   createdAt,
		UpdatedAt:   time.Now(),
	}
	s.manager.EXPECT().Update(gomock.Any(), testID, "editcode1234", uuid.Nil, gomock.Any()).Return(updated, nil)

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
	s.expectMutationGate()

	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", uuid.Nil, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ uuid.UUID, n *domain.Note) (*domain.Note, error) {
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
	s.expectMutationGate()

	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", uuid.Nil, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ uuid.UUID, n *domain.Note) (*domain.Note, error) {
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

func (s *HandlerSuite) TestUpdateNote_WithPrivacy() {
	s.expectMutationGate()

	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", uuid.Nil, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ uuid.UUID, n *domain.Note) (*domain.Note, error) {
			s.Equal(domain.PrivacyAuthenticated, n.EffectivePrivacy())

			return updated, nil
		})

	body := `{"title":"updated","content":"body","edit_code":"code","privacy":"authenticated"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_NotFound() {
	s.expectMutationGate()
	s.manager.EXPECT().Update(gomock.Any(), "missing", "", uuid.Nil, gomock.Any()).Return(nil, domain.ErrNotFound)

	body := `{"title":"title","content":"content","edit_code":""}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/missing", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_Forbidden() {
	s.expectMutationGate()
	s.manager.EXPECT().Update(gomock.Any(), testID, "wrong", uuid.Nil, gomock.Any()).Return(nil, domain.ErrInvalidEditCode)

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
	s.expectMutationGate()

	const mid = "project/GUIDE.md"

	updated := newTestNote("Guide", "new body")
	s.manager.EXPECT().Update(gomock.Any(), mid, "code", uuid.Nil, gomock.Any()).Return(updated, nil)

	body := `{"title":"Guide","content":"new body","edit_code":"code"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+mid, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code, "editing a path-like slug via PUT /notes/{id...} must succeed")
}

func (s *HandlerSuite) TestDeleteNote_MultiSegmentSlug_ByURL_Acceptance() {
	s.expectMutationGate()

	const mid = "project/GUIDE.md"

	s.manager.EXPECT().Delete(gomock.Any(), mid, "code", uuid.Nil).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+mid, nil)
	r.Header.Set("X-Edit-Code", "code")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code, "deleting a path-like slug via DELETE /notes/{id...} must succeed")
}

// TestUpdateNote_OmittedPrivate_PreservesPrivacy verifies that when "privacy" is absent
// from a PUT request body, the handler passes Privacy=nil to manager.Update so that the
// storage layer can COALESCE(NULL, privacy) and preserve the existing DB value.
func (s *HandlerSuite) TestUpdateNote_OmittedPrivate_PreservesPrivacy() {
	s.expectMutationGate()
	s.manager.EXPECT().Update(gomock.Any(), testID, "code", uuid.Nil, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ uuid.UUID, n *domain.Note) (*domain.Note, error) {
			// nil means "don't change" — storage will COALESCE(NULL, privacy).
			s.Nil(n.Privacy, "Privacy must be nil when omitted from the request, not defaulted to public")

			return newTestNote("updated", "new content"), nil
		})

	// No "privacy" field in the body.
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
	s.expectMutationGate()
	s.manager.EXPECT().Delete(gomock.Any(), testID, "editcode1234", uuid.Nil).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "editcode1234")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

// TestDeleteNote_QueryParamIgnored pins that the edit code is never read from the query string
// (it would leak through proxy/CDN access logs and Referer): the manager sees an empty code and
// the missing-code rejection surfaces as 403, for single-segment and path-like slugs alike.
func (s *HandlerSuite) TestDeleteNote_QueryParamIgnored() {
	for _, id := range []string{testID, "project/GUIDE.md"} {
		s.expectMutationGate()
		s.manager.EXPECT().Delete(gomock.Any(), id, "", uuid.Nil).Return(domain.ErrInvalidEditCode)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/notes/"+id+"?edit_code=fromquery", nil)

		s.router.ServeHTTP(w, r)

		s.Equal(http.StatusForbidden, w.Code, "edit_code in the query string must be ignored for %q", id)
	}
}

func (s *HandlerSuite) TestDeleteNote_NotFound() {
	s.expectMutationGate()
	s.manager.EXPECT().Delete(gomock.Any(), "nonexistent", "code", uuid.Nil).Return(domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/nonexistent", nil)
	r.Header.Set("X-Edit-Code", "code")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestDeleteNote_Forbidden() {
	s.expectMutationGate()
	s.manager.EXPECT().Delete(gomock.Any(), testID, "wrong", uuid.Nil).Return(domain.ErrInvalidEditCode)

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
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, adhttp.NoPinger{}, discardLog)
	opts := adhttp.RouterOptions{MaxBodyBytes: 256 * 1024}
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

// TestTOTPLoginHandler_AccountsDisabled_Returns404 covers the nil-guard added so that the
// account system is opt-in: with no auth manager configured, /totp-login must 404.
func (s *HandlerSuite) TestTOTPLoginHandler_AccountsDisabled_Returns404() {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog) // no WithAuthManager → accounts disabled
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/totp-login", nil)

	handler.TOTPLoginHandler(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// TestChangePasswordPage_AccountsDisabled_Returns404 mirrors the TOTPLoginHandler nil-guard
// test above for the GET /change-password page.
func (s *HandlerSuite) TestChangePasswordPage_AccountsDisabled_Returns404() {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog) // no WithAuthManager → accounts disabled
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
	handler := adhttp.NewHandler(s.manager, s.linkManager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	handler.IndexPage(fw, r)

	s.Equal(0, fw.code)
}

func (s *HandlerSuite) TestEditPage_WriteFail() {
	note := newTestNote("edit", "body")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().EnforceExpiry(gomock.Any(), testID, note).Return(nil)

	handler := adhttp.NewHandler(s.manager, s.linkManager, slog.New(slog.DiscardHandler))
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)
	r.SetPathValue("id", testID)

	handler.EditPage(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestSuccessPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, s.linkManager, slog.New(slog.DiscardHandler))
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)

	handler.SuccessPage(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestGetNote_HTML_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(note, "<p>body</p>", nil)

	handler := adhttp.NewHandler(s.manager, s.linkManager, slog.New(slog.DiscardHandler))
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

	handler := adhttp.NewHandler(s.manager, s.linkManager, slog.New(slog.DiscardHandler))
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

	handler := adhttp.NewHandler(s.manager, s.linkManager, slog.New(slog.DiscardHandler))
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "application/json")

	handler.GetNote(fwr, r)

	s.NotNil(fwr)
}

// ── Rate limit ──

func (s *HandlerSuite) TestRateLimit_Exceeded() {
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
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

// TestRateLimit_Exceeded_POSTLinks confirms rate limiting is actually wired to /links, not just
// assumed from it sharing a mux with the already-tested / route (see TODO.md's Bug-class
// checklist, old C7, and the acceptance criteria's explicit "POST /links returns 429").
func (s *HandlerSuite) TestRateLimit_Exceeded_POSTLinks() {
	link := newTestLink()
	s.linkManager.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(link, nil)

	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		RateLimit:    1,
		RateBurst:    1,
	}
	router, err := adhttp.NewRouter(handler, ogen, &opts)
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
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
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
	s.expectMutationGate()
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", uuid.Nil, gomock.Any()).
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

// TestFailLockout_ResetsOnSuccess verifies a successful request clears the accumulated failure
// count: an owner who mistypes the edit code a few times before getting it right must not carry
// a stale near-threshold counter into their next legitimate edit.
func (s *HandlerSuite) TestFailLockout_ResetsOnSuccess() {
	s.expectMutationGate()

	note := newTestNote("t", "c")

	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", uuid.Nil, gomock.Any()).
		Return(nil, domain.ErrForbidden).
		Times(9)
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "valid", uuid.Nil, gomock.Any()).
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
		Update(gomock.Any(), testID, "wrong", uuid.Nil, gomock.Any()).
		Return(nil, domain.ErrForbidden)

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(wrongBody))
	r.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusForbidden, w.Code, "counter must have reset on the earlier success, not carried toward lockout")
}

func (s *HandlerSuite) TestFailLockout_NoIncrementOnSuccess() {
	s.expectMutationGate()

	note := newTestNote("t", "c")
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "valid", uuid.Nil, gomock.Any()).
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
	s.expectMutationGate()
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", uuid.Nil, gomock.Any()).
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
	s.expectMutationGate()
	s.manager.EXPECT().
		Delete(gomock.Any(), testID, "wrong", uuid.Nil).
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
	s.expectMutationGate()

	body := `{"title":"t","content":"c","edit_code":"wrong"}`

	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", uuid.Nil, gomock.Any()).
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
		Update(gomock.Any(), otherID, "wrong", uuid.Nil, gomock.Any()).
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

	handler := adhttp.NewHandler(s.manager, s.linkManager, slog.New(slog.DiscardHandler))
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

func (s *HandlerSuite) TestAuth_MissingToken_Browser() {
	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"should redirect to /login with ?next= param")
}

// TestAuth_InvalidToken_JSONError verifies a non-browser 401 (invalid bearer token) is a JSON
// body matching the same {"message": ...} shape as every other API error, not http.Error's
// plain text — the generated ogen client (used by the CLI) decodes API errors as JSON, so a
// plain-text body would surface as an opaque decode failure instead of a clean "unauthorized".
// /admin is a named route, so it is gated by the middleware itself, before any handler runs.
func (s *HandlerSuite) TestAuth_InvalidToken_JSONError() {
	router := s.newRouterWithTOTPAuth()
	s.authMgr.EXPECT().ResolveAPIToken(gomock.Any(), "bogus-token").Return(nil, domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.Header.Set("Authorization", "Bearer bogus-token")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
	s.Equal("application/json", w.Header().Get("Content-Type"))
	s.JSONEq(`{"message":"unauthorized"}`, w.Body.String())
}

func (s *HandlerSuite) TestAuth_PublicPaths() {
	router := s.newRouterWithTOTPAuth()

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

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

// TestAuth_Create_AnonymousAllowed verifies that with auth configured, an anonymous caller can
// still create a note: POST /notes is admitted by isPublicRoute, and the usecase itself rejects
// an owner-only privacy requested by an anonymous caller (tested via ValidateOwnership, not
// middleware).
func (s *HandlerSuite) TestAuth_Create_AnonymousAllowed() {
	router := s.newRouterWithTOTPAuth()
	note := newTestNote("t", "c")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

// TestAuth_Update_Anonymous_PublicNote_WrongEditCode verifies the restored edit_code workflow:
// with auth configured, an anonymous caller hitting PUT on a public note passes the middleware
// and the mutation privacy gate (a public note admits anonymous callers), then the usecase's
// edit-code verification decides the outcome — a wrong code yields 403, not the middleware's 401.
func (s *HandlerSuite) TestAuth_Update_Anonymous_PublicNote_WrongEditCode() {
	router := s.newRouterWithTOTPAuth()
	s.expectMutationGate()
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "x", uuid.Nil, gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode)

	w := httptest.NewRecorder()
	body := `{"title":"t","content":"c","edit_code":"x"}`
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestAuth_Delete_Anonymous_PublicNote_WrongEditCode is the DELETE counterpart of
// TestAuth_Update_Anonymous_PublicNote_WrongEditCode: a wrong edit code is a usecase-level 403,
// never a middleware-level 401.
func (s *HandlerSuite) TestAuth_Delete_Anonymous_PublicNote_WrongEditCode() {
	router := s.newRouterWithTOTPAuth()
	s.expectMutationGate()
	s.manager.EXPECT().
		Delete(gomock.Any(), testID, "wrong", uuid.Nil).
		Return(domain.ErrInvalidEditCode)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "wrong")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestAuth_MissingToken_API verifies the mutation privacy gate on the API path: with auth
// configured, an anonymous PUT against a privacy=owner note is refused with 401 by the gate —
// someone who could not read the note may not attempt to mutate it via an edit code either.
// Update having no EXPECT guarantees the usecase is never reached.
func (s *HandlerSuite) TestAuth_MissingToken_API() {
	router := s.newRouterWithTOTPAuth()
	private := newTestNote("t", "c")
	private.Privacy = new(domain.PrivacyOwner)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(private, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
	// The ogen spec declares PUT /notes/{id} 401 without a schema, so the gate returns
	// the empty UpdateNoteUnauthorized — no JSON body to decode on the client side.
	s.Empty(w.Body.String())
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
	router := s.newRouterWithTOTPAuth()
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
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Sign in with TOTP")
}

func (s *HandlerSuite) TestLoginPage_Error() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login?error=1", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Invalid credentials")
}

func (s *HandlerSuite) TestLoginPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, s.linkManager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/login?error=1", nil)

	handler.LoginPage(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestLoginPage_WithNext() {
	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login?next=%2Fnotes%2Fabc", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `name="next"`)
	s.Contains(w.Body.String(), `/notes/abc`)
}

func (s *HandlerSuite) TestAuth_MissingToken_NextContainsOriginalURL() {
	router := s.newRouterWithTOTPAuth()

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
	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	// No Accept, no Sec-Fetch-Dest — typical API client (curl)
	r := httptest.NewRequest(http.MethodGet, "/success", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestAuth_Browser_SecFetchDestDocument_Returns303() {
	router := s.newRouterWithTOTPAuth()

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

// TestCSP_NonFetchDirectivesHardened verifies the directives that default-src does not cover are
// pinned on every page, including the API docs page: no <base> injection, forms posting only to
// the same origin, no framing and no plugin content.
func (s *HandlerSuite) TestCSP_NonFetchDirectivesHardened() {
	for _, path := range []string{"/", "/api"} {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		csp := rec.Header().Get("Content-Security-Policy")

		for _, directive := range []string{
			"base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'", "object-src 'none'",
		} {
			s.Contains(csp, directive, "CSP on %s must contain %q", path, directive)
		}
	}
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
	handler := adhttp.NewHandler(s.manager, s.linkManager, slog.New(slog.DiscardHandler))
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
	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog).WithAuthManager(s.authMgr)
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{MaxBodyBytes: 256 * 1024}
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

	// handlePrivateAuth calls Peek (authMgr != nil), passes note as preloaded.
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
// In TOTP-only deployments (authMgr set) handlePrivateAuth must respect the session before
// serving private notes, and CanEdit must depend on actual authentication state.
//
// Call-flow for GET /notes/{id} in TOTP mode:
//   1. Auth middleware is bypassed (note route is public).
//   2. handlePrivateAuth → Peek → checks Privacy (VisibleTo) + isAuthenticated.
//   3. For public notes: returns (note, false) without calling isAuthenticated.
//   4. renderNoteHTML(preloaded) → GetRenderedPreloaded → isAuthenticated + VisibleTo (for CanEdit).

// TestPrivateNote_TOTPMode_Unauthenticated verifies that an unauthenticated
// request to a private note is redirected to /login in TOTP-only mode.
func (s *HandlerSuite) TestPrivateNote_TOTPMode_Unauthenticated() {
	note := newTestNote("secret", "private content")
	note.Privacy = new(domain.PrivacyAuthenticated)

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

// ── Anonymous mutation privacy gate (PUT/DELETE) ──
//
// The gate (anonymousNoteMutationAllowed / gateAnonymousMutation) implements the restored
// semantics: an anonymous caller may mutate a note with an edit code if and only if they could
// also read it. The tests below exercise every branch: refused (private), deferred (Peek error),
// passed for an authenticated caller, and no interaction with the fail-lockout.

// TestMutationGate_AnonymousDelete_PrivateNote verifies the ogen DELETE gate: with auth
// configured, an anonymous DELETE against a privacy=authenticated note is refused with a
// bodyless 401 before the usecase is ever reached (Delete having no EXPECT guarantees that).
func (s *HandlerSuite) TestMutationGate_AnonymousDelete_PrivateNote() {
	router := s.newRouterWithTOTPAuth()
	note := newTestNote("secret", "nope")
	note.Privacy = new(domain.PrivacyAuthenticated)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "whatever")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
	// DELETE /notes/{id} 401 has no schema in the ogen spec → empty DeleteNoteUnauthorized.
	s.Empty(w.Body.String())
}

// TestMutationGate_AnonymousDelete_OwnerNote is the same branch for privacy=owner — the strictest
// level (even a signed-in non-owner may not read it, let alone mutate it anonymously).
func (s *HandlerSuite) TestMutationGate_AnonymousDelete_OwnerNote() {
	router := s.newRouterWithTOTPAuth()
	note := newTestNote("secret", "nope")
	note.Privacy = new(domain.PrivacyOwner)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "whatever")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// TestMutationGate_AuthenticatedCaller_PassesGate verifies that a signed-in caller is never
// refused by the gate — even on a privacy=owner note they do not own. The gate short-circuits
// on the caller ID without a Peek and steps aside; ownership is decided inside the usecase (a
// non-owner without the edit code gets 403 there).
func (s *HandlerSuite) TestMutationGate_AuthenticatedCaller_PassesGate_PersonalNote() {
	usr := newTestUser("bob")

	note := newTestNote("secret", "someone else's")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = new(uuid.UUID) // owned by a user who is not in this request

	router := s.newRouterWithTOTPAuth()
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "", usr.ID, gomock.Any()).
		Return(nil, domain.ErrForbidden)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil).Times(1)

	w := httptest.NewRecorder()
	body := `{"title":"t","content":"c"}`
	r := sessioned(httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestMutationGate_PeekError_DefersToMutation verifies the gate's fail-open behaviour on a
// Peek error: the request proceeds to the real mutation, so the caller sees the same 404 a read
// of a missing note would produce — never a spurious 401 (and never silent success).
func (s *HandlerSuite) TestMutationGate_PeekError_DefersToMutation() {
	router := s.newRouterWithTOTPAuth()
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(nil, domain.ErrNotFound)
	s.manager.EXPECT().
		Delete(gomock.Any(), testID, "whatever", uuid.Nil).
		Return(domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "whatever")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// TestMutationGate_Note401DoesNotTriggerFailLockout verifies the gate/lockout interplay: the
// fail-lockout counts consecutive 403s (wrong edit codes), while the mutation gate's 401 is a
// pre-check that never reaches the counter — repeated gated attempts stay 401 and never
// escalate to 429.
func (s *HandlerSuite) TestMutationGate_Note401DoesNotTriggerFailLockout() {
	router := s.newRouterWithTOTPAuth()
	note := newTestNote("secret", "nope")
	note.Privacy = new(domain.PrivacyOwner)
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil).Times(12)

	for range 12 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(`{"title":"t","content":"c"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json")

		router.ServeHTTP(w, r)

		s.Equal(http.StatusUnauthorized, w.Code, "each gated attempt must be 401")
	}
}

// TestMutationGate_MultiSegmentPaths verifies the native {id...} routes are gated too: an
// anonymous PUT and DELETE of a privacy=multi-segment-slug note whose privacy refuses anonymous
// callers are refused with 401 (Update/Delete having no EXPECT guarantees the usecase is
// never reached).
func (s *HandlerSuite) TestMutationGate_MultiSegmentPaths() {
	router := s.newRouterWithTOTPAuth()
	note := newTestNote("guide", "internal")
	note.Privacy = new(domain.PrivacyAuthenticated)
	s.manager.EXPECT().Peek(gomock.Any(), "project/GUIDE.md").Return(note, nil).Times(2)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/project/GUIDE.md", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodDelete, "/notes/project/GUIDE.md", nil)
	r.Header.Set("X-Edit-Code", "whatever")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// TestPrivateNote_TOTPMode_Authenticated verifies that a valid session grants
// access to a private note in TOTP-only mode.
// GetSession is called exactly once: the auth middleware resolves the session for the
// note route and stores the user in context, so both handlePrivateAuth (private check)
// and renderNoteHTML (CanEdit) read it from context without re-querying.
func (s *HandlerSuite) TestPrivateNote_TOTPMode_Authenticated() {
	note := newTestNote("secret", "private content")
	note.Privacy = new(domain.PrivacyAuthenticated)
	usr := &domain.User{ID: uuid.New(), Username: "alice"}

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

// TestOwnerOnlyNote_Owner_Authorized verifies that a note with privacy: owner is readable by
// the exact authenticated session that created it.
func (s *HandlerSuite) TestOwnerOnlyNote_Owner_Authorized() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}

	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner.ID

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().GetRenderedPreloaded(gomock.Any(), testID, note).Return(note, "rendered", nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(owner, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie
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
	owner := &domain.User{ID: uuid.New(), Username: "alice"}
	other := &domain.User{ID: uuid.New(), Username: "bob"}

	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner.ID

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(other, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
	s.Empty(w.Header().Get("Location"), "a settled 403 must not bounce the caller anywhere")
	s.NotContains(w.Body.String(), "owner-only content",
		"the note's plaintext must never reach a non-owner's response body")
}

// TestOwnerOnlyNote_Anonymous_Unauthorized verifies that a note with privacy: owner is not
// readable by an unauthenticated caller.
func (s *HandlerSuite) TestOwnerOnlyNote_Anonymous_Unauthorized() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}

	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner.ID

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")
	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// TestOwnerOnlyNote_Anonymous_HTML_RedirectToLogin is the HTML-negotiated counterpart to
// TestOwnerOnlyNote_Anonymous_Unauthorized: a browser request (Accept: text/html) to a
// privacy: owner note from an anonymous caller must redirect to /login, not 401.
func (s *HandlerSuite) TestOwnerOnlyNote_Anonymous_HTML_RedirectToLogin() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}

	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner.ID

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="))
}

// TestEditPage_OwnerOnly_NonOwner_Forbidden verifies that GET /edit/{id} for a privacy: owner
// note is gated exactly like GET /notes/{id} — a different authenticated caller must not see the
// note's plaintext content in the editor form, even though they are logged in.
func (s *HandlerSuite) TestEditPage_OwnerOnly_NonOwner_Forbidden() {
	owner := &domain.User{ID: uuid.New(), Username: "alice"}
	other := &domain.User{ID: uuid.New(), Username: "bob"}

	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner.ID

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(other, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie
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
	owner := &domain.User{ID: uuid.New(), Username: "alice"}

	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner.ID

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().EnforceExpiry(gomock.Any(), testID, note).Return(nil)
	s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(owner, nil).Times(1)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(&http.Cookie{Name: "padmark_session", Value: "valid-sess"}) //nolint:gosec // G124: test cookie
	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "owner-only content")
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
	usr := &domain.User{ID: uuid.New(), Username: "alice"}

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

// TestCanEdit_NoAuthConfigured_OwnerOnlyNote_HidesEditButton verifies CanEdit's VisibleTo gate
// directly: when no auth is configured at all, handlePrivateAuth's blanket bypass still lets the
// page render for everyone (200), but a privacy: owner note (e.g. left over from when accounts
// were enabled) must not show the Edit link — isAuthenticated() is unconditionally true in this
// mode, yet nobody can prove ownership of a note without a real caller identity.
func (s *HandlerSuite) TestCanEdit_NoAuthConfigured_OwnerOnlyNote_HidesEditButton() {
	owner := uuid.New()
	note := newTestNote("secret", "owner-only content")
	note.Privacy = new(domain.PrivacyOwner)
	note.OwnerID = &owner

	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(note, "rendered", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.NotContains(w.Body.String(), `href="/edit/`+testID+`"`,
		"no caller identity can prove ownership when auth isn't configured, so no Edit button")
}
