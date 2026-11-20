package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/mock/gomock"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// The file tool tests are ToolsSuite methods: they share its mock client and its MCP session.

func (s *ToolsSuite) TestUploadFile_SendsContentAndHidesEditCode() {
	ctx := s.T().Context()
	root := s.useUploadRoot()
	s.writeFile(filepath.Join(root, "report.txt"), "hello")

	noOptions := padmark.UploadFileOptions{TTL: 0, Privacy: "", Slug: "", EditCode: "", BurnAfterDownload: false}
	s.files.EXPECT().UploadFile(gomock.Any(), "report.txt", gomock.Any(), noOptions).
		DoAndReturn(func(_ context.Context, _ string, body io.Reader, _ padmark.UploadFileOptions) (
			*padmark.CreateFileResponse, error,
		) {
			data, err := io.ReadAll(body)
			s.Require().NoError(err)
			s.Equal("hello", string(data), "the tool streams the file content to the client")

			return testCreateFileResponse(s, "file123abc", "secretFC123456"), nil
		})

	out := s.successText(s.callTool(ctx, "upload_file", map[string]any{"path": "report.txt"}))

	s.Contains(out, `"url":"https://pad.example.com/file123abc"`)
	s.Contains(out, `"download_url":"https://pad.example.com/files/file123abc/download"`)
	s.NotContains(out, "secretFC123456", "the one-time edit code must not be printed by default")
	s.Contains(out, `"edit_code_note"`)
	s.Equal("secretFC123456", s.mgr.vault.Get("file123abc"))
}

func (s *ToolsSuite) TestUploadFile_PassesOptionsAndRevealsEditCode() {
	ctx := s.T().Context()
	root := s.useUploadRoot()
	s.Require().NoError(os.Mkdir(filepath.Join(root, "sub"), 0o700))
	s.writeFile(filepath.Join(root, "sub", "a.bin"), "x")

	want := padmark.UploadFileOptions{
		TTL: time.Minute, Privacy: "owner", Slug: "my-file", EditCode: "custom-code-1", BurnAfterDownload: true,
	}
	s.files.EXPECT().UploadFile(gomock.Any(), "renamed.bin", gomock.Any(), want).
		Return(testCreateFileResponse(s, "my-file", "custom-code-1"), nil)

	out := s.successText(s.callTool(ctx, "upload_file", map[string]any{
		"path": filepath.Join(root, "sub", "a.bin"), "name": "renamed.bin", "ttl": 60, "privacy": "owner",
		"slug": "my-file", "edit_code": "custom-code-1", "burn_after_download": true, "reveal_edit_code": true,
	}))

	s.Contains(out, `"edit_code":"custom-code-1"`)
}

func (s *ToolsSuite) TestUploadFile_SymlinkInsideRootKeepsLinkName() {
	ctx := s.T().Context()
	root := s.useUploadRoot()
	s.writeFile(filepath.Join(root, "blob-1"), "data")
	s.Require().NoError(os.Symlink(filepath.Join(root, "blob-1"), filepath.Join(root, "report.pdf")))

	s.files.EXPECT().UploadFile(gomock.Any(), "report.pdf", gomock.Any(), gomock.Any()).
		Return(testCreateFileResponse(s, "file123abc", "code-12345678"), nil)

	res := s.callTool(ctx, "upload_file", map[string]any{"path": "report.pdf"})

	s.False(res.IsError, "a link to a file under the root is permitted")
}

// TestUploadFile_RefusesPaths covers the local checks. No case may reach the server: the mock
// has no UploadFile expectation, so a call fails the test.
func (s *ToolsSuite) TestUploadFile_RefusesPaths() {
	root := s.useUploadRoot()
	outside := s.T().TempDir()
	s.writeFile(filepath.Join(outside, "secret.txt"), "secret")
	s.Require().NoError(os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")))
	s.Require().NoError(os.Mkdir(filepath.Join(root, "dir"), 0o700))

	cases := map[string]struct {
		path string
		want string
	}{
		"absolute path outside the root": {path: filepath.Join(outside, "secret.txt"), want: "outside the upload root"},
		"relative path out of the root":  {path: filepath.Join("..", filepath.Base(outside), "secret.txt"), want: "outside"},
		"link to a file outside":         {path: "link.txt", want: "outside the upload root"},
		"directory":                      {path: "dir", want: "not a regular file"},
		"root itself":                    {path: root, want: "not a regular file"},
		"missing file":                   {path: "missing.txt", want: "no such file"},
		"empty path":                     {path: " ", want: "path must not be empty"},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			res := s.callTool(s.T().Context(), "upload_file", map[string]any{"path": testCase.path})

			s.Contains(s.errorTextOf(res), testCase.want)
		})
	}
}

func (s *ToolsSuite) TestUploadFile_OffWithoutUploadRoot() {
	file := filepath.Join(s.T().TempDir(), "a.txt")
	s.writeFile(file, "x")

	res := s.callTool(s.T().Context(), "upload_file", map[string]any{"path": file})

	s.Contains(s.errorTextOf(res), "--upload-root")
}

func (s *ToolsSuite) TestUploadFile_MapsServerErrors() {
	root := s.useUploadRoot()
	s.writeFile(filepath.Join(root, "a.txt"), "x")

	cases := map[string]struct {
		err  error
		want string
	}{
		"413 too large": {
			err: &padmark.APIError{
				Message: "file too large (limit 10 bytes)", Code: "", StatusCode: http.StatusRequestEntityTooLarge,
			},
			want: "content too large: file too large (limit 10 bytes)",
		},
		"503 files off": {
			err:  &padmark.APIError{Message: "feature not supported", Code: "", StatusCode: http.StatusServiceUnavailable},
			want: "file storage is off or not available",
		},
		"503 API disabled": {
			err:  &padmark.APIError{Message: "x", Code: "api_disabled", StatusCode: http.StatusServiceUnavailable},
			want: "API is disabled on this server",
		},
		"409 slug in use": {
			err:  &padmark.APIError{Message: "conflict", Code: "", StatusCode: http.StatusConflict},
			want: "slug is already in use",
		},
		"transport": {
			err:  errors.New("connection refused"),
			want: "is padmark-server running",
		},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			s.files.EXPECT().UploadFile(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).Return(nil, testCase.err)

			res := s.callTool(s.T().Context(), "upload_file", map[string]any{"path": "a.txt"})

			s.Contains(s.errorTextOf(res), testCase.want)
		})
	}
}

func (s *ToolsSuite) TestGetFile_ReturnsMetadata() {
	ctx := s.T().Context()

	s.files.EXPECT().GetFile(gomock.Any(), padmark.GetFileParams{ID: "file123abc"}).
		Return(testFileInfo(s, "file123abc"), nil)

	res := s.callTool(ctx, "get_file", map[string]any{"id": "file123abc"})

	out := s.successText(res)
	s.Contains(out, `"name":"report.txt"`)
	s.Contains(out, `"size":5`)
	s.Contains(out, `"download_url":"https://pad.example.com/files/file123abc/download"`)
}

func (s *ToolsSuite) TestGetFile_MapsErrors() {
	cases := map[string]struct {
		res  padmark.GetFileRes
		want string
	}{
		"not found": {res: &padmark.GetFileNotFound{Message: "", Code: noCode()}, want: "file not found"},
		"files off": {res: &padmark.GetFileServiceUnavailable{Message: "", Code: noCode()}, want: "file storage is off"},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			s.files.EXPECT().GetFile(gomock.Any(), gomock.Any()).Return(testCase.res, nil)

			res := s.callTool(s.T().Context(), "get_file", map[string]any{"id": "file123abc"})

			s.Contains(s.errorTextOf(res), testCase.want)
		})
	}
}

func (s *ToolsSuite) TestUpdateFile_ClearsExpiryWithVaultCode() {
	ctx := s.T().Context()
	s.mgr.vault.Put("file123abc", "vault-code-01")

	params := padmark.UpdateFileParams{ID: "file123abc", XEditCode: padmark.NewOptString("vault-code-01")}
	s.files.EXPECT().UpdateFile(gomock.Any(), gomock.Any(), params).
		DoAndReturn(func(_ context.Context, req *padmark.UpdateFileRequest, _ padmark.UpdateFileParams) (
			padmark.UpdateFileRes, error,
		) {
			s.True(req.TTL.Set && req.TTL.Null, "clear_expiry sends ttl: null")
			s.Equal(padmark.NewOptUpdateFileRequestPrivacy("authenticated"), req.Privacy)
			s.False(req.BurnAfterDownload.Set, "an absent field keeps the current value")

			return testFileInfo(s, "file123abc"), nil
		})

	res := s.callTool(ctx, "update_file", map[string]any{
		"id": "file123abc", "clear_expiry": true, "privacy": "authenticated",
	})

	s.False(res.IsError, s.successText(res))
}

func (s *ToolsSuite) TestUpdateFile_SendsTTLAndExplicitCode() {
	ctx := s.T().Context()
	s.mgr.vault.Put("file123abc", "vault-code-01")

	params := padmark.UpdateFileParams{ID: "file123abc", XEditCode: padmark.NewOptString("explicit-code")}
	s.files.EXPECT().UpdateFile(gomock.Any(), gomock.Any(), params).
		DoAndReturn(func(_ context.Context, req *padmark.UpdateFileRequest, _ padmark.UpdateFileParams) (
			padmark.UpdateFileRes, error,
		) {
			s.Equal(padmark.NewOptNilInt64(3600), req.TTL)
			s.Equal(padmark.NewOptBool(false), req.BurnAfterDownload)

			return testFileInfo(s, "file123abc"), nil
		})

	res := s.callTool(ctx, "update_file", map[string]any{
		"id": "file123abc", "ttl": 3600, "burn_after_download": false, "edit_code": "explicit-code",
	})

	s.False(res.IsError)
}

// TestUpdateFile_RefusesBadInput checks the local input checks; no case reaches the server.
func (s *ToolsSuite) TestUpdateFile_RefusesBadInput() {
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"nothing to change":    {args: map[string]any{"id": "f"}, want: "nothing to change"},
		"ttl and clear_expiry": {args: map[string]any{"id": "f", "ttl": 1, "clear_expiry": true}, want: "not both"},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			res := s.callTool(s.T().Context(), "update_file", testCase.args)

			s.Contains(s.errorTextOf(res), testCase.want)
		})
	}
}

func (s *ToolsSuite) TestUpdateFile_MapsForbidden() {
	s.files.EXPECT().UpdateFile(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&padmark.UpdateFileForbidden{Message: "", Code: noCode()}, nil)

	res := s.callTool(s.T().Context(), "update_file", map[string]any{"id": "f", "privacy": "public"})

	s.Contains(s.errorTextOf(res), "wrong edit_code for the file")
}

func (s *ToolsSuite) TestDeleteFile_WithoutCodeSendsNoHeader() {
	params := padmark.DeleteFileParams{ID: "owned-file", XEditCode: padmark.OptString{Value: "", Set: false}}
	s.files.EXPECT().DeleteFile(gomock.Any(), params).Return(&padmark.DeleteFileNoContent{}, nil)

	res := s.callTool(s.T().Context(), "delete_file", map[string]any{"id": "owned-file"})

	s.Contains(s.successText(res), `"deleted":true`, "the bearer token of the owner authorizes the delete")
}

func (s *ToolsSuite) TestDeleteFile_MapsErrors() {
	cases := map[string]struct {
		res  padmark.DeleteFileRes
		want string
	}{
		"not found": {res: &padmark.DeleteFileNotFound{Message: "", Code: noCode()}, want: "file not found"},
		"forbidden": {res: &padmark.DeleteFileForbidden{Message: "", Code: noCode()}, want: "wrong edit_code for the file"},
		"files off": {res: &padmark.DeleteFileServiceUnavailable{Message: "", Code: noCode()}, want: "file storage is off"},
		"server": {
			res:  &padmark.DeleteFileInternalServerError{Message: "", Code: noCode()},
			want: "padmark server error",
		},
	}

	for name, testCase := range cases {
		s.Run(name, func() {
			s.files.EXPECT().DeleteFile(gomock.Any(), gomock.Any()).Return(testCase.res, nil)

			res := s.callTool(s.T().Context(), "delete_file", map[string]any{"id": "f"})

			s.Contains(s.errorTextOf(res), testCase.want)
		})
	}
}

// --- helpers ---

// noCode returns an absent machine-readable error code.
func noCode() padmark.OptString {
	return padmark.OptString{Value: "", Set: false}
}

// useUploadRoot rebuilds the manager with a new temporary upload root and returns the root.
func (s *ToolsSuite) useUploadRoot() string {
	root := s.T().TempDir()
	s.mgr = NewManager(s.cl, s.files, s.log, WithUploadRoot(root))

	return root
}

func (s *ToolsSuite) writeFile(path, content string) {
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))
}

// testCreateFileResponse builds the 201 body of an upload with a one-time edit code.
func testCreateFileResponse(s *ToolsSuite, id, editCode string) *padmark.CreateFileResponse {
	info := testFileInfo(s, id)

	return &padmark.CreateFileResponse{
		ID: info.ID, URL: info.URL, Name: info.Name, Size: info.Size,
		Privacy:   padmark.CreateFileResponsePrivacy(info.Privacy),
		Downloads: info.Downloads, BurnAfterDownload: info.BurnAfterDownload,
		ExpiresAt: info.ExpiresAt, CreatedAt: info.CreatedAt, EditCode: editCode,
	}
}

// testFileInfo builds the metadata of a small public file.
func testFileInfo(s *ToolsSuite, id string) *padmark.FileInfo {
	return &padmark.FileInfo{
		ID: id, URL: testURL(s.T(), "https://pad.example.com/"+id), Name: "report.txt", Size: 5,
		Privacy: padmark.FileInfoPrivacyPublic, Downloads: 0, BurnAfterDownload: false,
		ExpiresAt: padmark.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
		CreatedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
}
