package mcp

import (
	"context"
	"time"

	"go.uber.org/mock/gomock"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// testLinkResponse is a LinkResponse of a link with id that goes to target.
func (s *ToolsSuite) testLinkResponse(id, target string) *padmark.LinkResponse {
	return &padmark.LinkResponse{
		ID:             id,
		TargetURL:      testURL(s.T(), target),
		Views:          3,
		BurnAfterClick: false,
		Privacy:        padmark.OptLinkResponsePrivacy{Value: "", Set: false},
		ExpiresAt:      padmark.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
		DirectRedirect: padmark.OptNilBool{Value: false, Set: false, Null: false},
		CreatedAt:      time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
}

func (s *ToolsSuite) TestUpdateLink_UsesVaultEditCodeAndKeepsUnsetFields() {
	s.mgr.vault.Put("link-1", "vault-code-01")

	s.cl.EXPECT().UpdateLink(gomock.Any(), gomock.Any(), padmark.UpdateLinkParams{ID: "link-1"}).
		DoAndReturn(func(
			_ context.Context, req *padmark.UpdateLinkRequest, _ padmark.UpdateLinkParams,
		) (padmark.UpdateLinkRes, error) {
			s.Equal("https://new.example/path#part", req.TargetURL.String())
			s.Equal(padmark.NewOptString("vault-code-01"), req.EditCode, "no explicit edit_code ⇒ the vault code is sent")
			s.Equal(padmark.NewOptNilInt64(0), req.TTL, "ttl 0 is sent: it expires the link now")
			s.False(req.Privacy.Set, "an unset privacy keeps the stored value")
			s.False(req.DirectRedirect.Set, "an unset direct_redirect keeps the stored value")

			return s.testLinkResponse("link-1", "https://new.example/path#part"), nil
		})

	res := s.callTool(s.T().Context(), "update_link", map[string]any{
		"id": "link-1", "target_url": "https://new.example/path#part", "ttl": 0,
	})

	out := s.successText(res)
	s.Contains(out, `"id":"link-1"`)
	s.Contains(out, `"target_url":"https://new.example/path#part"`)
	s.NotContains(out, "vault-code-01")
}

func (s *ToolsSuite) TestUpdateLink_ExplicitFields() {
	s.mgr.vault.Put("link-1", "vault-code-01")

	s.cl.EXPECT().UpdateLink(gomock.Any(), gomock.Any(), padmark.UpdateLinkParams{ID: "link-1"}).
		DoAndReturn(func(
			_ context.Context, req *padmark.UpdateLinkRequest, _ padmark.UpdateLinkParams,
		) (padmark.UpdateLinkRes, error) {
			s.Equal(padmark.NewOptString("explicit-code"), req.EditCode, "an explicit edit_code beats the vault")
			s.Equal(padmark.NewOptUpdateLinkRequestPrivacy(padmark.UpdateLinkRequestPrivacyOwner), req.Privacy)
			s.Equal(padmark.NewOptNilBool(true), req.DirectRedirect)
			s.Equal(padmark.NewOptNilInt64(3600), req.TTL)

			return s.testLinkResponse("link-1", "https://new.example"), nil
		})

	res := s.callTool(s.T().Context(), "update_link", map[string]any{
		"id": "link-1", "target_url": "https://new.example", "edit_code": "explicit-code",
		"privacy": "owner", "direct_redirect": true, "ttl": 3600,
	})

	s.False(res.IsError)
}

func (s *ToolsSuite) TestUpdateLink_RefusesInputBeforeServerCall() {
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"bad target": {args: map[string]any{"id": "l", "target_url": "not a url"}, want: errInvalidTargetURL.Error()},
		"negative ttl": {
			args: map[string]any{"id": "l", "target_url": "https://x.example", "ttl": -1}, want: errLinkTTLRange.Error(),
		},
		"too large ttl": {
			args: map[string]any{"id": "l", "target_url": "https://x.example", "ttl": padmark.MaxTTLSeconds + 1},
			want: errLinkTTLRange.Error(),
		},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			res := s.callTool(s.T().Context(), "update_link", testCase.args)

			s.Contains(s.errorTextOf(res), testCase.want)
		})
	}
}

func (s *ToolsSuite) TestUpdateLink_MapsErrors() {
	cases := map[string]struct {
		res  padmark.UpdateLinkRes
		want string
	}{
		"wrong edit code": {
			res:  &padmark.UpdateLinkForbidden{Message: "wrong", Code: padmark.OptString{Value: "", Set: false}},
			want: "decode update link response: forbidden: wrong edit_code for the link",
		},
		"missing link": {
			res:  &padmark.UpdateLinkNotFound{Message: "x", Code: padmark.OptString{Value: "", Set: false}},
			want: "link not found",
		},
		"no credential": {res: &padmark.UpdateLinkUnauthorized{}, want: "unauthorized: the server needs a bearer token"},
		"validation": {
			res: &padmark.UpdateLinkUnprocessableEntity{
				Message: "owner needs an owner", Code: padmark.OptString{Value: "", Set: false},
			},
			want: "validation error: owner needs an owner",
		},
		"rate limit": {
			res:  &padmark.UpdateLinkTooManyRequests{Message: "x", Code: padmark.OptString{Value: "", Set: false}},
			want: "rate limit or failure lockout is active on link updates",
		},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			s.cl.EXPECT().UpdateLink(gomock.Any(), gomock.Any(), gomock.Any()).Return(testCase.res, nil)

			res := s.callTool(s.T().Context(), "update_link", map[string]any{"id": "l", "target_url": "https://x.example"})

			s.Contains(s.errorTextOf(res), testCase.want)
		})
	}
}
