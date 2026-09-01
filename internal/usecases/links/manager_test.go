package links

import (
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

// passthroughEncryptor is a test double that returns content unchanged — mirrors
// notes.passthroughEncryptor.
type passthroughEncryptor struct{}

func (passthroughEncryptor) Encrypt(pt, _ string) (string, error) { return pt, nil }
func (passthroughEncryptor) Decrypt(ct, _ string) (string, error) { return ct, nil }

// identityHasher is a test double that stores and compares edit codes as plaintext.
type identityHasher struct{}

func (identityHasher) Hash(code string) (string, error) { return code, nil }
func (identityHasher) Verify(hash, code string) bool    { return hash == code }

// failingEncryptor is a test double whose Decrypt always returns an error.
type failingEncryptor struct{}

func (failingEncryptor) Encrypt(pt, _ string) (string, error) { return pt, nil }
func (failingEncryptor) Decrypt(_, _ string) (string, error) {
	return "", errors.New("decryption failed")
}

// noopSlugLock is a test double that grants every lock immediately — mirrors
// notes.noopSlugLock. These tests exercise business logic in isolation, never two concurrent
// Create calls, so nothing here needs the real cross-manager serialization
// internal/infra/lock.Locker provides in production.
type noopSlugLock struct{}

func (noopSlugLock) Lock(context.Context, string) (func(), error) { return func() {}, nil }

type ManagerTestSuite struct {
	suite.Suite

	ctrl    *gomock.Controller
	storage *MockStorage
	notes   *MockNotesChecker
	manager *Manager
}

func (s *ManagerTestSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.storage = NewMockStorage(s.ctrl)
	s.notes = NewMockNotesChecker(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true)
}

func (s *ManagerTestSuite) TearDownTest() {
	s.ctrl.Finish()
}

func TestManagerTestSuite(t *testing.T) {
	suite.Run(t, new(ManagerTestSuite))
}

// Create

func (s *ManagerTestSuite) TestCreate_OK() {
	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	link, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{})

	s.Require().NoError(err)
	s.Equal("https://example.com", link.TargetURL)
	s.NotEmpty(link.ID)
	s.NotEmpty(link.EditCode)
}

// TestCreate_URLSchemeWhitelist is the explicit whitelist test matrix from TODO.md's Phase 2
// checklist: http/https allowed, every other scheme (and schemeless/relative URLs) rejected with
// domain.ErrInvalidURL at Create time — before storage.Create, storage.Exists, or notes.Exists
// are ever called, so a malicious scheme never reaches persistence or the cross-table check.
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
				s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
				s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
				s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			}

			_, err := s.manager.Create(s.T().Context(), test.url, CreateOptions{})

			if test.ok {
				s.NoError(err)
			} else {
				s.ErrorIs(err, domain.ErrInvalidURL)
			}
		})
	}
}

func (s *ManagerTestSuite) TestCreate_CustomAliasConflict_WithNotes() {
	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(true, nil)

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{CustomHash: "taken"})

	s.ErrorIs(err, domain.ErrSlugConflict)
}

func (s *ManagerTestSuite) TestCreate_CustomAliasConflict_WithLinks() {
	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(true, nil)

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{CustomHash: "taken"})

	s.ErrorIs(err, domain.ErrSlugConflict)
}

func (s *ManagerTestSuite) TestCreate_RandomSlugCollision_Retries() {
	gomock.InOrder(
		s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil),
		s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(true, nil),
		s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil),
		s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil),
	)
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{})

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestCreate_RandomSlugCollision_ExhaustsRetries() {
	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil).Times(maxHashRetries)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(true, nil).Times(maxHashRetries)

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{})

	s.ErrorIs(err, domain.ErrHashCollision)
}

func (s *ManagerTestSuite) TestCreate_CustomSlugRejectsMultiSegment() {
	// Links are only served at single-segment routes (/r/{id}, /links/{id}); a path-like alias
	// would be created but unroutable, so Create must reject it up front. No repo call
	// expectations are set — gomock fails the test if the guard ever lets a slug through.
	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{CustomHash: "docs/guide"})

	s.Require().ErrorIs(err, domain.ErrInvalidSlug)

	_, err = s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{CustomHash: "a/b/c"})

	s.ErrorIs(err, domain.ErrInvalidSlug)
}

func (s *ManagerTestSuite) TestCreate_CustomSlugDisabled() {
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), false)

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{CustomHash: "my-alias"})

	s.ErrorIs(err, domain.ErrCustomSlugDisabled)
}

func (s *ManagerTestSuite) TestCreate_InvalidPrivacy() {
	p := domain.Privacy("bogus")

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{Privacy: &p})

	s.ErrorIs(err, domain.ErrInvalidPrivacy)
}

func (s *ManagerTestSuite) TestCreate_OwnerPrivacyRequiresOwner() {
	p := domain.PrivacyOwner

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{Privacy: &p})

	s.ErrorIs(err, domain.ErrOwnerPrivacyRequiresOwner)
}

func (s *ManagerTestSuite) TestCreate_OwnerPrivacy_WithOwnerID_OK() {
	owner := uuid.New()
	ownerPrivacy := domain.PrivacyOwner

	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	link, err := s.manager.Create(s.T().Context(), "https://example.com",
		CreateOptions{Privacy: &ownerPrivacy, OwnerID: &owner})

	s.Require().NoError(err)
	s.Equal(domain.PrivacyOwner, link.EffectivePrivacy())
}

// TestCreate_DirectRedirectOverride_PassedThrough verifies CreateOptions.DirectRedirect reaches
// storage.Create and the returned link unchanged — Manager itself doesn't interpret the value
// (that's LinkHandler.Redirect's job via domain.Link.EffectiveDirectRedirect), it only threads it
// through.
func (s *ManagerTestSuite) TestCreate_DirectRedirectOverride_PassedThrough() {
	override := false

	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, l *domain.Link) error {
			s.Require().NotNil(l.DirectRedirect)
			s.False(*l.DirectRedirect)

			return nil
		})

	link, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{DirectRedirect: &override})

	s.Require().NoError(err)
	s.Require().NotNil(link.DirectRedirect)
	s.False(*link.DirectRedirect)
}

func (s *ManagerTestSuite) TestCreate_StorageError() {
	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(errors.New("db error"))

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{})

	s.Require().Error(err)
}

// Redirect

func (s *ManagerTestSuite) TestRedirect_OK() {
	link := &domain.Link{TargetURL: "https://example.com"}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil)

	got, err := s.manager.Redirect(s.T().Context(), "the-slug")

	s.Require().NoError(err)
	s.Equal("https://example.com", got.TargetURL)
	s.Equal(1, got.Views)
}

func (s *ManagerTestSuite) TestRedirect_NotFound() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

	_, err := s.manager.Redirect(s.T().Context(), "missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestRedirect_Expired() {
	past := time.Now().Add(-time.Hour)
	link := &domain.Link{TargetURL: "https://example.com", ExpiresAt: &past}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Redirect(s.T().Context(), "expired-slug")

	s.ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerTestSuite) TestRedirect_BurnAfterClick_ConsumedOnce() {
	link := &domain.Link{TargetURL: "https://example.com", BurnAfterClick: true}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(link, nil)

	got, err := s.manager.Redirect(s.T().Context(), "burn-slug")

	s.Require().NoError(err)
	s.Equal("https://example.com", got.TargetURL)
}

// TestRedirect_BurnAfterClick_SecondCallNotFound verifies burn-after-click is exactly-once: a
// second Redirect for the same slug, after the row was consumed, gets ErrNotFound.
func (s *ManagerTestSuite) TestRedirect_BurnAfterClick_SecondCallNotFound() {
	link := &domain.Link{TargetURL: "https://example.com", BurnAfterClick: true}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(link, nil)

	_, err := s.manager.Redirect(s.T().Context(), "burn-slug")
	s.Require().NoError(err)

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

	_, err = s.manager.Redirect(s.T().Context(), "burn-slug")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestRedirect_DecryptFailure() {
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, failingEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true)

	link := &domain.Link{TargetURL: "ciphertext"}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)

	_, err := s.manager.Redirect(s.T().Context(), "the-slug")

	s.ErrorIs(err, domain.ErrNotFound,
		"decryption failure collapses to ErrNotFound, not a distinguishable error")
}

// Cache (WithCache) — nil by default (see SetupTest); these tests attach a MockCache locally to
// exercise resolveLink/evictCache without changing every other test's expectations.

func (s *ManagerTestSuite) TestRedirect_CacheHit_SkipsStorageGetByID() {
	cache := NewMockCache(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithCache(cache)

	cached := &domain.Link{TargetURL: "https://example.com"}
	cache.EXPECT().Get(gomock.Any()).Return(cached, true)
	// storage.GetByID deliberately has no EXPECT() — a cache hit must never reach storage.
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil)

	got, err := s.manager.Redirect(s.T().Context(), "the-slug")

	s.Require().NoError(err)
	s.Equal("https://example.com", got.TargetURL)
}

func (s *ManagerTestSuite) TestRedirect_CacheMiss_PopulatesCacheForNonBurnLink() {
	cache := NewMockCache(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithCache(cache)

	link := &domain.Link{TargetURL: "https://example.com"}

	cache.EXPECT().Get(gomock.Any()).Return(nil, false)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	cache.EXPECT().Set(gomock.Any(), gomock.Any(), link)
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Redirect(s.T().Context(), "the-slug")

	s.Require().NoError(err)
}

// TestRedirect_BurnAfterClick_NeverCached verifies a burn-after-click link is never written to
// the cache — caching one would risk a second, concurrent Redirect being served a stale
// "not yet consumed" copy instead of relying on storage.Consume's atomic exactly-once guarantee.
func (s *ManagerTestSuite) TestRedirect_BurnAfterClick_NeverCached() {
	cache := NewMockCache(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithCache(cache)

	link := &domain.Link{TargetURL: "https://example.com", BurnAfterClick: true}

	cache.EXPECT().Get(gomock.Any()).Return(nil, false)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	// cache.Set deliberately has no EXPECT() — a burn-after-click link must never be cached.
	s.storage.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(link, nil)

	_, err := s.manager.Redirect(s.T().Context(), "burn-slug")

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestRedirect_Expired_EvictsCache() {
	cache := NewMockCache(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithCache(cache)

	past := time.Now().Add(-time.Hour)
	link := &domain.Link{TargetURL: "https://example.com", ExpiresAt: &past}

	cache.EXPECT().Get(gomock.Any()).Return(nil, false)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)
	cache.EXPECT().Delete(gomock.Any())

	_, err := s.manager.Redirect(s.T().Context(), "expired-slug")

	s.ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerTestSuite) TestPeek_CacheHit_SkipsStorage() {
	cache := NewMockCache(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithCache(cache)

	cached := &domain.Link{TargetURL: "https://example.com"}
	cache.EXPECT().Get(gomock.Any()).Return(cached, true)

	got, err := s.manager.Peek(s.T().Context(), "the-slug")

	s.Require().NoError(err)
	s.Equal("https://example.com", got.TargetURL)
}

// Metrics (WithMetrics) — nil by default (see SetupTest); these tests attach a MockMetrics
// locally, exactly like the Cache tests above.

func (s *ManagerTestSuite) TestCreate_RecordsLinksCreated() {
	metrics := NewMockMetrics(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithMetrics(metrics)

	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	metrics.EXPECT().IncLinksCreated()

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{})

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestCreate_InvalidURL_DoesNotRecordLinksCreated() {
	metrics := NewMockMetrics(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithMetrics(metrics)
	// metrics.EXPECT().IncLinksCreated() deliberately absent — a rejected create must not count.

	_, err := s.manager.Create(s.T().Context(), "javascript:alert(1)", CreateOptions{})

	s.ErrorIs(err, domain.ErrInvalidURL)
}

func (s *ManagerTestSuite) TestRedirect_RecordsRedirect() {
	metrics := NewMockMetrics(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithMetrics(metrics)

	link := &domain.Link{TargetURL: "https://example.com"}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().IncrementViews(gomock.Any(), gomock.Any()).Return(nil)
	metrics.EXPECT().IncRedirects()

	_, err := s.manager.Redirect(s.T().Context(), "the-slug")

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestRedirect_BurnAfterClick_RecordsRedirect() {
	metrics := NewMockMetrics(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithMetrics(metrics)

	link := &domain.Link{TargetURL: "https://example.com", BurnAfterClick: true}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(link, nil)
	s.storage.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(link, nil)
	metrics.EXPECT().IncRedirects()

	_, err := s.manager.Redirect(s.T().Context(), "burn-slug")

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestRedirect_NotFound_DoesNotRecordRedirect() {
	metrics := NewMockMetrics(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithMetrics(metrics)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)
	// metrics.EXPECT().IncRedirects() deliberately absent — only a served redirect counts.

	_, err := s.manager.Redirect(s.T().Context(), "missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerTestSuite) TestCreate_RandomSlugCollision_RecordsCollision() {
	metrics := NewMockMetrics(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithMetrics(metrics)

	gomock.InOrder(
		s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil),
		s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(true, nil),
		s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil),
		s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil),
	)
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	metrics.EXPECT().IncCollisions()
	metrics.EXPECT().IncLinksCreated()

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{})

	s.Require().NoError(err)
}

// TestCreate_CustomAliasConflict_DoesNotRecordCollision proves the scoping decision documented on
// the Metrics interface: a rejected custom alias is normal user behavior, not a slug-space health
// signal, so it must not increment IncCollisions the way a random-slug retry does.
func (s *ManagerTestSuite) TestCreate_CustomAliasConflict_DoesNotRecordCollision() {
	metrics := NewMockMetrics(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithMetrics(metrics)

	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(true, nil)
	// metrics.EXPECT().IncCollisions() deliberately absent.

	_, err := s.manager.Create(s.T().Context(), "https://example.com", CreateOptions{CustomHash: "taken"})

	s.ErrorIs(err, domain.ErrSlugConflict)
}

// Delete

func (s *ManagerTestSuite) TestDelete_OwnerBypassesEditCode() {
	owner := uuid.New()
	existing := &domain.Link{EditCode: "secret123456", OwnerID: &owner}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	err := s.manager.Delete(s.T().Context(), "the-slug", "", owner)

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_EvictsCache() {
	cache := NewMockCache(s.ctrl)
	s.manager = NewManager(s.storage, s.notes, noopSlugLock{}, passthroughEncryptor{}, identityHasher{},
		slog.New(slog.DiscardHandler), true).WithCache(cache)

	owner := uuid.New()
	existing := &domain.Link{EditCode: "secret123456", OwnerID: &owner}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)
	cache.EXPECT().Delete(gomock.Any())

	err := s.manager.Delete(s.T().Context(), "the-slug", "", owner)

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_NonOwnerNeedsEditCode() {
	owner := uuid.New()
	other := uuid.New()
	existing := &domain.Link{EditCode: "secret123456", OwnerID: &owner}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)

	err := s.manager.Delete(s.T().Context(), "the-slug", "wrong-code", other)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

func (s *ManagerTestSuite) TestDelete_CorrectEditCode() {
	existing := &domain.Link{EditCode: "secret123456"}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)
	s.storage.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	err := s.manager.Delete(s.T().Context(), "the-slug", "secret123456", uuid.Nil)

	s.Require().NoError(err)
}

func (s *ManagerTestSuite) TestDelete_AnonymousLinkAlwaysNeedsEditCode() {
	existing := &domain.Link{EditCode: "secret123456"}
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(existing, nil)

	err := s.manager.Delete(s.T().Context(), "the-slug", "wrong", uuid.Nil)

	s.ErrorIs(err, domain.ErrInvalidEditCode)
}

func (s *ManagerTestSuite) TestDelete_NotFound() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

	err := s.manager.Delete(s.T().Context(), "missing", "code", uuid.Nil)

	s.ErrorIs(err, domain.ErrNotFound)
}
