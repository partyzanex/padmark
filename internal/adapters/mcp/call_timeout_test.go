package mcp

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"go.uber.org/mock/gomock"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// TestCallTimeout_LimitsToolsButNotUpload checks which tools get the --timeout deadline:
// get_file (and each other ordinary tool) gets it, upload_file does not, so a large upload is
// limited only by the response header timeout of the HTTP client.
func (s *ToolsSuite) TestCallTimeout_LimitsToolsButNotUpload() {
	const timeout = time.Hour

	root := s.useUploadRoot(WithCallTimeout(timeout))
	s.writeFile(filepath.Join(root, "a.txt"), "x")

	s.Run("get_file has the call deadline", func() {
		s.cl.EXPECT().GetFile(gomock.Any(), padmark.GetFileParams{ID: "file123abc"}).
			DoAndReturn(func(ctx context.Context, _ padmark.GetFileParams) (padmark.GetFileRes, error) {
				deadline, ok := ctx.Deadline()
				s.Require().True(ok, "an ordinary tool call has a deadline")
				s.WithinDuration(time.Now().Add(timeout), deadline, time.Minute)

				return testFileInfo(s, "file123abc"), nil
			})

		res := s.callTool(s.T().Context(), "get_file", map[string]any{"id": "file123abc"})
		s.False(res.IsError)
	})

	s.Run("upload_file has no call deadline", func() {
		s.cl.EXPECT().UploadFile(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, _ string, body io.Reader, _ padmark.UploadFileOptions) (
				*padmark.CreateFileResponse, error,
			) {
				_, ok := ctx.Deadline()
				s.False(ok, "the call deadline would stop a large upload")

				_, err := io.ReadAll(body)
				s.Require().NoError(err)

				return testCreateFileResponse(s, "file123abc", "code-12345678"), nil
			})

		res := s.callTool(s.T().Context(), "upload_file", map[string]any{"path": "a.txt"})
		s.False(res.IsError)
	})
}

// TestCallTimeout_ZeroAddsNoDeadline checks that --timeout 0 turns the call limit off.
func (s *ToolsSuite) TestCallTimeout_ZeroAddsNoDeadline() {
	s.useUploadRoot(WithCallTimeout(0))

	s.cl.EXPECT().GetFile(gomock.Any(), padmark.GetFileParams{ID: "file123abc"}).
		DoAndReturn(func(ctx context.Context, _ padmark.GetFileParams) (padmark.GetFileRes, error) {
			_, ok := ctx.Deadline()
			s.False(ok, "--timeout 0 adds no deadline")

			return testFileInfo(s, "file123abc"), nil
		})

	res := s.callTool(s.T().Context(), "get_file", map[string]any{"id": "file123abc"})
	s.False(res.IsError)
}
