package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

// linkTargets returns the link targets for the tests. The addresses differ only in "#" and "%23".
func linkTargets() []string {
	return []string{
		"https://example.org/guide#install",
		"https://example.org/guide%23install",
		"https://example.org/guide?lang=en#install",
		"https://example.org/a%23b#c",
	}
}

// LinkTargetSuite checks that expand and shorten print the target of a link with the address that
// the server sent, and that shorten sends the address that the user gave. The commands run
// against a test server, so the generated ogen decoder reads a real HTTP response.
type LinkTargetSuite struct {
	suite.Suite

	// target is what the test server answers; sent is the target_url of the last create request.
	target string
	sent   string
	server *httptest.Server
}

func TestLinkTargetSuite(t *testing.T) {
	suite.Run(t, new(LinkTargetSuite))
}

func (s *LinkTargetSuite) SetupTest() {
	isolateEnv(s.T())

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusOK

		if r.Method == http.MethodPost {
			status = http.StatusCreated

			var req struct {
				TargetURL string `json:"target_url"`
			}

			data, readErr := io.ReadAll(r.Body)
			s.NoError(readErr)
			s.NoError(json.Unmarshal(data, &req))

			s.sent = req.TargetURL
		}

		body, err := json.Marshal(map[string]any{
			"id": "example", "target_url": s.target, "views": 0, "burn_after_click": false,
			"created_at": "2026-10-25T01:02:03Z", "edit_code": "link-target-code",
		})
		s.NoError(err)

		writeTestResponse(s.T(), w, status, string(body))
	}))
}

func (s *LinkTargetSuite) TearDownTest() {
	s.server.Close()
}

func (s *LinkTargetSuite) TestExpandText() {
	for _, target := range linkTargets() {
		s.Run(target, func() {
			s.target = target

			out, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, "expand", "example")
			s.Require().NoError(err)
			s.Equal(target, outputFields(out)["target_url"])
		})
	}
}

func (s *LinkTargetSuite) TestExpandJSON() {
	for _, target := range linkTargets() {
		s.Run(target, func() {
			s.target = target

			out, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, "expand", "example", "--json")
			s.Require().NoError(err)

			var printed struct {
				TargetURL string `json:"target_url"`
			}

			s.Require().NoError(json.Unmarshal([]byte(out), &printed), out)
			s.Equal(target, printed.TargetURL)
		})
	}
}

func (s *LinkTargetSuite) TestShorten() {
	for _, target := range linkTargets() {
		s.Run(target, func() {
			s.target = target

			out, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, "shorten", "--target-url", target)
			s.Require().NoError(err)
			s.Equal(target, s.sent, "the request carries the address that the user gave")
			s.Equal(target, outputFields(out)["target_url"], "the output shows the address that the server saved")
		})
	}
}
