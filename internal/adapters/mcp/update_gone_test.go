package mcp

import (
	"go.uber.org/mock/gomock"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// TestUpdate_GoneRecord: the server refuses an update of an expired record with 410. Each update
// tool reports "gone" with a text that tells the model to create a new record.
func (s *ToolsSuite) TestUpdate_GoneRecord() {
	expired := padmark.OptString{Value: "", Set: false}

	s.Run("note", func() {
		s.cl.EXPECT().UpdateNote(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&padmark.UpdateNoteGone{Message: "expired", Code: expired}, nil)

		res := s.callTool(s.T().Context(), "update_note", map[string]any{
			"id": "old-note", "content": "new", "edit_code": "secret-code",
		})

		s.Contains(s.errorTextOf(res), "gone: the note has expired and cannot be changed; create a new note")
	})

	s.Run("link", func() {
		s.cl.EXPECT().UpdateLink(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&padmark.UpdateLinkGone{Message: "expired", Code: expired}, nil)

		res := s.callTool(s.T().Context(), "update_link", map[string]any{
			"id": "old-link", "target_url": "https://x.example", "edit_code": "secret-code",
		})

		s.Contains(s.errorTextOf(res), "gone: the link has expired and cannot be changed; create a new link")
	})

	s.Run("file", func() {
		s.cl.EXPECT().UpdateFile(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&padmark.UpdateFileGone{Message: "expired", Code: expired}, nil)

		res := s.callTool(s.T().Context(), "update_file", map[string]any{
			"id": "old-file", "clear_expiry": true, "edit_code": "secret-code",
		})

		s.Contains(s.errorTextOf(res), "gone: expired")
	})
}
