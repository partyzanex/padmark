package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
)

// TestLLMsTxt_OK_SetsContentType verifies the happy path: correct Content-Type and non-empty
// body.
func TestLLMsTxt_OK_SetsContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/llms.txt", nil)

	adhttp.LLMsTxt(discardLog())(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/markdown; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.NotEmpty(t, rec.Body.Bytes())
}

// TestLLMsTxt_WriteError_DoesNotCorruptBody verifies that when w.Write fails, LLMsTxt does not
// call http.Error and therefore does not append error text to the body — same contract as APISpec.
func TestLLMsTxt_WriteError_DoesNotCorruptBody(t *testing.T) {
	wer := &errorWriter{
		writeHeaderCalls: 0, body: nil,
	}
	req, _ := http.NewRequest(http.MethodGet, "/llms.txt", nil)

	adhttp.LLMsTxt(discardLog())(wer, req)

	expected := httptest.NewRecorder()
	adhttp.LLMsTxt(discardLog())(expected, req)
	assert.Equal(t, expected.Body.Bytes(), wer.body,
		"http.Error must not append text after a partial Write")
	// WriteHeader must not have been called — no status change attempted.
	assert.Equal(t, 0, wer.writeHeaderCalls,
		"WriteHeader must not be called when Write fails")
}

// errorWriter always fails on Write — used to simulate a broken connection. Defined locally
// (the white-box router_test.go has its own copy in package http) with the same contract.
type errorWriter struct {
	writeHeaderCalls int
	body             []byte
}

func (e *errorWriter) Header() http.Header { return http.Header{} }

func (e *errorWriter) WriteHeader(_ int) { e.writeHeaderCalls++ }

func (e *errorWriter) Write(b []byte) (int, error) {
	e.body = append(e.body, b...)

	return 0, assert.AnError
}

// LLMsRouterSuite builds the full router (with mock managers, like DisableAPISuite) to verify
// route wiring and middleware classification, not just the handler function.
type LLMsRouterSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	manager     *MockNoteManager
	linkManager *MockLinkManager
	pinger      *MockPinger
}

func TestLLMsTxtRouter(t *testing.T) {
	suite.Run(t, new(LLMsRouterSuite))
}

func (s *LLMsRouterSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)
}

func (s *LLMsRouterSuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *LLMsRouterSuite) newRouter() http.Handler {
	s.T().Helper()

	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog())
	handler.WithAuthManager(NewMockAuthManager(s.ctrl))
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog())

	opts := adhttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   testCSRFSecret,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// TestServedAsRoute verifies that a bare GET /llms.txt through the full router returns the
// document, not a fall-through into the /{id...} note catch-all (which would answer 404).
// No mock expectation is set — reaching a note handler would fail the test on the unexpected
// mock call.
func (s *LLMsRouterSuite) TestServedAsRoute() {
	router := s.newRouter()

	req := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("text/markdown; charset=utf-8", rec.Header().Get("Content-Type"))
	s.NotEmpty(rec.Body.String())
}

// TestAnonymousFetch is the auth-middleware regression guard: /llms.txt is agent-facing
// documentation, so an anonymous request must not be redirected to /login or sent to note
// resolution.
func (s *LLMsRouterSuite) TestAnonymousFetch() {
	router := s.newRouter()

	req := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusOK, rec.Code)
	s.NotEqual(http.StatusSeeOther, rec.Code, "anonymous fetch must not redirect to /login")
}

// TestReferencedSpecIsAlive guards the document's pointer: the spec URL it advertises must
// serve 200, so an agent following /llms.txt does not hit a dead link.
func (s *LLMsRouterSuite) TestReferencedSpecIsAlive() {
	router := s.newRouter()

	for _, path := range []string{"/api/openapi.yaml", "/api/reference.md", "/api"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		s.Equal(http.StatusOK, rec.Code, path)
	}
}

// TestReferencedSpecIsAdvertised cross-checks the document against the router: llms.txt must
// actually mention the spec URL it points to.
func (s *LLMsRouterSuite) TestSpecURLAdvertised() {
	router := s.newRouter()

	req := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	s.Contains(rec.Body.String(), "/api/openapi.yaml")
	s.Contains(rec.Body.String(), "/api/reference.md")
}
