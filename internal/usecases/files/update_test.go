package files

import (
	"context"
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

	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("abc")).Return(stored, nil)
	s.storage.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, file *domain.File) error {
		s.Equal(domain.HashSlug("abc"), file.ID)
		s.Equal(&expires, file.ExpiresAt)
		s.True(file.BurnAfterDownload)
		s.Equal("obj", file.ObjectKey)
		s.JSONEq(testMeta, file.SealedMeta)
		s.Equal(3, file.Downloads)

		return nil
	})

	opts := noUpdate()
	opts.ExpiresAt = &expires
	opts.BurnAfterDownload = &burn

	updated, err := s.manager.Update(ctx, "abc", "", owner, opts)
	s.Require().NoError(err)
	s.Equal("abc", updated.ID)
	s.Equal("report.pdf", updated.Name)
	s.Empty(updated.EditCode, "an update response does not show the edit code")
	s.Equal("code", stored.EditCode, "Update does not change the stored record in place")
}

func (s *ManagerSuite) TestUpdate_OtherUserWithEditCodeChangesPrivacy() {
	ctx := s.T().Context()
	stored := storedFile(false)
	authenticated := domain.PrivacyAuthenticated

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(stored, nil)
	s.storage.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, file *domain.File) error {
		s.Equal(domain.PrivacyAuthenticated, file.EffectivePrivacy())

		return nil
	})

	opts := noUpdate()
	opts.Privacy = &authenticated

	updated, err := s.manager.Update(ctx, "abc", "code", uuid.New(), opts)
	s.Require().NoError(err)
	s.Equal(domain.PrivacyAuthenticated, updated.EffectivePrivacy())
}

func (s *ManagerSuite) TestUpdate_WrongEditCode() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)

	_, err := s.manager.Update(s.T().Context(), "abc", "wrong", uuid.New(), noUpdate())
	s.Require().ErrorIs(err, domain.ErrInvalidEditCode)
}

func (s *ManagerSuite) TestUpdate_ExpiredFileIsNotFound() {
	stored := storedFile(false)
	past := time.Now().Add(-time.Minute)
	stored.ExpiresAt = &past

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(stored, nil)

	_, err := s.manager.Update(s.T().Context(), "abc", "code", uuid.Nil, noUpdate())
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestUpdate_ClearExpiry() {
	stored := storedFile(false)
	future := time.Now().Add(time.Hour)
	stored.ExpiresAt = &future

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(stored, nil)
	s.storage.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, file *domain.File) error {
		s.Nil(file.ExpiresAt)

		return nil
	})

	opts := noUpdate()
	opts.ClearExpiry = true
	opts.ExpiresAt = &future

	updated, err := s.manager.Update(s.T().Context(), "abc", "code", uuid.Nil, opts)
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

			_, err := s.manager.Update(s.T().Context(), "abc", "code", uuid.Nil, opts)
			s.Require().ErrorIs(err, testCase.err)
		})
	}
}

func (s *ManagerSuite) TestUpdate_StorageErrors() {
	errDB := errors.New("db down")

	s.Run("read", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, errDB)

		_, err := s.manager.Update(s.T().Context(), "abc", "code", uuid.Nil, noUpdate())
		s.Require().ErrorIs(err, errDB)
		s.Contains(err.Error(), "update file")
	})

	s.Run("write", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)
		s.storage.EXPECT().Update(gomock.Any(), gomock.Any()).Return(errDB)

		_, err := s.manager.Update(s.T().Context(), "abc", "code", uuid.Nil, noUpdate())
		s.Require().ErrorIs(err, errDB)
		s.Contains(err.Error(), "update file")
	})

	s.Run("missing record", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

		_, err := s.manager.Update(s.T().Context(), "abc", "code", uuid.Nil, noUpdate())
		s.Require().ErrorIs(err, domain.ErrNotFound)
	})
}
