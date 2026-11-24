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

// TestCreate_AliasReleasesExpiredHolder covers R-26 and audit finding 5: an expired note, link or
// file that no read and no sweep removed still holds the alias in storage. Create asks the
// release port to remove it, evicts a cached target of that ID and tries once more.
func (s *ManagerTestSuite) TestCreate_AliasReleasesExpiredHolder() {
	s.manager.WithCache(s.cache)

	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict),
		s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), domain.HashSlug("report"), testNow()).Return(true, nil),
		s.cache.EXPECT().Delete(domain.HashSlug("report")),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
	)

	link, err := s.manager.Create(s.T().Context(), "https://example.com", aliasOptions())
	s.Require().NoError(err)
	s.Equal("report", link.ID)
}

// TestCreate_AliasConflictWithoutExpiredHolder_RecordsNoCollision: a custom alias that no expired
// record holds stays taken. A refused alias is normal user behavior, not a signal about the slug
// space, so no subtest increments IncCollisions: the metrics mock has no expectation.
func (s *ManagerTestSuite) TestCreate_AliasConflictWithoutExpiredHolder_RecordsNoCollision() {
	s.manager.WithMetrics(s.metrics)

	s.Run("live holder", func() {
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), domain.HashSlug("report"), testNow()).Return(false, nil)

		_, err := s.manager.Create(s.T().Context(), "https://example.com", aliasOptions())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("release error is fail-soft", func() {
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any(), testNow()).Return(false, errors.New("db down"))

		_, err := s.manager.Create(s.T().Context(), "https://example.com", aliasOptions())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("another record takes the alias before the second attempt", func() {
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any(), testNow()).Return(true, nil)
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)

		_, err := s.manager.Create(s.T().Context(), "https://example.com", aliasOptions())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})
}
