package notes

import (
	"errors"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

func customSlugNote() *domain.Note {
	return &domain.Note{
		Title: "hello", Content: "world",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "report", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false, ShowContents: nil,
	}
}

// TestCreate_CustomSlugReleasesExpiredHolder covers audit finding 5 and R-26: an expired note,
// link or file that no read and no sweep removed still holds the slug in storage. Create asks
// the release port to remove it and tries once more with the same slug.
func (s *ManagerTestSuite) TestCreate_CustomSlugReleasesExpiredHolder() {
	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict),
		s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), domain.HashSlug("report"), testNow()).Return(true, nil),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
	)

	note, err := s.manager.Create(s.T().Context(), customSlugNote())
	s.Require().NoError(err)
	s.Equal("report", note.ID)
	s.Equal("world", note.Content)
}

func (s *ManagerTestSuite) TestCreate_CustomSlugConflict() {
	s.Run("live holder", func() {
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any(), testNow()).Return(false, nil)

		_, err := s.manager.Create(s.T().Context(), customSlugNote())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("release error is fail-soft", func() {
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any(), testNow()).Return(false, errors.New("db down"))

		_, err := s.manager.Create(s.T().Context(), customSlugNote())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("another record takes the slug before the second attempt", func() {
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
		s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any(), testNow()).Return(true, nil)
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)

		_, err := s.manager.Create(s.T().Context(), customSlugNote())
		s.Require().ErrorIs(err, domain.ErrSlugConflict)
	})

	s.Run("other create error does not release", func() {
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(errors.New("disk full"))

		_, err := s.manager.Create(s.T().Context(), customSlugNote())
		s.Require().Error(err)
		s.Require().NotErrorIs(err, domain.ErrSlugConflict)
	})
}

// TestCreate_RandomSlugRetries: a random slug that storage refuses gets a new random slug. The
// release port is not asked: a new slug is cheaper than a removal.
func (s *ManagerTestSuite) TestCreate_RandomSlugRetries() {
	var ids []string

	record := func(_ any, note *domain.Note) { ids = append(ids, note.ID) }

	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Do(record).Return(domain.ErrSlugConflict),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Do(record).Return(domain.ErrSlugConflict),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Do(record).Return(nil),
	)

	note := customSlugNote()
	note.ID = ""

	created, err := s.manager.Create(s.T().Context(), note)
	s.Require().NoError(err)
	s.Require().Len(ids, 3)
	s.NotEqual(ids[0], ids[1])
	s.NotEqual(ids[1], ids[2])
	s.Equal(domain.HashSlug(created.ID), ids[2])
}

func (s *ManagerTestSuite) TestCreate_RandomSlugRetriesEnd() {
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict).Times(maxSlugRetries)

	note := customSlugNote()
	note.ID = ""

	_, err := s.manager.Create(s.T().Context(), note)
	s.Require().ErrorIs(err, domain.ErrHashCollision)
}
