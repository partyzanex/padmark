package notes

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// contentsNote builds a note for the ShowContents tests with all fields set explicitly.
func contentsNote(showContents *bool) *domain.Note {
	return &domain.Note{
		Title: "toc", Content: "## Part",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ID: "", ContentType: nil,
		EditCode: "", Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false, ShowContents: showContents,
	}
}

// TestCreate_ShowContentsDefaultsToFalse pins the old-client contract: a create request without
// the option stores false, so the note view keeps the full-width article.
func (s *ManagerTestSuite) TestCreate_ShowContentsDefaultsToFalse() {
	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, written *domain.Note) error {
			s.Require().NotNil(written.ShowContents)
			s.False(*written.ShowContents)

			return nil
		})

	result, err := s.manager.Create(s.T().Context(), contentsNote(nil))

	s.Require().NoError(err)
	s.False(result.ContentsSidebar())
}

func (s *ManagerTestSuite) TestCreate_ShowContentsTrueIsStored() {
	s.expectSlugFree()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, written *domain.Note) error {
			s.True(written.ContentsSidebar())

			return nil
		})

	result, err := s.manager.Create(s.T().Context(), contentsNote(new(true)))

	s.Require().NoError(err)
	s.True(result.ContentsSidebar())
}

// TestUpdate_OmittedShowContentsKeepsStoredValue pins the update contract: an omitted option
// goes to storage as nil (COALESCE keeps the column), and the response reports the stored value.
func (s *ManagerTestSuite) TestUpdate_OmittedShowContentsKeepsStoredValue() {
	existing := contentsNote(new(true))
	existing.ID = "abc-123"
	existing.EditCode = "secret123456"

	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc-123"), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, written *domain.Note) error {
			s.Nil(written.ShowContents, "an omitted option must not overwrite a concurrent change")

			return nil
		})

	result, err := s.manager.Update(s.T().Context(), "abc-123", "secret123456", uuid.Nil, contentsNote(nil))

	s.Require().NoError(err)
	s.True(result.ContentsSidebar(), "the omitted option must not reset to false")
}

func (s *ManagerTestSuite) TestUpdate_ExplicitShowContentsIsWritten() {
	for _, value := range []bool{false, true} {
		existing := contentsNote(new(!value))
		existing.ID = "abc-123"
		existing.EditCode = "secret123456"

		s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(existing, nil)
		s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc-123"), gomock.Any()).DoAndReturn(
			func(_ context.Context, _ string, written *domain.Note) error {
				s.Require().NotNil(written.ShowContents)
				s.Equal(value, *written.ShowContents)

				return nil
			})

		result, err := s.manager.Update(s.T().Context(), "abc-123", "secret123456", uuid.Nil, contentsNote(new(value)))

		s.Require().NoError(err)
		s.Equal(value, result.ContentsSidebar())
	}
}
