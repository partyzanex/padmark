package notes

import (
	"errors"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestCreate_ExpiredNoteReleasesSlug covers audit finding 5: an expired note that no one read
// after its expiry holds the slug in storage. Create removes it and tries once more; the file
// manager is not asked, because only one record holds a slug.
func (s *ManagerTestSuite) TestCreate_ExpiredNoteReleasesSlug() {
	releaser := NewMockExpiredFileReleaser(s.ctrl)
	s.manager.WithExpiredFiles(releaser)

	s.expectSlugFree()
	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict),
		s.storage.EXPECT().DeleteExpired(gomock.Any(), domain.HashSlug("report")).Return(nil),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
	)

	note, err := s.manager.Create(s.T().Context(), expiredFileNote())
	s.Require().NoError(err)
	s.Equal("report", note.ID)
	s.Equal("world", note.Content)
}

func (s *ManagerTestSuite) TestCreate_ExpiredNoteReleaseFailures() {
	releaser := NewMockExpiredFileReleaser(s.ctrl)
	s.manager.WithExpiredFiles(releaser)

	s.Run("delete error is fail-soft", func() {
		s.expectSlugFree()
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(errors.New("db down"))
		releaser.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any()).Return(false, nil)

		_, err := s.manager.Create(s.T().Context(), expiredFileNote())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("another record takes the slug before the retry", func() {
		s.expectSlugFree()
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(nil)
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)

		_, err := s.manager.Create(s.T().Context(), expiredFileNote())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("other create error does not release", func() {
		s.expectSlugFree()
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(errors.New("disk full"))

		_, err := s.manager.Create(s.T().Context(), expiredFileNote())
		s.Require().Error(err)
		s.Require().NotErrorIs(err, domain.ErrSlugConflict)
	})
}
