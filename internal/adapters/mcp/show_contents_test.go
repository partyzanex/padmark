package mcp

import (
	"context"

	"go.uber.org/mock/gomock"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// TestCreateNote_ShowContents checks that the tool sends show_contents only when the caller
// sets it, so an old call keeps the server default.
func (s *ToolsSuite) TestCreateNote_ShowContents() {
	ctx := s.T().Context()

	for _, tcase := range []struct {
		args map[string]any
		want padmark.OptBool
	}{
		{map[string]any{"content": "# hi"}, padmark.OptBool{Value: false, Set: false}},
		{map[string]any{"content": "# hi", "show_contents": true}, padmark.NewOptBool(true)},
	} {
		s.cl.EXPECT().CreateNote(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, req *padmark.CreateNoteRequest) (padmark.CreateNoteRes, error) {
				s.Equal(tcase.want, req.ShowContents)

				headers := testCreateNoteHeaders("abc123x999", "secretEC123456")
				headers.Response.ShowContents = tcase.want

				return headers, nil
			})

		res := s.callTool(ctx, "create_note", tcase.args)
		s.Contains(s.successText(res), `"show_contents":`)
	}
}

// TestUpdateNote_ShowContents checks that an omitted show_contents is not sent, so the server
// keeps the stored value.
func (s *ToolsSuite) TestUpdateNote_ShowContents() {
	ctx := s.T().Context()

	for _, tcase := range []struct {
		args map[string]any
		want padmark.OptBool
	}{
		{map[string]any{"id": "abc123x999", "content": "x"}, padmark.OptBool{Value: false, Set: false}},
		{map[string]any{"id": "abc123x999", "content": "x", "show_contents": false}, padmark.NewOptBool(false)},
	} {
		s.cl.EXPECT().UpdateNote(gomock.Any(), gomock.Any(), padmark.UpdateNoteParams{ID: "abc123x999"}).
			DoAndReturn(updateStub(func(req *padmark.UpdateNoteRequest) {
				s.Equal(tcase.want, req.ShowContents)
			}))

		res := s.callTool(ctx, "update_note", tcase.args)
		s.False(res.IsError)
	}
}
