package links

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

type ManagerTestSuite struct {
	suite.Suite

	ctrl    *gomock.Controller
	storage *MockStorage
	expired *MockExpiredSlugReleaser
	// cache and metrics are attached only by the tests that check them (WithCache, WithMetrics).
	cache   *MockCache
	metrics *MockMetrics
	manager *Manager
}

func (s *ManagerTestSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.storage = NewMockStorage(s.ctrl)
	s.expired = NewMockExpiredSlugReleaser(s.ctrl)
	s.cache = NewMockCache(s.ctrl)
	s.metrics = NewMockMetrics(s.ctrl)
	s.manager = s.newManager(passthroughEncryptor(s.ctrl))
}

// newManager returns a Manager with the storage mocks of the suite, the given encryptor and custom
// slugs on. It has no cache and no metrics: a test attaches s.cache or s.metrics.
func (s *ManagerTestSuite) newManager(encryptor Encryptor) *Manager {
	return NewManager(&Deps{
		Storage: s.storage, Encryptor: encryptor, Hasher: identityHasher(s.ctrl),
		ExpiredSlugs: s.expired, Log: slog.New(slog.DiscardHandler),
		Now: testNow,
	}, true)
}

func (s *ManagerTestSuite) TearDownTest() {
	s.ctrl.Finish()
}

func TestManagerTestSuite(t *testing.T) {
	suite.Run(t, new(ManagerTestSuite))
}

// Create.

func (s *ManagerTestSuite) TestCreate_OK() {
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	link, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", EditCode: "", BurnAfterClick: false,
	})

	s.Require().NoError(err)
	s.Equal("https://example.com", link.TargetURL)
	s.NotEmpty(link.ID)
	s.NotEmpty(link.EditCode)
}

// TestCreate_UserSuppliedCodeUsesMemoryHardHash pins that a caller-chosen edit code is
// hashed with the memory-hard variant (identityHasher.HashUserProvided → "user:" prefix)
// because its entropy is not guaranteed; the generated-code path stays on the fast hash.
func (s *ManagerTestSuite) TestCreate_UserSuppliedCodeUsesMemoryHardHash() {
	var storedHash string

	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stored *domain.Link) error {
			storedHash = stored.EditCode

			return nil
		})

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		EditCode:  "MyCustomCode1",
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", BurnAfterClick: false,
	})

	s.Require().NoError(err)
	s.Equal("user:MyCustomCode1", storedHash)
}

// TestCreate_URLSchemeWhitelist is the explicit whitelist test matrix: http/https allowed, every
// other scheme (and schemeless/relative URLs) rejected with domain.ErrInvalidURL at Create time —
// before storage.Create is ever called, so a malicious scheme never reaches persistence.
func (s *ManagerTestSuite) TestCreate_URLSchemeWhitelist() {
	tests := []struct {
		name string
		url  string
		ok   bool
	}{
		{name: "http allowed", url: "http://example.com", ok: true},
		{name: "https allowed", url: "https://example.com/path?q=1", ok: true},
		{name: "javascript rejected", url: "javascript:alert(1)", ok: false},
		{name: "data rejected", url: "data:text/html,<script>alert(1)</script>", ok: false},
		{name: "file rejected", url: "file:///etc/passwd", ok: false},
		{name: "vbscript rejected", url: "vbscript:msgbox(1)", ok: false},
		{name: "schemeless rejected", url: "example.com/path", ok: false},
		{name: "relative path rejected", url: "/just/a/path", ok: false},
		{name: "empty rejected", url: "", ok: false},
	}

	for _, test := range tests {
		s.Run(test.name, func() {
			if test.ok {
				s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			}

			_, err := s.manager.Create(s.T().Context(), test.url, CreateOptions{
				ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
				CustomHash: "", EditCode: "", BurnAfterClick: false,
			})

			if test.ok {
				s.NoError(err)
			} else {
				s.ErrorIs(err, domain.ErrInvalidURL)
			}
		})
	}
}

// TestCreate_CustomAliasConflict: storage refuses the custom alias and no expired record holds
// it, so Create reports the conflict without a new slug.
func (s *ManagerTestSuite) TestCreate_CustomAliasConflict() {
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
	s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), domain.HashSlug("taken"), testNow()).Return(false, nil)

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		CustomHash: "taken",
		ExpiresAt:  nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		EditCode: "", BurnAfterClick: false,
	})

	s.ErrorIs(err, domain.ErrSlugConflict)
}

func (s *ManagerTestSuite) TestCreate_RandomSlugCollision_Retries() {
	var ids []string

	record := func(_ any, link *domain.Link) { ids = append(ids, link.ID) }

	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Do(record).Return(domain.ErrSlugConflict),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Do(record).Return(nil),
	)

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", EditCode: "", BurnAfterClick: false,
	})

	s.Require().NoError(err)
	s.Require().Len(ids, 2)
	s.NotEqual(ids[0], ids[1], "a refused random slug gets a new random slug")
}

func (s *ManagerTestSuite) TestCreate_RandomSlugCollision_ExhaustsRetries() {
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict).Times(maxHashRetries)

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", EditCode: "", BurnAfterClick: false,
	})

	s.ErrorIs(err, domain.ErrHashCollision)
}

func (s *ManagerTestSuite) TestCreate_CustomSlugRejectsMultiSegment() {
	// Links are only served at single-segment routes (/r/{id}, /links/{id}); a path-like alias
	// would be created but unroutable, so Create must reject it up front. No repo call
	// expectations are set — gomock fails the test if the guard ever lets a slug through.
	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		CustomHash: "docs/guide",
		ExpiresAt:  nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		EditCode: "", BurnAfterClick: false,
	})

	s.Require().ErrorIs(err, domain.ErrInvalidSlug)

	_, err = s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		CustomHash: "a/b/c",
		ExpiresAt:  nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		EditCode: "", BurnAfterClick: false,
	})

	s.ErrorIs(err, domain.ErrInvalidSlug)
}

func (s *ManagerTestSuite) TestCreate_CustomSlugDisabled() {
	s.manager = NewManager(&Deps{
		Storage: s.storage, Encryptor: passthroughEncryptor(s.ctrl), Hasher: identityHasher(s.ctrl),
		ExpiredSlugs: s.expired, Log: slog.New(slog.DiscardHandler),
		Now: testNow,
	}, false)

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		CustomHash: "my-alias",
		ExpiresAt:  nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		EditCode: "", BurnAfterClick: false,
	})

	s.ErrorIs(err, domain.ErrCustomSlugDisabled)
}

func (s *ManagerTestSuite) TestCreate_InvalidPrivacy() {
	p := domain.Privacy("bogus")

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		Privacy:   &p,
		ExpiresAt: nil, OwnerID: nil, DirectRedirect: nil, CustomHash: "",
		EditCode: "", BurnAfterClick: false,
	})

	s.ErrorIs(err, domain.ErrInvalidPrivacy)
}

func (s *ManagerTestSuite) TestCreate_OwnerPrivacyRequiresOwner() {
	p := domain.PrivacyOwner

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		Privacy:   &p,
		ExpiresAt: nil, OwnerID: nil, DirectRedirect: nil, CustomHash: "",
		EditCode: "", BurnAfterClick: false,
	})

	s.ErrorIs(err, domain.ErrOwnerPrivacyRequiresOwner)
}

func (s *ManagerTestSuite) TestCreate_OwnerPrivacy_WithOwnerID_OK() {
	owner := uuid.New()
	ownerPrivacy := domain.PrivacyOwner

	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	link, err := s.manager.Create(s.T().Context(), "https://example.com",
		CreateOptions{
			Privacy: &ownerPrivacy, OwnerID: &owner,
			ExpiresAt: nil, DirectRedirect: nil, CustomHash: "", EditCode: "",
			BurnAfterClick: false,
		})

	s.Require().NoError(err)
	s.Equal(domain.PrivacyOwner, link.EffectivePrivacy())
}

// TestCreate_DirectRedirectOverride_PassedThrough verifies CreateOptions.DirectRedirect reaches
// storage.Create and the returned link unchanged — Manager itself doesn't interpret the value
// (that's LinkHandler.Redirect's job via domain.Link.EffectiveDirectRedirect), it only threads it
// through.
func (s *ManagerTestSuite) TestCreate_DirectRedirectOverride_PassedThrough() {
	override := false

	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, l *domain.Link) error {
			s.Require().NotNil(l.DirectRedirect)
			s.False(*l.DirectRedirect)

			return nil
		})

	link, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		DirectRedirect: &override,
		ExpiresAt:      nil, Privacy: nil, OwnerID: nil, CustomHash: "",
		EditCode: "", BurnAfterClick: false,
	})

	s.Require().NoError(err)
	s.Require().NotNil(link.DirectRedirect)
	s.False(*link.DirectRedirect)
}

func (s *ManagerTestSuite) TestCreate_StorageError() {
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(errors.New("db error"))

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", EditCode: "", BurnAfterClick: false,
	})

	s.Require().Error(err)
}

// Follow.

func (s *ManagerTestSuite) TestFollow_OK() {
	link := &domain.Link{
		TargetURL: "https://example.com",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
		BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil)

	got, err := s.manager.Follow(s.T().Context(), "the-slug", openReader())

	s.Require().NoError(err)
	s.Equal("https://example.com", got.TargetURL)
	s.Equal(1, got.Views)
}

func (s *ManagerTestSuite) TestFollow_NotFound() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

	_, err := s.manager.Follow(s.T().Context(), "missing", openReader())

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestFollow_Expired() {
	past := testNow().Add(-time.Hour)
	link := &domain.Link{
		TargetURL: "https://example.com", ExpiresAt: &past,
		CreatedAt: time.Time{}, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		ID: "", EditCode: "", Views: 0, BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), testNow()).Return(nil)

	_, err := s.manager.Follow(s.T().Context(), "expired-slug", openReader())

	s.ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerTestSuite) TestFollow_BurnAfterClick_ConsumedOnce() {
	link := &domain.Link{
		TargetURL: "https://example.com", BurnAfterClick: true,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().Consume(gomock.Any(), gomock.Any(), testNow()).Return(link, nil)

	got, err := s.manager.Follow(s.T().Context(), "burn-slug", openReader())

	s.Require().NoError(err)
	s.Equal("https://example.com", got.TargetURL)
}

// TestFollow_NoBurnIntent_BurnLinkLeftIntact: an access without deliberate intent must neither
// consume a burn_after_click link nor count a view — it gets ErrBurnIntentRequired.
func (s *ManagerTestSuite) TestFollow_NoBurnIntent_BurnLinkLeftIntact() {
	link := &domain.Link{
		TargetURL: "https://example.com", BurnAfterClick: true,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	// No Consume, no IncrementViews.

	got, err := s.manager.Follow(s.T().Context(), "burn-slug", heldReader())

	s.Require().ErrorIs(err, domain.ErrBurnIntentRequired)
	s.Nil(got)
}

// TestFollow_NoBurnIntent_OrdinaryLinkFollowed: without AllowBurn, any other link is followed
// exactly as with it.
func (s *ManagerTestSuite) TestFollow_NoBurnIntent_OrdinaryLinkFollowed() {
	link := &domain.Link{
		TargetURL: "https://example.com",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
		BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil)

	got, err := s.manager.Follow(s.T().Context(), "plain-slug", heldReader())

	s.Require().NoError(err)
	s.Equal("https://example.com", got.TargetURL)
	s.Equal(1, got.Views)
}

// TestFollow_NoBurnIntent_ExpiredLinkStillExpires: expiry is not a burn — an expired link is
// deleted and ErrExpired returned on either path.
func (s *ManagerTestSuite) TestFollow_NoBurnIntent_ExpiredLinkStillExpires() {
	past := testNow().Add(-time.Minute)
	link := &domain.Link{
		TargetURL: "https://example.com", BurnAfterClick: true, ExpiresAt: &past,
		CreatedAt: time.Time{}, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		ID: "", EditCode: "", Views: 0,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), testNow()).Return(nil)

	_, err := s.manager.Follow(s.T().Context(), "old-slug", heldReader())

	s.ErrorIs(err, domain.ErrExpired)
}

// TestFollow_BurnAfterClick_SecondCallNotFound verifies burn-after-click is exactly-once: a
// second Follow for the same slug, after the row was consumed, gets ErrNotFound.
func (s *ManagerTestSuite) TestFollow_BurnAfterClick_SecondCallNotFound() {
	link := &domain.Link{
		TargetURL: "https://example.com", BurnAfterClick: true,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().Consume(gomock.Any(), gomock.Any(), testNow()).Return(link, nil)

	_, err := s.manager.Follow(s.T().Context(), "burn-slug", openReader())
	s.Require().NoError(err)

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

	_, err = s.manager.Follow(s.T().Context(), "burn-slug", openReader())
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestFollow_DecryptFailure() {
	s.manager = s.newManager(failingEncryptor(s.ctrl))

	link := &domain.Link{
		TargetURL: "ciphertext",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
		BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)

	_, err := s.manager.Follow(s.T().Context(), "the-slug", openReader())

	s.ErrorIs(err, domain.ErrNotFound,
		"decryption failure collapses to ErrNotFound, not a distinguishable error")
}

// Cache (WithCache) — the default Manager has no cache (see SetupTest); these tests attach s.cache
// to exercise resolveLink/evictCache without changing every other test's expectations.

func (s *ManagerTestSuite) TestFollow_CacheHit_ReadsCurrentMetadata() {
	s.manager = s.newManager(failingEncryptor(s.ctrl)).WithCache(s.cache)

	stored := &domain.Link{
		TargetURL: "ciphertext", Views: 8,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("the-slug")).Return(stored, nil)
	s.cache.EXPECT().Get(domain.HashSlug("the-slug"), "ciphertext").Return("https://example.com", true)
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil)

	got, err := s.manager.Follow(s.T().Context(), "the-slug", openReader())

	s.Require().NoError(err)
	s.Equal("https://example.com", got.TargetURL)
	s.Equal(9, got.Views)
	s.Equal("the-slug", got.ID)
}

func (s *ManagerTestSuite) TestFollow_CacheMiss_PopulatesCacheForNonBurnLink() {
	s.manager.WithCache(s.cache)

	link := &domain.Link{
		TargetURL: "https://example.com",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
		BurnAfterClick: false,
	}

	s.cache.EXPECT().Get(gomock.Any(), "https://example.com").Return("", false)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.cache.EXPECT().Set(gomock.Any(), gomock.Any(), "https://example.com", "https://example.com")
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Follow(s.T().Context(), "the-slug", openReader())

	s.Require().NoError(err)
}

// TestFollow_BurnAfterClick_NeverCached verifies a burn-after-click link is never written to
// the cache — caching one would risk a second, concurrent Follow being served a stale
// "not yet consumed" copy instead of relying on storage.Consume's atomic exactly-once guarantee.
func (s *ManagerTestSuite) TestFollow_BurnAfterClick_NeverCached() {
	s.manager.WithCache(s.cache)

	link := &domain.Link{
		TargetURL: "https://example.com", BurnAfterClick: true,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
	}

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	// cache.Set deliberately has no EXPECT() — a burn-after-click link must never be cached.
	s.storage.EXPECT().Consume(gomock.Any(), gomock.Any(), testNow()).Return(link, nil)

	_, err := s.manager.Follow(s.T().Context(), "burn-slug", openReader())

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestFollow_Expired_EvictsCache() {
	s.manager.WithCache(s.cache)

	past := testNow().Add(-time.Hour)
	link := &domain.Link{
		TargetURL: "https://example.com", ExpiresAt: &past,
		CreatedAt: time.Time{}, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		ID: "", EditCode: "", Views: 0, BurnAfterClick: false,
	}

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), testNow()).Return(nil)
	s.cache.EXPECT().Delete(gomock.Any())

	_, err := s.manager.Follow(s.T().Context(), "expired-slug", openReader())

	s.ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerTestSuite) TestPeek_CacheHit_ReadsCurrentMetadata() {
	s.manager = s.newManager(failingEncryptor(s.ctrl)).WithCache(s.cache)

	stored := &domain.Link{
		TargetURL: "ciphertext", Views: 8,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("the-slug")).Return(stored, nil)
	s.cache.EXPECT().Get(domain.HashSlug("the-slug"), "ciphertext").Return("https://example.com", true)

	got, err := s.manager.Peek(s.T().Context(), "the-slug")

	s.Require().NoError(err)
	s.Equal("https://example.com", got.TargetURL)
	s.Equal(8, got.Views)
	s.Equal("the-slug", got.ID)
}

func (s *ManagerTestSuite) TestPeek_PrivateRowNeverUsesCache() {
	s.manager.WithCache(s.cache)

	privacy, owner := domain.PrivacyOwner, uuid.New()
	stored := &domain.Link{
		TargetURL: "https://example.com/private", Privacy: &privacy, OwnerID: &owner,
		CreatedAt: time.Time{}, ExpiresAt: nil, DirectRedirect: nil, ID: "",
		EditCode: "", Views: 0, BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("the-slug")).Return(stored, nil)
	// No cache Get/Set is allowed for a private row, even if an older public target
	// is still present in this replica's cache.
	got, err := s.manager.Peek(s.T().Context(), "the-slug")
	s.Require().NoError(err)
	s.Equal(domain.PrivacyOwner, got.EffectivePrivacy())
	s.False(got.VisibleTo(domain.Reader{CallerID: uuid.Nil, Authenticated: false, AllowBurn: false, SkipPrivacy: false}))
}

func (s *ManagerTestSuite) TestPeek_DatabaseFailureNeverFallsBackToCache() {
	s.manager.WithCache(s.cache)

	for _, cause := range []error{domain.ErrNotFound, errors.New("database unavailable")} {
		s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("the-slug")).Return(nil, cause)
		_, err := s.manager.Peek(s.T().Context(), "the-slug")
		s.ErrorIs(err, cause)
	}
}

// Metrics (WithMetrics) — the default Manager records no metrics (see SetupTest); these tests
// attach s.metrics, exactly like the Cache tests above.

func (s *ManagerTestSuite) TestCreate_RecordsLinksCreated() {
	s.manager.WithMetrics(s.metrics)

	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.metrics.EXPECT().IncLinksCreated()

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", EditCode: "", BurnAfterClick: false,
	})

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestCreate_InvalidURL_DoesNotRecordLinksCreated() {
	s.manager.WithMetrics(s.metrics)
	// s.metrics.EXPECT().IncLinksCreated() deliberately absent — a rejected create must not count.

	_, err := s.manager.Create(s.T().Context(), "javascript:alert(1)", CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", EditCode: "", BurnAfterClick: false,
	})

	s.ErrorIs(err, domain.ErrInvalidURL)
}

func (s *ManagerTestSuite) TestFollow_RecordsRedirect() {
	s.manager.WithMetrics(s.metrics)

	link := &domain.Link{
		TargetURL: "https://example.com",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
		BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil)
	s.metrics.EXPECT().IncRedirects()

	_, err := s.manager.Follow(s.T().Context(), "the-slug", openReader())

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestFollow_BurnAfterClick_RecordsRedirect() {
	s.manager.WithMetrics(s.metrics)

	link := &domain.Link{
		TargetURL: "https://example.com", BurnAfterClick: true,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", EditCode: "", Views: 0,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().Consume(gomock.Any(), gomock.Any(), testNow()).Return(link, nil)
	s.metrics.EXPECT().IncRedirects()

	_, err := s.manager.Follow(s.T().Context(), "burn-slug", openReader())

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestFollow_NotFound_DoesNotRecordRedirect() {
	s.manager.WithMetrics(s.metrics)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)
	// s.metrics.EXPECT().IncRedirects() deliberately absent — only a served redirect counts.

	_, err := s.manager.Follow(s.T().Context(), "missing", openReader())

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestCreate_RandomSlugCollision_RecordsCollision() {
	s.manager.WithMetrics(s.metrics)

	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
	)
	s.metrics.EXPECT().IncCollisions()
	s.metrics.EXPECT().IncLinksCreated()

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		CustomHash: "", EditCode: "", BurnAfterClick: false,
	})

	s.Require().NoError(err)
}

// TestCreate_CustomAliasConflict_DoesNotRecordCollision proves the scoping decision documented on
// the Metrics interface: a rejected custom alias is normal user behavior, not a slug-space health
// signal, so it must not increment IncCollisions the way a random-slug retry does.
func (s *ManagerTestSuite) TestCreate_CustomAliasConflict_DoesNotRecordCollision() {
	s.manager.WithMetrics(s.metrics)

	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
	s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), gomock.Any(), testNow()).Return(false, nil)
	// s.metrics.EXPECT().IncCollisions() deliberately absent.

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{
		CustomHash: "taken",
		ExpiresAt:  nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		EditCode: "", BurnAfterClick: false,
	})

	s.ErrorIs(err, domain.ErrSlugConflict)
}

// Delete.

func (s *ManagerTestSuite) TestDelete_OwnerBypassesEditCode() {
	owner := uuid.New()
	existing := &domain.Link{
		EditCode: "secret123456", OwnerID: &owner,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
		ID: "", TargetURL: "", Views: 0, BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	err := s.manager.Delete(s.T().Context(), "the-slug", "", accountReader(owner))

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_EvictsCache() {
	s.manager.WithCache(s.cache)

	owner := uuid.New()
	existing := &domain.Link{
		EditCode: "secret123456", OwnerID: &owner,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
		ID: "", TargetURL: "", Views: 0, BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	s.cache.EXPECT().Delete(gomock.Any())

	err := s.manager.Delete(s.T().Context(), "the-slug", "", accountReader(owner))

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_NonOwnerNeedsEditCode() {
	owner := uuid.New()
	other := uuid.New()
	existing := &domain.Link{
		EditCode: "secret123456", OwnerID: &owner,
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, DirectRedirect: nil,
		ID: "", TargetURL: "", Views: 0, BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)

	err := s.manager.Delete(s.T().Context(), "the-slug", "wrong-code", accountReader(other))

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

func (s *ManagerTestSuite) TestDelete_CorrectEditCode() {
	existing := &domain.Link{
		EditCode:  "secret123456",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", TargetURL: "", Views: 0,
		BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	err := s.manager.Delete(s.T().Context(), "the-slug", "secret123456", openReader())

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_AnonymousLinkAlwaysNeedsEditCode() {
	existing := &domain.Link{
		EditCode:  "secret123456",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, ID: "", TargetURL: "", Views: 0,
		BurnAfterClick: false,
	}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)

	err := s.manager.Delete(s.T().Context(), "the-slug", "wrong", openReader())

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

func (s *ManagerTestSuite) TestDelete_NotFound() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

	err := s.manager.Delete(s.T().Context(), "missing", "code", openReader())

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestDecryptionFailureLogsOnlyHashedSlug() {
	var buf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level:     slog.LevelDebug,
		AddSource: false, ReplaceAttr: nil,
	}))
	note := &domain.Link{
		ID: "secret-slug", TargetURL: "invalid",
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		DirectRedirect: nil, EditCode: "", Views: 0, BurnAfterClick: false,
	}
	err := decryptTargetURL(s.T().Context(), failingEncryptor(s.ctrl), log, note)
	s.Require().ErrorIs(err, domain.ErrNotFound)
	s.Contains(buf.String(), domain.HashSlug(note.ID))
	s.NotContains(buf.String(), note.ID)
}

// openReader is the reader of a server without accounts: no privacy check, and a deliberate
// access that may consume a burn link.
func openReader() domain.Reader {
	return domain.Reader{CallerID: uuid.Nil, Authenticated: false, AllowBurn: true, SkipPrivacy: true}
}

// heldReader is openReader without the burn intent: a preview fetch or a scanner.
func heldReader() domain.Reader {
	return domain.Reader{CallerID: uuid.Nil, Authenticated: false, AllowBurn: false, SkipPrivacy: true}
}

// accountReader is a signed-in reader with the account ID id and a deliberate access.
func accountReader(id uuid.UUID) domain.Reader {
	return domain.Reader{CallerID: id, Authenticated: true, AllowBurn: true, SkipPrivacy: false}
}

// --- test doubles: mockgen mocks with fixed behavior ---

// passthroughEncryptor returns an Encryptor mock that returns content unchanged.
func passthroughEncryptor(ctrl *gomock.Controller) *MockEncryptor {
	enc := NewMockEncryptor(ctrl)
	enc.EXPECT().Encrypt(gomock.Any(), gomock.Any()).
		DoAndReturn(func(plaintext, _ string) (string, error) { return plaintext, nil }).AnyTimes()
	enc.EXPECT().Decrypt(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ciphertext, _, _ string) (string, error) { return ciphertext, nil }).AnyTimes()

	return enc
}

// identityHasher returns an EditCodeHasher mock that keeps edit codes as plaintext. A
// caller-supplied code gets the "user:" prefix, so tests can see which hash the usecase selected.
func identityHasher(ctrl *gomock.Controller) *MockEditCodeHasher {
	hasher := NewMockEditCodeHasher(ctrl)
	hasher.EXPECT().Hash(gomock.Any()).DoAndReturn(func(code string) (string, error) { return code, nil }).AnyTimes()
	hasher.EXPECT().HashUserProvided(gomock.Any()).
		DoAndReturn(func(code string) (string, error) { return "user:" + code, nil }).AnyTimes()
	hasher.EXPECT().Verify(gomock.Any(), gomock.Any()).
		DoAndReturn(func(hash, code string) bool { return hash == code || hash == "user:"+code }).AnyTimes()

	return hasher
}

// failingEncryptor returns an Encryptor mock whose Decrypt always fails.
func failingEncryptor(ctrl *gomock.Controller) *MockEncryptor {
	enc := NewMockEncryptor(ctrl)
	enc.EXPECT().Encrypt(gomock.Any(), gomock.Any()).
		DoAndReturn(func(plaintext, _ string) (string, error) { return plaintext, nil }).AnyTimes()
	enc.EXPECT().Decrypt(gomock.Any(), gomock.Any(), gomock.Any()).
		Return("", errors.New("decryption failed")).AnyTimes()

	return enc
}
