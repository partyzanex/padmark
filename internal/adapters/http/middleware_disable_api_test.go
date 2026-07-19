package http_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
)

type DisableAPISuite struct {
	suite.Suite

	ctrl    *gomock.Controller
	manager *MockNoteManager
	pinger  *MockPinger
}

func TestDisableAPI(t *testing.T) {
	suite.Run(t, new(DisableAPISuite))
}

func (s *DisableAPISuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)
}

func (s *DisableAPISuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *DisableAPISuite) newRouter(disableAPI bool) http.Handler {
	handler := adhttp.NewHandler(s.manager, discardLog, nil)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   testCSRFSecret,
		DisableAPI:   disableAPI,
	}

	router, err := adhttp.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// TestAPIPaths_Return503WithNoMessage covers every REST/JSON API route (ogen single-segment and
// native multi-segment) with --disable-api set. No mock expectation is set on s.manager for any
// of these — if the request reached a real handler, the mock would fail the test on the
// unexpected call, which is exactly what proves the middleware short-circuits before auth or
// business logic run.
func (s *DisableAPISuite) TestAPIPaths_Return503WithFixedMessage() {
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
		{"api docs", http.MethodGet, "/api", ""},
		{"openapi spec", http.MethodGet, "/api/openapi.yaml", ""},
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
			s.JSONEq(`{"message":"До свидания"}`, w.Body.String())
		})
	}
}

// TestWebUI_Unaffected verifies the web UI and operational endpoints keep working when the API
// is disabled — only the REST/JSON API surface is short-circuited.
func (s *DisableAPISuite) TestWebUI_Unaffected() {
	router := s.newRouter(true)

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(newTestNote("t", "c"), nil)
	s.manager.EXPECT().View(gomock.Any(), testID).Return(newTestNote("t", "c"), nil)
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
