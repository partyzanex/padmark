package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/mock/gomock"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// The link tools must give the agent the target address that the server sent. The mock client
// returns responses that the generated ogen decoder made from JSON, as the real client does.
// That decoder leaves a "#fragment" in the path, so String would write "%23" without a repair.

// linkTargets returns the link targets for the tests. The addresses differ only in "#" and "%23".
func linkTargets() []string {
	return []string{
		"https://example.org/guide#install",
		"https://example.org/guide%23install",
		"https://example.org/guide?lang=en#install",
		"https://example.org/a%23b#c",
	}
}

func (s *ToolsSuite) TestGetLink_KeepsTargetAddress() {
	for _, target := range linkTargets() {
		s.Run(target, func() {
			var link padmark.LinkResponse

			s.Require().NoError(link.UnmarshalJSON([]byte(s.linkJSON(target))))
			s.cl.EXPECT().GetLink(gomock.Any(), padmark.GetLinkParams{ID: "abc"}).Return(&link, nil)

			res := s.callTool(s.T().Context(), "get_link", map[string]any{"id": "abc"})

			s.Equal(target, s.targetOf(s.successText(res)))
		})
	}
}

func (s *ToolsSuite) TestCreateLink_KeepsTargetAddress() {
	for _, target := range linkTargets() {
		s.Run(target, func() {
			var created padmark.CreateLinkResponse

			s.Require().NoError(created.UnmarshalJSON([]byte(s.linkJSON(target))))

			s.cl.EXPECT().CreateLink(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, req *padmark.CreateLinkRequest) (padmark.CreateLinkRes, error) {
					// The request side: the address that the agent gave reaches the encoder without a change.
					sent := req.TargetURL
					s.Equal(target, sent.String())

					return &padmark.CreateLinkResponseHeaders{
						Response: created, Location: padmark.OptString{Value: "", Set: false},
					}, nil
				})

			res := s.callTool(s.T().Context(), "create_link", map[string]any{"target_url": target})

			s.Equal(target, s.targetOf(s.successText(res)))
		})
	}
}

func (s *ToolsSuite) TestCreateLinksBulk_KeepsTargetAddress() {
	targets := linkTargets()

	var items []string

	for idx, target := range targets {
		items = append(items, fmt.Sprintf(`{"target_url":%s,"id":"link-%d","edit_code":"code-%d"}`,
			s.jsonString(target), idx, idx))
	}

	var bulk padmark.BulkCreateLinksResponse

	s.Require().NoError(bulk.UnmarshalJSON([]byte(`{"results":[` + strings.Join(items, ",") + `]}`)))

	s.cl.EXPECT().CreateLinksBulk(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *padmark.BulkCreateLinksRequest) (padmark.CreateLinksBulkRes, error) {
			s.Require().Len(req.Links, len(targets))

			for idx := range req.Links {
				sent := req.Links[idx].TargetURL
				s.Equal(targets[idx], sent.String(), "request item %d", idx)
			}

			return &bulk, nil
		})

	res := s.callTool(s.T().Context(), "create_links_bulk", map[string]any{"target_urls": targets})

	var out struct {
		Results []struct {
			TargetURL string `json:"target_url"`
		} `json:"results"`
	}

	s.Require().NoError(json.Unmarshal([]byte(s.successText(res)), &out))
	s.Require().Len(out.Results, len(targets))

	for idx, target := range targets {
		s.Equal(target, out.Results[idx].TargetURL, "result item %d", idx)
	}
}

// targetOf reads target_url from the JSON text of a link tool result.
func (s *ToolsSuite) targetOf(text string) string {
	s.T().Helper()

	var out struct {
		TargetURL string `json:"target_url"`
	}

	s.Require().NoError(json.Unmarshal([]byte(text), &out), text)

	return out.TargetURL
}

// --- helpers ---

// linkJSON is the JSON of a link response with the given target.
func (s *ToolsSuite) linkJSON(target string) string {
	return `{"id":"abc","target_url":` + s.jsonString(target) + `,"views":0,"burn_after_click":false,` +
		`"created_at":"2026-11-20T10:00:00Z","edit_code":"code-12345678"}`
}

// jsonString writes value as a JSON string.
func (s *ToolsSuite) jsonString(value string) string {
	s.T().Helper()

	quoted, err := json.Marshal(value)
	s.Require().NoError(err)

	return string(quoted)
}
