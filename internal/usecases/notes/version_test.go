package notes

import (
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// These tests check that every write after an access check carries the stored edit-code hash of
// the checked note. Storage uses it to refuse a new note that took the ID after the check.

// checkedBurnNote returns a public burn note with the stored edit-code hash editCode.
func checkedBurnNote(editCode string) *domain.Note {
	return &domain.Note{
		ID: "x", Content: "ct", EditCode: editCode, OwnerID: nil, BurnAfterReading: true,

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, Title: "",
		ContentType: nil, Views: 0, BurnTTL: 0, ClearExpiry: false, ShowContents: nil,
	}
}

func storedNote(editCode string, ownerID *uuid.UUID) *domain.Note {
	return &domain.Note{
		ID: "abc-123", EditCode: editCode, OwnerID: ownerID, ContentType: new(domain.ContentTypeMarkdown),

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, Title: "old",
		Content: "", Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	}
}

func (s *ManagerTestSuite) TestDelete_UsesCheckedVersion() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(storedNote("secret123456", nil), nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc-123"), "secret123456").Return(nil)

	s.Require().NoError(s.manager.Delete(s.T().Context(), "abc-123", "secret123456", openReader()))
}

// TestDelete_OwnerUsesCheckedVersion verifies that the owner, who sends no edit code, still
// deletes only the checked note.
func (s *ManagerTestSuite) TestDelete_OwnerUsesCheckedVersion() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).
		Return(storedNote("secret123456", new(testUserID1)), nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc-123"), "secret123456").Return(nil)

	s.Require().NoError(s.manager.Delete(s.T().Context(), "abc-123", "", accountReader(testUserID1)))
}

// TestDelete_NewVersionIsNotFound verifies that a refused stale delete reaches the caller as
// ErrNotFound, the answer for a deleted note.
func (s *ManagerTestSuite) TestDelete_NewVersionIsNotFound() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).Return(storedNote("secret123456", nil), nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc-123"), "secret123456").Return(domain.ErrNotFound)

	err := s.manager.Delete(s.T().Context(), "abc-123", "secret123456", openReader())

	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestUpdate_UsesCheckedVersion() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("abc-123")).
		Return(storedNote("secret123456", new(testUserID1)), nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc-123"), gomock.Any(), testNow()).DoAndReturn(
		func(_ any, _ string, written *domain.Note, _ time.Time) error {
			s.Equal("secret123456", written.EditCode, "the stored hash selects the checked note")

			return domain.ErrNotFound
		})

	update := storedNote("", nil)
	update.Title, update.Content = "new", "body"

	_, err := s.manager.Update(s.T().Context(), "abc-123", "", accountReader(testUserID1), update)

	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestRead_BurnUsesCheckedVersion() {
	note := checkedBurnNote("hash:checked")
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-x")).Return(note, nil)
	s.storage.EXPECT().Consume(gomock.Any(), domain.HashSlug("burn-x"), domain.PrivacyPublic, "hash:checked", testNow()).
		Return(nil, domain.ErrNotFound)

	_, err := s.manager.Read(s.T().Context(), "burn-x", openReader())

	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// TestHandleSetBurnExpiryErr_NewVersion covers the check-then-write race: the checked note was
// deleted and a new note with the same privacy took its ID. The reader gets ErrNotFound and
// never the new note's content.
func (s *ManagerTestSuite) TestHandleSetBurnExpiryErr_NewVersion() {
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-x")).Return(checkedBurnNote("hash:new"), nil)

	got, err := s.manager.burn.handleSetExpiryErr(s.T().Context(), "burn-x", checkedBurnNote("hash:old"),
		domain.ErrNotFound)

	s.Require().ErrorIs(err, domain.ErrNotFound)
	s.Nil(got)
}

// TestRead_BurnTimerUsesCheckedVersion verifies that the timer start carries the stored edit-code
// hash of the checked note.
func (s *ManagerTestSuite) TestRead_BurnTimerUsesCheckedVersion() {
	note := checkedBurnNote("hash:checked")
	note.BurnTTL = 60
	s.storage.EXPECT().Get(gomock.Any(), domain.HashSlug("burn-x")).Return(note, nil)
	s.storage.EXPECT().SetBurnExpiry(gomock.Any(), domain.HashSlug("burn-x"), gomock.Any(), domain.PrivacyPublic,
		"hash:checked").Return(note, nil)

	_, err := s.manager.Read(s.T().Context(), "burn-x", openReader())

	s.Require().NoError(err)
}
