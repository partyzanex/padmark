package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
)

// TestLLMsTxt_OK_SetsContentType verifies the happy path: correct Content-Type and non-empty
// body.
func TestLLMsTxt_OK_SetsContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/llms.txt", nil)

	web.LLMsTxt(discardLog())(rec, req)

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

	web.LLMsTxt(discardLog())(wer, req)

	expected := httptest.NewRecorder()
	web.LLMsTxt(discardLog())(expected, req)
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
	auth        *MockAuthManager
}

func TestLLMsTxtRouter(t *testing.T) {
	suite.Run(t, new(LLMsRouterSuite))
}

func (s *LLMsRouterSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)
	s.auth = NewMockAuthManager(s.ctrl)
}

func (s *LLMsRouterSuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *LLMsRouterSuite) newRouter() http.Handler {
	s.T().Helper()

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.Accounts = s.auth
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

// TestServedAsRouteToAnonymous verifies that a bare anonymous GET /llms.txt through the full
// router returns the document, not a fall-through into the /{id...} note catch-all (which would
// answer 404) and not a redirect to /login: the document is for agents. No mock expectation is
// set — reaching a note handler would fail the test on the unexpected mock call.
func (s *LLMsRouterSuite) TestServedAsRouteToAnonymous() {
	router := s.newRouter()

	req := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	s.Equal(http.StatusOK, rec.Code)
	s.NotEqual(http.StatusSeeOther, rec.Code, "anonymous fetch must not redirect to /login")
	s.Equal("text/markdown; charset=utf-8", rec.Header().Get("Content-Type"))
	s.NotEmpty(rec.Body.String())
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

// TestSpecURLsAndFilesSectionAdvertised cross-checks the document against the router: llms.txt
// mentions the spec URLs it points to, has the Files section, and its links point to the Files
// section of the reference.
func (s *LLMsRouterSuite) TestSpecURLsAndFilesSectionAdvertised() {
	router := s.newRouter()

	index := s.get(router, "/llms.txt")
	s.Contains(index, "/api/openapi.yaml")
	s.Contains(index, "/api/reference.md")
	s.Contains(index, "\n## Files\n")

	for _, text := range []string{
		"(/api/reference.md#files)", "`POST /files`", "multipart/form-data",
		"`GET /files/{id}/download`", "Accept: application/octet-stream",
		"`PUT /files/{id}`", "`DELETE /files/{id}`", "X-Edit-Code", "`413`",
	} {
		s.Contains(index, text)
	}

	s.Contains(s.get(router, "/api/reference.md"), "\n## Files\n", "the #files anchor must exist")
}

func (s *LLMsRouterSuite) get(router http.Handler, path string) string {
	s.T().Helper()

	req := httptest.NewRequestWithContext(s.T().Context(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	s.Require().Equal(http.StatusOK, rec.Code, path)

	return rec.Body.String()
}
