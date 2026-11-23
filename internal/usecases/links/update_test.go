package links

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
)

func (s *ManagerTestSuite) TestUpdate_PreservesStateAndEvictsCache() {
	ctx := s.T().Context()
	owner := uuid.New()
	privacy := domain.PrivacyOwner
	expiry := testNow().Add(time.Hour)
	direct := false
	existing := &domain.Link{
		ID: domain.HashSlug("stable"), TargetURL: "old ciphertext", EditCode: "original",
		OwnerID: &owner, CreatedAt: testNow().Add(-time.Hour), Views: 7,
		Privacy: &privacy, ExpiresAt: &expiry, DirectRedirect: &direct, BurnAfterClick: true,
	}
	s.storage.EXPECT().GetByID(ctx, existing.ID).Return(existing, nil)
	s.storage.EXPECT().Update(ctx, existing.ID, "original", gomock.Any(), testNow()).DoAndReturn(
		func(_ context.Context, _, _ string, patch *domain.LinkPatch, _ time.Time) (*domain.Link, error) {
			s.Equal("https://example.com/new", patch.TargetURL)
			s.Equal(&domain.LinkPatch{
				TargetURL: "https://example.com/new",
				ExpiresAt: nil, Privacy: nil, DirectRedirect: nil, ClearExpiry: false, ResetDirectRedirect: false,
			}, patch, "omitted fields must not reach storage, so the stored values stay")

			// Storage reports the row as stored, including a click recorded after GetByID.
			stored := *existing
			stored.TargetURL = patch.TargetURL
			stored.Views = 8

			return &stored, nil
		})
	s.cache.EXPECT().Delete(existing.ID)
	s.manager.WithCache(s.cache)

	updated, err := s.manager.Update(ctx, "stable", "", accountReader(owner), "https://example.com/new", UpdateOptions{
		ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
		ClearExpiry: false, ResetDirectRedirect: false,
	})
	s.Require().NoError(err)
	s.Equal("stable", updated.ID)
	s.Equal(8, updated.Views)
	s.Equal(existing.ExpiresAt, updated.ExpiresAt)
	s.Equal(existing.DirectRedirect, updated.DirectRedirect)
	s.True(updated.BurnAfterClick)
	s.Empty(updated.EditCode)
	s.Equal("original", existing.EditCode)
}

// TestUpdate_PatchCarriesOnlySentFields covers finding 9 in the usecase: each sent field and each
// explicit reset reaches storage, and no value comes from the snapshot that GetByID returned.
func (s *ManagerTestSuite) TestUpdate_PatchCarriesOnlySentFields() {
	expiry := testNow().Add(time.Hour)
	later := testNow().Add(2 * time.Hour)
	direct, other := true, false
	privacy := domain.PrivacyAuthenticated

	// Each case sets one field in the request and the same field in the expected patch.
	for name, send := range map[string]func(opts *UpdateOptions, want *domain.LinkPatch){
		"expiry only": func(opts *UpdateOptions, want *domain.LinkPatch) {
			opts.ExpiresAt, want.ExpiresAt = &later, &later
		},
		"clear expiry": func(opts *UpdateOptions, want *domain.LinkPatch) {
			opts.ClearExpiry, want.ClearExpiry = true, true
		},
		"privacy only": func(opts *UpdateOptions, want *domain.LinkPatch) {
			opts.Privacy, want.Privacy = &privacy, &privacy
		},
		"direct redirect only": func(opts *UpdateOptions, want *domain.LinkPatch) {
			opts.DirectRedirect, want.DirectRedirect = &other, &other
		},
		"reset direct redirect": func(opts *UpdateOptions, want *domain.LinkPatch) {
			opts.ResetDirectRedirect, want.ResetDirectRedirect = true, true
		},
	} {
		s.Run(name, func() {
			opts := UpdateOptions{
				ExpiresAt: nil, Privacy: nil, DirectRedirect: nil, ClearExpiry: false, ResetDirectRedirect: false,
			}
			want := domain.LinkPatch{
				TargetURL: "https://example.com/new",
				ExpiresAt: nil, Privacy: nil, DirectRedirect: nil, ClearExpiry: false, ResetDirectRedirect: false,
			}
			send(&opts, &want)

			s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("stable")).Return(&domain.Link{
				EditCode: "code", ExpiresAt: &expiry, DirectRedirect: &direct,

				CreatedAt: time.Time{}, Privacy: nil, OwnerID: nil, ID: "", TargetURL: "old",
				Views: 0, BurnAfterClick: false,
			}, nil)

			s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("stable"), "code", &want, testNow()).
				Return(storedLink("code", nil), nil)

			_, err := s.manager.Update(s.T().Context(), "stable", "code", openReader(),
				"https://example.com/new", opts)
			s.Require().NoError(err)
		})
	}
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
				s.storage.EXPECT().Update(gomock.Any(), domain.HashSlug("stable"), "original", gomock.Any(),
					testNow()).Return(&domain.Link{
					CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
					DirectRedirect: nil, ID: "", TargetURL: "", EditCode: "",
					Views: 0, BurnAfterClick: false,
				}, nil)
			}

			_, err := s.manager.Update(s.T().Context(), "stable", testCase.code, accountReader(testCase.caller),
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
	_, err := s.manager.Update(s.T().Context(), "stable", "code", openReader(), "javascript:alert(1)", UpdateOptions{
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
		_, err = s.manager.Update(s.T().Context(), "stable", "code", openReader(), "https://example.com",
			UpdateOptions{
				Privacy:   &testCase.privacy,
				ExpiresAt: nil, DirectRedirect: nil, ClearExpiry: false,
				ResetDirectRedirect: false,
			})
		s.ErrorIs(err, testCase.err)
	}
}

// TestRead_StaleCacheFillIsNotServed: no lock orders reads, updates and cache fills, so a read
// that started before an Update can store the old target after the Update evicts it. The next
// read gets the new ciphertext from storage, so the fingerprint misses, the old target is not
// served and the new target replaces the entry.
func (s *ManagerTestSuite) TestRead_StaleCacheFillIsNotServed() {
	ctx := s.T().Context()
	dbID := domain.HashSlug("stable")
	cache := linkcache.NewDefault(slog.New(slog.DiscardHandler))
	s.manager.WithCache(cache)

	// The passthrough encryptor makes the ciphertext equal to the target URL.
	cache.Set(ctx, dbID, "https://example.com/old", "https://example.com/old")
	s.storage.EXPECT().GetByID(ctx, dbID).Return(&domain.Link{
		ID: dbID, TargetURL: "https://example.com/new", EditCode: "code",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, Views: 0, BurnAfterClick: false,
	}, nil)

	link, err := s.manager.Peek(ctx, "stable")
	s.Require().NoError(err)
	s.Equal("https://example.com/new", link.TargetURL)

	_, stale := cache.Get(dbID, "https://example.com/old")
	s.False(stale, "the read replaces the stale entry")

	target, fresh := cache.Get(dbID, "https://example.com/new")
	s.True(fresh)
	s.Equal("https://example.com/new", target)
}
