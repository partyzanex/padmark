package files

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

func noUpdate() UpdateOptions {
	return UpdateOptions{ExpiresAt: nil, Privacy: nil, BurnAfterDownload: nil, ClearExpiry: false}
}

func (s *ManagerSuite) TestUpdate_OwnerChangesExpiryWithoutEditCode() {
	ctx := s.T().Context()
	owner := uuid.New()
	stored := storedFile(false)
	stored.OwnerID = &owner
	stored.Downloads = 3
	expires := time.Now().Add(time.Hour)
	burn := true

	opts := noUpdate()
	opts.ExpiresAt = &expires
	opts.BurnAfterDownload = &burn

	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("abc")).Return(stored, nil)
	// Storage gets only the changes of the request, so a parallel change of the privacy on a
	// different server process stays.
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc"), "obj", opts, gomock.Any()).Return(nil)

	updated, err := s.manager.Update(ctx, "abc", "", account(owner), opts)
	s.Require().NoError(err)
	s.Equal("abc", updated.ID)
	s.Equal("report.pdf", updated.Name)
	s.Equal(&expires, updated.ExpiresAt)
	s.True(updated.BurnAfterDownload)
	s.Equal(3, updated.Downloads)
	s.Nil(updated.ContentKey, "an update response does not hold the content key")
	s.Empty(updated.EditCode, "an update response does not show the edit code")
	s.Equal("code", stored.EditCode, "Update does not change the stored record in place")
}

func (s *ManagerSuite) TestUpdate_OtherUserWithEditCodeChangesPrivacy() {
	ctx := s.T().Context()
	authenticated := domain.PrivacyAuthenticated

	opts := noUpdate()
	opts.Privacy = &authenticated

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)
	s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("abc"), "obj", opts, gomock.Any()).Return(nil)

	updated, err := s.manager.Update(ctx, "abc", "code", account(uuid.New()), opts)
	s.Require().NoError(err)
	s.Equal(domain.PrivacyAuthenticated, updated.EffectivePrivacy())
}

func (s *ManagerSuite) TestUpdate_WithoutChangesWritesNothing() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)
	// No Storage.Update expectation: the strict mock fails the test on a write.

	updated, err := s.manager.Update(s.T().Context(), "abc", "code", skipPrivacy(), noUpdate())
	s.Require().NoError(err)
	s.Equal("report.pdf", updated.Name)
}

func (s *ManagerSuite) TestUpdate_WrongEditCode() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)

	_, err := s.manager.Update(s.T().Context(), "abc", "wrong", account(uuid.New()), noUpdate())
	s.Require().ErrorIs(err, domain.ErrInvalidEditCode)
}

// TestUpdate_ExpiredFileIsGone: an expired file gets the error of a read (410), before the edit
// code check, and storage gets no write. The strict mock fails the test on a Storage.Update call.
func (s *ManagerSuite) TestUpdate_ExpiredFileIsGone() {
	for _, code := range []string{"code", "wrong"} {
		s.Run(code, func() {
			stored := storedFile(false)
			past := time.Now().Add(-time.Minute)
			stored.ExpiresAt = &past

			s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(stored, nil)

			opts := noUpdate()
			opts.ClearExpiry = true

			_, err := s.manager.Update(s.T().Context(), "abc", code, skipPrivacy(), opts)
			s.Require().ErrorIs(err, domain.ErrExpired)
		})
	}
}

// TestUpdate_HiddenExpiredFileIsForbidden: privacy comes before expiry, so a reader that cannot
// see the file does not learn that it expired.
func (s *ManagerSuite) TestUpdate_HiddenExpiredFileIsForbidden() {
	stored := privateFile(false)
	past := time.Now().Add(-time.Minute)
	stored.ExpiresAt = &past

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(stored, nil)

	_, err := s.manager.Update(s.T().Context(), "abc", "code", account(uuid.New()), noUpdate())
	s.Require().ErrorIs(err, domain.ErrForbidden)
}

func (s *ManagerSuite) TestUpdate_ClearExpiry() {
	stored := storedFile(false)
	future := time.Now().Add(time.Hour)
	stored.ExpiresAt = &future

	opts := noUpdate()
	opts.ClearExpiry = true
	opts.ExpiresAt = &future

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(stored, nil)
	s.storage.EXPECT().Update(gomock.Any(), gomock.Any(), "obj", opts, gomock.Any()).Return(nil)

	updated, err := s.manager.Update(s.T().Context(), "abc", "code", skipPrivacy(), opts)
	s.Require().NoError(err)
	s.Nil(updated.ExpiresAt)
}

func (s *ManagerSuite) TestUpdate_InvalidPrivacy() {
	for _, testCase := range []struct {
		privacy domain.Privacy
		err     error
	}{
		{domain.Privacy("invalid"), domain.ErrInvalidPrivacy},
		{domain.PrivacyOwner, domain.ErrOwnerPrivacyRequiresOwner},
	} {
		s.Run(string(testCase.privacy), func() {
			s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)

			opts := noUpdate()
			opts.Privacy = &testCase.privacy

			_, err := s.manager.Update(s.T().Context(), "abc", "code", skipPrivacy(), opts)
			s.Require().ErrorIs(err, testCase.err)
		})
	}
}

// TestUpdate_CorruptMetaWritesNothing covers R-40: a record that Update cannot open is not
// found, and Update must not change it before it returns that error.
func (s *ManagerSuite) TestUpdate_CorruptMetaWritesNothing() {
	stored := storedFile(false)
	stored.SealedMeta = "not json"
	burn := true

	opts := noUpdate()
	opts.BurnAfterDownload = &burn

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(stored, nil)

	_, err := s.manager.Update(s.T().Context(), "abc", "code", skipPrivacy(), opts)
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// TestUpdate_SlugTakenByNewFile covers a stale read: a new file took the slug after the read,
// so the write with the old object key finds no row.
func (s *ManagerSuite) TestUpdate_SlugTakenByNewFile() {
	burn := true

	opts := noUpdate()
	opts.BurnAfterDownload = &burn

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)
	s.storage.EXPECT().Update(gomock.Any(), gomock.Any(), "obj", opts, gomock.Any()).Return(domain.ErrNotFound)

	_, err := s.manager.Update(s.T().Context(), "abc", "code", skipPrivacy(), opts)
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestUpdate_StorageErrors() {
	errDB := errors.New("db down")
	burn := true

	opts := noUpdate()
	opts.BurnAfterDownload = &burn

	s.Run("read", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, errDB)

		_, err := s.manager.Update(s.T().Context(), "abc", "code", skipPrivacy(), opts)
		s.Require().ErrorIs(err, errDB)
		s.Contains(err.Error(), "update file")
	})

	s.Run("write", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)
		s.storage.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(errDB)

		_, err := s.manager.Update(s.T().Context(), "abc", "code", skipPrivacy(), opts)
		s.Require().ErrorIs(err, errDB)
		s.Contains(err.Error(), "update file")
	})

	s.Run("missing record", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

		_, err := s.manager.Update(s.T().Context(), "abc", "code", skipPrivacy(), opts)
		s.Require().ErrorIs(err, domain.ErrNotFound)
	})
}
