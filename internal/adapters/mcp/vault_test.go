package mcp

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func TestVaultPutGet_RoundTrip(t *testing.T) {
	vault := newEditCodeVault()

	vault.Put("note-a", "code-a")

	assert.Equal(t, "code-a", vault.Get("note-a"))
	assert.Empty(t, vault.Get("note-b"), "entries are isolated per id")
	assert.Empty(t, vault.Get(""), "an empty id is just another key, not a wildcard")
}

func TestVault_ConcurrentAccessIsRaceClean(_ *testing.T) {
	vault := newEditCodeVault()

	var wg sync.WaitGroup

	// Handlers may create and delete notes concurrently on the same vault.
	for range 50 {
		wg.Go(func() {
			vault.Put("shared", "code")
			vault.Get("shared")
			vault.Get("missing")
		})
	}

	wg.Wait()
}

// TestDeleteFile_UsesSessionVaultCode uploads a file and deletes it without an edit_code: the
// proxy sends the code that it stored at the upload. It is a ToolsSuite method (tools_test.go).
func (s *ToolsSuite) TestDeleteFile_UsesSessionVaultCode() {
	ctx := s.T().Context()
	root := s.useUploadRoot()
	s.writeFile(filepath.Join(root, "a.txt"), "x")

	s.files.EXPECT().UploadFile(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).
		Return(testCreateFileResponse(s, "file123abc", "session-code-1"), nil)
	s.files.EXPECT().DeleteFile(gomock.Any(), padmark.DeleteFileParams{
		ID: "file123abc", XEditCode: padmark.NewOptString("session-code-1"),
	}).Return(&padmark.DeleteFileNoContent{}, nil)

	uploaded := s.callTool(ctx, "upload_file", map[string]any{"path": "a.txt"})
	s.Require().False(uploaded.IsError, s.successText(uploaded))

	deleted := s.callTool(ctx, "delete_file", map[string]any{"id": "file123abc"})

	s.Contains(s.successText(deleted), `"deleted":true`)
}
