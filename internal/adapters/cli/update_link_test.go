package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

const (
	cmdUpdateLink = "update-link"
	testLinkID    = "link123abc"
)

// linkJSON is a LinkResponse body of the link testLinkID.
const linkJSON = `{"id":"link123abc","target_url":"https://new.example/path#part","views":3,` +
	`"burn_after_click":false,"created_at":"2026-11-20T10:00:00Z"}`

type UpdateLinkSuite struct {
	suite.Suite

	status  int
	body    string
	method  string
	payload string
	server  *httptest.Server
}

func TestUpdateLinkSuite(t *testing.T) {
	suite.Run(t, new(UpdateLinkSuite))
}

func (s *UpdateLinkSuite) SetupTest() {
	isolateEnv(s.T())

	s.status, s.body = http.StatusOK, linkJSON
	s.method, s.payload = "", ""
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			s.T().Errorf("read body: %v", err)
		}

		s.method = r.Method + " " + r.URL.Path
		s.payload = string(data)
		writeTestResponse(s.T(), w, s.status, s.body)
	}))
}

func (s *UpdateLinkSuite) TearDownTest() {
	s.server.Close()
}

func (s *UpdateLinkSuite) run(args ...string) (string, error) {
	return runCLIOutput(s.T().Context(), append([]string{"--url", s.server.URL, cmdUpdateLink}, args...)...)
}

func (s *UpdateLinkSuite) TestSendsOnlySetFieldsAndPrintsLink() {
	out, err := s.run(testLinkID, "--target-url", "https://new.example/path#part", "--edit-code", "MySecretCode1")

	s.Require().NoError(err)
	s.Equal("PUT /links/"+testLinkID, s.method)
	s.JSONEq(`{"target_url":"https://new.example/path#part","edit_code":"MySecretCode1"}`, s.payload)
	s.Equal("id:          link123abc\n"+
		"target_url:  https://new.example/path#part\n"+
		"views:       3\n"+
		"created:     2026-11-20T10:00:00Z\n", out)
}

func (s *UpdateLinkSuite) TestSendsOptionalFields() {
	_, err := s.run(testLinkID, "--target-url", "https://new.example", "--ttl", "3600",
		"--privacy", "owner", "--direct-redirect=false")

	s.Require().NoError(err)
	s.JSONEq(`{"target_url":"https://new.example","ttl":3600,"privacy":"owner","direct_redirect":false}`, s.payload)
}

func (s *UpdateLinkSuite) TestRefusesInputBeforeRequest() {
	cases := map[string]struct {
		want error
		args []string
	}{
		"missing ID": {args: []string{"--target-url", "https://x.example"}, want: errLinkIDRequired},
		"zero TTL": {
			args: []string{testLinkID, "--target-url", "https://x.example", "--ttl", "0"}, want: errNonPositiveTTL,
		},
		"large TTL": {
			args: []string{testLinkID, "--target-url", "https://x.example", "--ttl", "9223372037"}, want: errTTLTooLarge,
		},
		"dot ID": {args: []string{"..", "--target-url", "https://x.example"}, want: errInvalidID},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			s.method = ""

			_, err := s.run(testCase.args...)

			s.Require().ErrorIs(err, testCase.want)
			s.Empty(s.method, "the command must stop before the request")
		})
	}
}

func (s *UpdateLinkSuite) TestRequiresTargetURL() {
	_, err := s.run(testLinkID)

	s.Require().ErrorContains(err, `"target-url" not set`)
	s.Empty(s.method)
}

func (s *UpdateLinkSuite) TestMapsErrors() {
	cases := map[string]struct {
		want   error
		body   string
		status int
	}{
		"wrong edit code": {status: http.StatusForbidden, body: `{"message":"x"}`, want: errLinkOwnershipRequired},
		"no credential":   {status: http.StatusUnauthorized, body: ``, want: ErrUnauthorized},
		"missing link":    {status: http.StatusNotFound, body: `{"message":"x"}`, want: ErrLinkNotFound},
		"validation":      {status: http.StatusUnprocessableEntity, body: `{"message":"bad"}`, want: ErrValidation},
		"rate limit":      {status: http.StatusTooManyRequests, body: `{"message":"x"}`, want: ErrRateLimited},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			s.status, s.body = testCase.status, testCase.body

			_, err := s.run(testLinkID, "--target-url", "https://new.example")

			s.Require().ErrorIs(err, testCase.want)
			s.ErrorContains(err, "decode update link response: ")
		})
	}
}
