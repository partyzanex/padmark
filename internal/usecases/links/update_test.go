package links

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	"github.com/partyzanex/padmark/internal/infra/lock"
)

func (s *ManagerTestSuite) TestUpdate_PreservesStateAndEvictsCache() {
	ctx := s.T().Context()
	owner := uuid.New()
	privacy := domain.PrivacyOwner
	expiry := time.Now().Add(time.Hour)
	direct := false
	existing := &domain.Link{
		ID: domain.HashSlug("stable"), TargetURL: "old ciphertext", EditCode: "original",
		OwnerID: &owner, CreatedAt: time.Now().Add(-time.Hour), Views: 7,
		Privacy: &privacy, ExpiresAt: &expiry, DirectRedirect: &direct, BurnAfterClick: true,
	}
	s.storage.EXPECT().GetByID(ctx, existing.ID).Return(existing, nil)
	s.storage.EXPECT().Update(ctx, existing.ID, gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, link *domain.Link) (*domain.Link, error) {
			s.Equal("https://example.com/new", link.TargetURL)
			s.Equal("original", link.EditCode)
			s.Equal(existing.OwnerID, link.OwnerID)
			s.Nil(link.Privacy, "an omitted privacy must reach storage as nil, so COALESCE keeps it")
			s.Equal(existing.ExpiresAt, link.ExpiresAt)
			s.Equal(existing.DirectRedirect, link.DirectRedirect)
			s.Equal(existing.CreatedAt, link.CreatedAt)
			s.True(link.BurnAfterClick)
			link.Views = 8 // storage reports a click recorded after GetByID.

			return link, nil
		})
	cache := NewMockCache(s.ctrl)
	cache.EXPECT().Delete(existing.ID)
	s.manager.WithCache(cache)

	updated, err := s.manager.Update(ctx, "stable", "", owner, "https://example.com/new", UpdateOptions{
		ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
		ClearExpiry: false, ResetDirectRedirect: false,
	})
	s.Require().NoError(err)
	s.Equal("stable", updated.ID)
	s.Equal(8, updated.Views)
	s.Empty(updated.EditCode)
	s.Equal("original", existing.EditCode)
}

func (s *ManagerTestSuite) TestUpdate_EditCodeAndOwnerChecks() {
	owner, other := uuid.New(), uuid.New()
	for _, testCase := range []struct {
		name   string
		caller uuid.UUID
		code   string
		ok     bool
	}{
		{"owner bypass", owner, "", true},
		{"anonymous correct code", uuid.Nil, "original", true},
		{"other correct code", other, "original", true},
		{"anonymous wrong code", uuid.Nil, "wrong", false},
		{"other without code", other, "", false},
	} {
		s.Run(testCase.name, func() {
			existing := &domain.Link{
				OwnerID: &owner, EditCode: "original",
				CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
				ID: "", TargetURL: "", Views: 0, BurnAfterClick: false,
			}

			s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("stable")).Return(existing, nil)

			if testCase.ok {
				s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("stable"), gomock.Any()).Return(&domain.Link{
					CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
					DirectRedirect: nil, ID: "", TargetURL: "", EditCode: "",
					Views: 0, BurnAfterClick: false,
				}, nil)
			}

			_, err := s.manager.Update(s.T().Context(), "stable", testCase.code, testCase.caller,
				"https://example.com", UpdateOptions{
					ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
					ClearExpiry: false, ResetDirectRedirect: false,
				})
			if testCase.ok {
				s.NoError(err)
			} else {
				s.ErrorIs(err, domain.ErrInvalidEditCode)
			}
		})
	}
}

func (s *ManagerTestSuite) TestUpdate_Validation() {
	_, err := s.manager.Update(s.T().Context(), "stable", "code", uuid.Nil, "javascript:alert(1)", UpdateOptions{
		ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
		ClearExpiry: false, ResetDirectRedirect: false,
	})
	s.Require().ErrorIs(err, domain.ErrInvalidURL)

	for _, testCase := range []struct {
		privacy domain.Privacy
		err     error
	}{
		{domain.Privacy("invalid"), domain.ErrInvalidPrivacy},
		{domain.PrivacyOwner, domain.ErrOwnerPrivacyRequiresOwner},
	} {
		s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("stable")).Return(&domain.Link{
			EditCode:  "code",
			CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
			DirectRedirect: nil, ID: "", TargetURL: "", Views: 0,
			BurnAfterClick: false,
		}, nil)
		_, err = s.manager.Update(s.T().Context(), "stable", "code", uuid.Nil, "https://example.com",
			UpdateOptions{
				Privacy:   &testCase.privacy,
				ExpiresAt: nil, DirectRedirect: nil, ClearExpiry: false,
				ResetDirectRedirect: false,
			})
		s.ErrorIs(err, testCase.err)
	}
}

// A cache fill paused on an old snapshot must finish before the update evicts
// that snapshot. Metadata is always read from storage, even after a cache fill.
//
//nolint:funlen // Keep the blocked cache fill and concurrent PUT together to show their ordering.
func (s *ManagerTestSuite) TestUpdate_ConcurrentCacheFill() {
	ctx, cancel := context.WithTimeout(s.T().Context(), 5*time.Second)
	defer cancel()

	dbID := domain.HashSlug("stable")
	old := &domain.Link{
		ID: dbID, TargetURL: "https://example.com/old", EditCode: "code",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, Views: 0, BurnAfterClick: false,
	}
	readStarted, releaseRead, updateLockAttempt := make(chan struct{}), make(chan struct{}), make(chan struct{})
	locker := lock.NewLocker()
	observedLock := NewMockSlugLock(s.ctrl)

	gomock.InOrder(
		observedLock.EXPECT().Lock(ctx, dbID).DoAndReturn(locker.Lock),
		observedLock.EXPECT().Lock(ctx, dbID).DoAndReturn(func(ctx context.Context, id string) (func(), error) {
			close(updateLockAttempt)

			return locker.Lock(ctx, id)
		}),
	)
	gomock.InOrder(
		s.storage.EXPECT().GetByID(ctx, dbID).DoAndReturn(func(context.Context, string) (*domain.Link, error) {
			close(readStarted)

			select {
			case <-releaseRead:
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			clone := *old

			return &clone, nil
		}),
		s.storage.EXPECT().GetByID(ctx, dbID).Return(old, nil),
		s.storage.EXPECT().Update(ctx, dbID, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ string, updated *domain.Link) (*domain.Link, error) { return updated, nil }),
	)
	s.manager.slugLock = observedLock
	s.manager.WithCache(linkcache.NewDefault(slog.New(slog.DiscardHandler)))

	readDone := make(chan error, 1)

	go func() {
		_, err := s.manager.Peek(ctx, "stable")
		readDone <- err
	}()

	select {
	case <-readStarted:
	case <-ctx.Done():
		s.T().Fatal(ctx.Err())
	}

	updateDone := make(chan error, 1)

	privacy := domain.PrivacyAuthenticated
	go func() {
		_, err := s.manager.Update(ctx, "stable", "code", uuid.Nil, "https://example.com/new",
			UpdateOptions{
				Privacy:   &privacy,
				ExpiresAt: nil, DirectRedirect: nil, ClearExpiry: false,
				ResetDirectRedirect: false,
			})
		updateDone <- err
	}()

	select {
	case <-updateLockAttempt:
	case <-ctx.Done():
		s.T().Fatal(ctx.Err())
	}

	close(releaseRead)
	s.Require().NoError(<-readDone)
	s.Require().NoError(<-updateDone)
	_, cached := s.manager.cache.Get(dbID, old.TargetURL)
	s.False(cached, "the old target must not survive local eviction after PUT")
}
