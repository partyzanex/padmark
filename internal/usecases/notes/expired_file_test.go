package notes

import (
	"errors"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

func expiredFileNote() *domain.Note {
	return &domain.Note{
		Title: "hello", Content: "world",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "report", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false, ShowContents: nil,
	}
}

// TestCreate_ExpiredFileReleasesSlug covers R-26: an expired file that the sweep did not remove
// yet holds the slug in storage. Create asks the file manager to remove it and tries once more.
func (s *ManagerTestSuite) TestCreate_ExpiredFileReleasesSlug() {
	releaser := NewMockExpiredFileReleaser(s.ctrl)
	s.manager.WithExpiredFiles(releaser)

	s.expectSlugFree()
	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict),
		releaser.EXPECT().ReleaseExpiredSlug(gomock.Any(), domain.HashSlug("report")).Return(true, nil),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
	)

	note, err := s.manager.Create(s.T().Context(), expiredFileNote())
	s.Require().NoError(err)
	s.Equal("report", note.ID)
}

func (s *ManagerTestSuite) TestCreate_SlugConflictWithoutExpiredFile() {
	releaser := NewMockExpiredFileReleaser(s.ctrl)
	s.manager.WithExpiredFiles(releaser)

	s.Run("live resource", func() {
		s.expectSlugFree()
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		releaser.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any()).Return(false, nil)

		_, err := s.manager.Create(s.T().Context(), expiredFileNote())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("release error is fail-soft", func() {
		s.expectSlugFree()
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		releaser.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any()).Return(false, errors.New("s3 down"))

		_, err := s.manager.Create(s.T().Context(), expiredFileNote())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("default releaser", func() {
		s.manager.WithExpiredFiles(noExpiredFiles{})
		s.expectSlugFree()
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)

		_, err := s.manager.Create(s.T().Context(), expiredFileNote())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})
}
