//go:build integration

package integration

import (
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// contentsNote builds a create or update payload; nil showContents is an old client.
func contentsNote(content string, showContents *bool) *domain.Note {
	return &domain.Note{
		Content:   content,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", Title: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false, ShowContents: showContents,
	}
}

// TestShowContentsBackwardCompatibility runs the old-client contract against the real storage:
// a create without the option stores false, and an update without it keeps the stored value.
func (s *ManagerSuite) TestShowContentsBackwardCompatibility() {
	ctx := s.T().Context()

	old, err := s.Manager.Create(ctx, contentsNote("old client", nil))
	s.Require().NoError(err)

	stored, err := s.Manager.Peek(ctx, old.ID)
	s.Require().NoError(err)
	s.False(stored.ContentsSidebar(), "a create without show_contents stores false")

	created, err := s.Manager.Create(ctx, contentsNote("## Part", new(true)))
	s.Require().NoError(err)

	updated, err := s.Manager.Update(ctx, created.ID, created.EditCode, uuid.Nil, contentsNote("## Changed", nil))
	s.Require().NoError(err)
	s.True(updated.ContentsSidebar(), "the response reports the kept value")

	stored, err = s.Manager.Peek(ctx, created.ID)
	s.Require().NoError(err)
	s.True(stored.ContentsSidebar(), "an update without show_contents keeps true")

	_, err = s.Manager.Update(ctx, created.ID, created.EditCode, uuid.Nil, contentsNote("## Off", new(false)))
	s.Require().NoError(err)

	stored, err = s.Manager.Peek(ctx, created.ID)
	s.Require().NoError(err)
	s.False(stored.ContentsSidebar(), "an explicit false turns the sidebar off")
}
