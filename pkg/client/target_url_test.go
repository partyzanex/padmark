package client_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/pkg/client"
)

// Target URLs for the tests. The addresses differ only in "#" and "%23".
const (
	fragmentTarget      = "https://example.org/guide#install"
	encodedHashTarget   = "https://example.org/guide%23install"
	queryFragmentTarget = "https://example.org/guide?lang=en#install"
	pathQueryFragment   = "https://example.org/a%23b#c?d=1"
)

type TargetURLSuite struct {
	suite.Suite
}

func TestTargetURLSuite(t *testing.T) {
	suite.Run(t, new(TargetURLSuite))
}

// decodeLink reads a link through the generated ogen decoder, as the SDK does for a response.
func (s *TargetURLSuite) decodeLink(target string) client.LinkResponse {
	body := `{"id":"abc","target_url":"` + target + `","views":0,"burn_after_click":false,` +
		`"created_at":"2026-11-20T10:00:00Z"}`

	var link client.LinkResponse

	s.Require().NoError(link.UnmarshalJSON([]byte(body)))

	return link
}

// TestDecodeLosesFragment documents the fault of the generated decoder. If ogen gets fixed,
// this test fails, and SplitFragment becomes unnecessary.
func (s *TargetURLSuite) TestDecodeLosesFragment() {
	link := s.decodeLink(fragmentTarget)

	s.Equal("https://example.org/guide%23install", link.TargetURL.String())
	s.Empty(link.TargetURL.Fragment)
}

func (s *TargetURLSuite) TestSplitFragment() {
	cases := map[string]struct {
		target   string
		wantPath string
		wantFrag string
		wantURL  string
	}{
		"fragment":            {fragmentTarget, "/guide", "install", fragmentTarget},
		"encoded hash sign":   {encodedHashTarget, "/guide#install", "", encodedHashTarget},
		"query and fragment":  {queryFragmentTarget, "/guide", "install", queryFragmentTarget},
		"hash sign and query": {pathQueryFragment, "/a#b", "c?d=1", pathQueryFragment},
		"no hash sign":        {"https://example.org/guide?lang=en", "/guide", "", "https://example.org/guide?lang=en"},
		"host only":           {"https://example.org", "", "", "https://example.org"},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			decoded := s.decodeLink(testCase.target)
			got := client.SplitFragment(decoded.TargetURL)

			s.Equal(testCase.wantPath, got.Path)
			s.Equal(testCase.wantFrag, got.Fragment)
			s.Equal(testCase.wantURL, got.String(), "the address must not change")
		})
	}
}

// TestSplitFragment_KeepsParsedURL checks that a URL that url.Parse made has no change: it has
// its fragment in the right field already.
func (s *TargetURLSuite) TestSplitFragment_KeepsParsedURL() {
	for _, raw := range []string{fragmentTarget, encodedHashTarget, queryFragmentTarget} {
		parsed, err := url.Parse(raw)
		s.Require().NoError(err)

		s.Equal(*parsed, client.SplitFragment(*parsed), raw)
	}
}

// TestRoundTrip sends a link target out through the generated encoder and reads it back through
// the generated decoder, as the CLI and MCP do. The target keeps its address both ways.
func (s *TargetURLSuite) TestRoundTrip() {
	for _, raw := range []string{fragmentTarget, encodedHashTarget, queryFragmentTarget} {
		s.Run(raw, func() {
			parsed, err := url.Parse(raw)
			s.Require().NoError(err)

			// The request side: a URL that url.Parse made is encoded without a change.
			encoded, err := (&client.CreateLinkRequest{
				TargetURL:      *parsed,
				Slug:           client.OptString{Value: "", Set: false},
				BurnAfterClick: client.OptBool{Value: false, Set: false},
				TTL:            client.OptInt64{Value: 0, Set: false},
				EditCode:       client.OptString{Value: "", Set: false},
				Privacy:        client.OptCreateLinkRequestPrivacy{Value: "", Set: false},
				DirectRedirect: client.OptNilBool{Value: false, Set: false, Null: false},
			}).MarshalJSON()
			s.Require().NoError(err)
			s.Contains(string(encoded), `"target_url":"`+raw+`"`)

			// The response side: the real client decodes the body, then SplitFragment repairs it.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"abc","target_url":"` + raw + `","views":0,` +
					`"burn_after_click":false,"created_at":"2026-11-20T10:00:00Z"}`))
			}))
			s.T().Cleanup(server.Close)

			cl, err := client.NewClient(server.URL)
			s.Require().NoError(err)

			res, err := cl.GetLink(s.T().Context(), client.GetLinkParams{ID: "abc"})
			s.Require().NoError(err)

			link, ok := res.(*client.LinkResponse)
			s.Require().True(ok, "unexpected response %T", res)

			got := client.SplitFragment(link.TargetURL)
			s.Equal(raw, got.String())
		})
	}
}
