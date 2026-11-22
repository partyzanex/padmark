package links

import (
	"errors"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

func aliasOptions() CreateOptions {
	return CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "report", EditCode: "", BurnAfterClick: false,
	}
}

func (s *ManagerTestSuite) expectAliasFree() {
	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
}

// TestCreate_ExpiredFileReleasesSlug covers R-26: an expired file that the sweep did not remove
// yet holds the slug in storage. Create asks the file manager to remove it and tries once more.
func (s *ManagerTestSuite) TestCreate_ExpiredFileReleasesSlug() {
	releaser := NewMockExpiredFileReleaser(s.ctrl)
	s.manager.WithExpiredFiles(releaser)

	s.expectAliasFree()
	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict),
		s.storage.EXPECT().DeleteExpired(gomock.Any(), domain.HashSlug("report")).Return(domain.ErrNotFound),
		releaser.EXPECT().ReleaseExpiredSlug(gomock.Any(), domain.HashSlug("report")).Return(true, nil),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
	)

	link, err := s.manager.Create(s.T().Context(), "https://example.com", aliasOptions())
	s.Require().NoError(err)
	s.Equal("report", link.ID)
}

// TestCreate_ExpiredLinkReleasesSlug covers an expired link that took the slug after the preflight
// check: Create removes it through the conditional DeleteExpired, evicts its cached target and
// tries once more. The file manager is not asked, because one record at most holds a slug.
func (s *ManagerTestSuite) TestCreate_ExpiredLinkReleasesSlug() {
	releaser := NewMockExpiredFileReleaser(s.ctrl)
	cache := NewMockCache(s.ctrl)
	s.manager.WithExpiredFiles(releaser).WithCache(cache)

	s.expectAliasFree()
	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict),
		s.storage.EXPECT().DeleteExpired(gomock.Any(), domain.HashSlug("report")).Return(nil),
		cache.EXPECT().Delete(domain.HashSlug("report")),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
	)

	link, err := s.manager.Create(s.T().Context(), "https://example.com", aliasOptions())
	s.Require().NoError(err)
	s.Equal("report", link.ID)
}

func (s *ManagerTestSuite) TestCreate_SlugConflictWithoutExpiredFile() {
	releaser := NewMockExpiredFileReleaser(s.ctrl)
	s.manager.WithExpiredFiles(releaser)

	s.Run("live resource", func() {
		s.expectAliasFree()
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(domain.ErrNotFound)
		releaser.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any()).Return(false, nil)

		_, err := s.manager.Create(s.T().Context(), "https://example.com", aliasOptions())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("release error is fail-soft", func() {
		s.expectAliasFree()
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(errors.New("db down"))
		releaser.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any()).Return(false, errors.New("s3 down"))

		_, err := s.manager.Create(s.T().Context(), "https://example.com", aliasOptions())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})
}
