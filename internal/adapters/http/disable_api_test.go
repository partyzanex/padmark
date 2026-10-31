package http_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

type DisableAPISuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	manager     *MockNoteManager
	linkManager *MockLinkManager
	pinger      *MockPinger
}

func TestDisableAPI(t *testing.T) {
	suite.Run(t, new(DisableAPISuite))
}

func (s *DisableAPISuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)
}

func (s *DisableAPISuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *DisableAPISuite) newRouter(disableAPI bool) http.Handler {
	s.T().Helper()

	handler := adhttp.NewHandler(s.manager, s.linkManager, discardLog())
	ogen := adhttp.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog())

	opts := adhttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   testCSRFSecret,
		DisableAPI:   disableAPI,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DirectRedirect: false,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// TestAPIPaths_Return503WithCode covers every REST/JSON API route (ogen single-segment and
// native multi-segment) with --disable-api set. No mock expectation is set on s.manager for any
// of these — if the request reached a real handler, the mock would fail the test on the
// unexpected call, which proves the middleware prevents business logic from running.
func (s *DisableAPISuite) TestAPIPaths_Return503WithCode() {
	router := s.newRouter(true)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, "/notes", `{"title":"t","content":"c"}`},
		{"update single-segment", http.MethodPut, "/notes/" + testID, `{"title":"t","content":"c"}`},
		{"delete single-segment", http.MethodDelete, "/notes/" + testID, ""},
		{"read single-segment", http.MethodGet, "/notes/" + testID, ""},
		{"update multi-segment", http.MethodPut, "/notes/project/GUIDE.md", `{"title":"t","content":"c"}`},
		{"delete multi-segment", http.MethodDelete, "/notes/project/GUIDE.md", ""},
		{"link create", http.MethodPost, "/links", `{"target_url":"https://example.com"}`},
		{"link bulk create", http.MethodPost, "/links/bulk", `{"target_urls":["https://example.com"]}`},
		{"link read single-segment", http.MethodGet, "/links/" + testID, ""},
		{"link update single-segment", http.MethodPut, "/links/" + testID, `{"target_url":"https://example.com"}`},
		{"link delete single-segment", http.MethodDelete, "/links/" + testID, ""},
		{"token create", http.MethodPost, "/api/tokens", ""},
		{"invite create", http.MethodPost, "/api/invites", ""},
		{"api docs", http.MethodGet, "/api", ""},
		{"openapi spec", http.MethodGet, "/api/openapi.yaml", ""},
		{"API reference", http.MethodGet, "/api/reference.md", ""},
		{"llms.txt", http.MethodGet, "/llms.txt", ""},
	}

	for _, testCase := range cases {
		s.Run(testCase.name, func() {
			var body *strings.Reader
			if testCase.body != "" {
				body = strings.NewReader(testCase.body)
			} else {
				body = strings.NewReader("")
			}

			r := httptest.NewRequest(testCase.method, testCase.path, body)
			r.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()

			router.ServeHTTP(w, r)

			s.Equal(http.StatusServiceUnavailable, w.Code)
			s.Equal("application/json", w.Header().Get("Content-Type"))
			s.JSONEq(`{"message":"API is disabled","code":"api_disabled"}`, w.Body.String())
		})
	}
}

// TestWebUI_Unaffected verifies the web UI and operational endpoints keep working when the API
// is disabled — only the REST/JSON API surface is short-circuited.
func (s *DisableAPISuite) TestWebUI_Unaffected() {
	router := s.newRouter(true)

	// The note view (no explicit Accept) and the editor each load the note via Peek; the view
	// then applies the read policy to that snapshot, the editor only enforces expiry.
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(newTestNote("t", "c"), nil).Times(2)
	s.manager.EXPECT().ViewPreloaded(gomock.Any(), testID, gomock.Any()).Return(newTestNote("t", "c"), nil)
	s.manager.EXPECT().EnforceExpiry(gomock.Any(), testID, gomock.Any()).Return(nil)
	s.linkManager.EXPECT().RedirectWithoutBurn(gomock.Any(), testID).Return(newTestLink(), nil)
	s.pinger.EXPECT().PingContext(gomock.Any()).Return(nil)

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"index", http.MethodGet, "/"},
		{"short URL note view", http.MethodGet, "/" + testID},
		{"edit page", http.MethodGet, "/edit/" + testID},
		{"login page", http.MethodGet, "/login"},
		{"short link redirect", http.MethodGet, "/r/" + testID},
		{"healthz", http.MethodGet, "/healthz"},
		{"readyz", http.MethodGet, "/readyz"},
	}

	for _, testCase := range cases {
		s.Run(testCase.name, func() {
			r := httptest.NewRequest(testCase.method, testCase.path, nil)
			w := httptest.NewRecorder()

			router.ServeHTTP(w, r)

			s.NotEqual(http.StatusServiceUnavailable, w.Code, "web UI / operational routes must not be disabled")
		})
	}
}

// TestDisableAPI_Off_APIStillWorks is the regression guard: with the flag off (default), the
// middleware must not be wired in at all, and normal API traffic keeps working.
func (s *DisableAPISuite) TestDisableAPI_Off_APIStillWorks() {
	router := s.newRouter(false)

	note := newTestNote("t", "c")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

// Exercise the generated decoder against the real middleware, not a synthetic 503 body.
func (s *DisableAPISuite) TestGeneratedClient_DistinguishesDisabledAPIFromReadinessFailure() {
	server := httptest.NewServer(s.newRouter(true))
	defer server.Close()

	client, err := padmark.NewClient(server.URL)
	s.Require().NoError(err)

	result, err := client.GetNote(s.T().Context(), padmark.GetNoteParams{ID: testID})
	s.Require().NoError(err)

	disabled, isDisabled := result.(*padmark.GetNoteServiceUnavailable)
	s.Require().True(isDisabled, "expected a typed 503, got %T", result)
	s.Equal("api_disabled", disabled.Code.Or(""))
	s.Equal("API is disabled", disabled.Message)

	s.pinger.EXPECT().PingContext(gomock.Any()).Return(errors.New("database offline"))

	readiness, err := client.Readyz(s.T().Context())
	s.Require().NoError(err)

	unavailable, isUnavailable := readiness.(*padmark.ErrorResponse)
	s.Require().True(isUnavailable, "expected readiness error, got %T", readiness)
	s.False(unavailable.Code.IsSet(), "a database failure must not look like a disabled API")
	s.Equal("db unavailable", unavailable.Message)
}
