package expiredslugs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

type ReleaserSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	notes    *MockRecordExpirer
	links    *MockRecordExpirer
	files    *MockFileReleaser
	releaser *Releaser
}

func TestReleaserSuite(t *testing.T) {
	suite.Run(t, new(ReleaserSuite))
}

func (s *ReleaserSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.notes = NewMockRecordExpirer(s.ctrl)
	s.links = NewMockRecordExpirer(s.ctrl)
	s.files = NewMockFileReleaser(s.ctrl)
	s.releaser = New(s.notes, s.links).WithFiles(s.files)
}

func (s *ReleaserSuite) TearDownTest() {
	s.ctrl.Finish()
}

// TestFirstRemovalEnds: one record at most holds a slug, so a removed note ends the search.
func (s *ReleaserSuite) TestFirstRemovalEnds() {
	s.notes.EXPECT().DeleteExpired(gomock.Any(), "id").Return(nil)

	released, err := s.releaser.ReleaseExpiredSlug(s.T().Context(), "id")
	s.Require().NoError(err)
	s.True(released)
}

func (s *ReleaserSuite) TestLinkThenFile() {
	s.Run("link", func() {
		s.notes.EXPECT().DeleteExpired(gomock.Any(), "id").Return(domain.ErrNotFound)
		s.links.EXPECT().DeleteExpired(gomock.Any(), "id").Return(nil)

		released, err := s.releaser.ReleaseExpiredSlug(s.T().Context(), "id")
		s.Require().NoError(err)
		s.True(released)
	})

	s.Run("file", func() {
		s.notes.EXPECT().DeleteExpired(gomock.Any(), "id").Return(domain.ErrNotFound)
		s.links.EXPECT().DeleteExpired(gomock.Any(), "id").Return(domain.ErrNotFound)
		s.files.EXPECT().ReleaseExpiredSlug(gomock.Any(), "id").Return(true, nil)

		released, err := s.releaser.ReleaseExpiredSlug(s.T().Context(), "id")
		s.Require().NoError(err)
		s.True(released)
	})

	s.Run("no expired holder", func() {
		s.notes.EXPECT().DeleteExpired(gomock.Any(), "id").Return(domain.ErrNotFound)
		s.links.EXPECT().DeleteExpired(gomock.Any(), "id").Return(domain.ErrNotFound)
		s.files.EXPECT().ReleaseExpiredSlug(gomock.Any(), "id").Return(false, nil)

		released, err := s.releaser.ReleaseExpiredSlug(s.T().Context(), "id")
		s.Require().NoError(err)
		s.False(released)
	})
}

func (s *ReleaserSuite) TestErrorsStopTheSearch() {
	dbErr := errors.New("db down")

	s.Run("storage", func() {
		s.notes.EXPECT().DeleteExpired(gomock.Any(), "id").Return(dbErr)

		released, err := s.releaser.ReleaseExpiredSlug(s.T().Context(), "id")
		s.Require().ErrorIs(err, dbErr)
		s.False(released)
	})

	s.Run("files", func() {
		s.notes.EXPECT().DeleteExpired(gomock.Any(), "id").Return(domain.ErrNotFound)
		s.links.EXPECT().DeleteExpired(gomock.Any(), "id").Return(domain.ErrNotFound)
		s.files.EXPECT().ReleaseExpiredSlug(gomock.Any(), "id").Return(false, dbErr)

		released, err := s.releaser.ReleaseExpiredSlug(s.T().Context(), "id")
		s.Require().ErrorIs(err, dbErr)
		s.False(released)
	})
}

// TestWithoutFiles: without WithFiles (no --s3-endpoint), no file holds a slug.
func (s *ReleaserSuite) TestWithoutFiles() {
	releaser := New(s.notes, s.links)
	s.notes.EXPECT().DeleteExpired(gomock.Any(), "id").Return(domain.ErrNotFound)
	s.links.EXPECT().DeleteExpired(gomock.Any(), "id").Return(domain.ErrNotFound)

	released, err := releaser.ReleaseExpiredSlug(s.T().Context(), "id")
	s.Require().NoError(err)
	s.False(released)
}
