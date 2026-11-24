package files

import (
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// hiddenFile returns a stored owner-only file of a random owner.
func hiddenFile() *domain.File {
	owner := uuid.New()
	privacy := domain.PrivacyOwner
	file := storedFile(false)
	file.OwnerID = &owner
	file.Privacy = &privacy

	return file
}

// TestReader_HiddenFileIsForbidden checks that a reader who cannot see the file gets
// ErrForbidden from Inspect, Download, Update and Delete, also with the right edit code.
func (s *ManagerSuite) TestReader_HiddenFileIsForbidden() {
	ctx := s.T().Context()
	stranger := account(uuid.New())

	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("abc"), testNow()).Return(hiddenFile(), nil).Times(4)

	_, err := s.manager.Inspect(ctx, "abc", stranger)
	s.Require().ErrorIs(err, domain.ErrForbidden)

	_, err = s.manager.Download(ctx, "abc", stranger)
	s.Require().ErrorIs(err, domain.ErrForbidden)

	_, err = s.manager.Update(ctx, "abc", "code", stranger, noUpdate())
	s.Require().ErrorIs(err, domain.ErrForbidden)

	s.Require().ErrorIs(s.manager.Delete(ctx, "abc", "code", stranger), domain.ErrForbidden)
}

// TestSettings_ExpiredFileStays checks that the editor read reports an expired file without a
// delete: the strict mock fails on DeleteExpired, so the file page can still answer 410.
func (s *ManagerSuite) TestSettings_ExpiredFileStays() {
	file := storedFile(false)
	past := testNow().Add(-time.Minute)
	file.ExpiresAt = &past

	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("abc"), testNow()).Return(file, nil)

	_, err := s.manager.Settings(s.T().Context(), "abc", skipPrivacy())
	s.Require().ErrorIs(err, domain.ErrExpired)
}

// TestSettings_HiddenFileIsForbidden checks the privacy rule of the editor read.
func (s *ManagerSuite) TestSettings_HiddenFileIsForbidden() {
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("abc"), testNow()).Return(hiddenFile(), nil)

	_, err := s.manager.Settings(s.T().Context(), "abc", account(uuid.New()))
	s.Require().ErrorIs(err, domain.ErrForbidden)
}
