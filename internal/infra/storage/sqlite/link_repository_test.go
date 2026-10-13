package sqlite

import (
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

type LinkRepositoryTestSuite struct {
	suite.Suite

	db   *bun.DB
	repo *LinkRepository
}

func (s *LinkRepositoryTestSuite) SetupTest() {
	db, err := Open("file::memory:?cache=shared")
	s.Require().NoError(err)

	s.db = db
	s.Require().NoError(s.db.Ping())

	_, err = s.db.NewCreateTable().Model((*link)(nil)).IfNotExists().Exec(s.T().Context())
	s.Require().NoError(err)

	s.repo = NewLinkRepository(s.db, slog.Default())
}

func (s *LinkRepositoryTestSuite) TearDownTest() {
	s.Require().NoError(s.db.Close())
}

func TestLinkRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(LinkRepositoryTestSuite))
}

func (s *LinkRepositoryTestSuite) TestCreate_OK() {
	domLink := &domain.Link{ID: "link-1", TargetURL: "ciphertext", CreatedAt: time.Now()}

	err := s.repo.Create(s.T().Context(), domLink)

	s.Require().NoError(err)
}

// TestCreate_SweepsExpiredLinks verifies the lazy-sweep-on-Create pattern (mirroring
// session/invite repositories): a Create call opportunistically deletes every already-expired
// link in the table, not just the one being inserted — this is what keeps a link that expires
// and is never followed again from lingering forever, since Redirect's own delete-on-read only
// cleans up links that ARE read after expiry.
func (s *LinkRepositoryTestSuite) TestCreate_SweepsExpiredLinks() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Hour)

	// Insert directly, bypassing repo.Create, so this row's own insert doesn't trigger a sweep
	// that immediately deletes itself.
	_, err := s.db.NewInsert().Model(&link{
		ID: "already-expired", TargetURL: "a", CreatedAt: past, ExpiresAt: &past,
	}).Exec(ctx)
	s.Require().NoError(err)

	// This Create's sweep should delete the row above.
	s.Require().NoError(s.repo.Create(ctx, &domain.Link{ID: "fresh", TargetURL: "b", CreatedAt: time.Now()}))

	_, err = s.repo.GetByID(ctx, "already-expired")
	s.Require().ErrorIs(err, domain.ErrNotFound, "expired link must be swept by the next Create")

	_, err = s.repo.GetByID(ctx, "fresh")
	s.NoError(err, "the newly created link itself must survive its own sweep")
}

func (s *LinkRepositoryTestSuite) TestCreate_DuplicateSlug() {
	ctx := s.T().Context()

	l := &domain.Link{ID: "dup-1", TargetURL: "a", CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	dup := &domain.Link{ID: "dup-1", TargetURL: "b", CreatedAt: time.Now()}
	err := s.repo.Create(ctx, dup)

	s.ErrorIs(err, domain.ErrSlugConflict)
}

func (s *LinkRepositoryTestSuite) TestCreate_AllFields() {
	ctx := s.T().Context()

	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	owner := uuid.New()
	authenticated := domain.PrivacyAuthenticated
	directRedirect := false
	domLink := &domain.Link{
		ID:             "all-fields",
		TargetURL:      "ciphertext",
		EditCode:       "s3cr3t",
		ExpiresAt:      &future,
		OwnerID:        &owner,
		Privacy:        &authenticated,
		DirectRedirect: &directRedirect,
		BurnAfterClick: true,
		CreatedAt:      time.Now(),
	}
	s.Require().NoError(s.repo.Create(ctx, domLink))

	got, err := s.repo.GetByID(ctx, "all-fields")
	s.Require().NoError(err)
	s.Equal("ciphertext", got.TargetURL)
	s.Equal("s3cr3t", got.EditCode)
	s.True(got.BurnAfterClick)
	s.NotNil(got.ExpiresAt)
	s.Require().NotNil(got.OwnerID)
	s.Equal(owner, *got.OwnerID)
	s.Equal(domain.PrivacyAuthenticated, got.EffectivePrivacy())
	s.Require().NotNil(got.DirectRedirect)
	s.False(*got.DirectRedirect)
}

// TestCreate_DirectRedirectDefaultsToNil verifies that omitting the per-link override persists
// NULL, not a boolean — GetByID must round-trip it back to nil so EffectiveDirectRedirect falls
// back to the server default, not to a stray false.
func (s *LinkRepositoryTestSuite) TestCreate_DirectRedirectDefaultsToNil() {
	ctx := s.T().Context()

	l := &domain.Link{ID: "no-override", TargetURL: "a", CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	got, err := s.repo.GetByID(ctx, "no-override")
	s.Require().NoError(err)
	s.Nil(got.DirectRedirect)
}

// TestCreate_NoOwnerID_DefaultsToPublicPrivacy verifies an anonymously-created link (no
// authenticated caller, no explicit privacy) persists OwnerID as nil and privacy as public —
// mirrors note_repository_test.go's TestCreate_NoOwnerID.
func (s *LinkRepositoryTestSuite) TestCreate_NoOwnerID_DefaultsToPublicPrivacy() {
	ctx := s.T().Context()

	l := &domain.Link{ID: "anon", TargetURL: "ciphertext", CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	got, err := s.repo.GetByID(ctx, "anon")
	s.Require().NoError(err)
	s.Nil(got.OwnerID)
	s.Equal(domain.PrivacyPublic, got.EffectivePrivacy())
}

func (s *LinkRepositoryTestSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.T().Context(), "missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *LinkRepositoryTestSuite) TestExists_True() {
	ctx := s.T().Context()

	l := &domain.Link{ID: "exists-1", TargetURL: "a", CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	exists, err := s.repo.Exists(ctx, "exists-1")
	s.Require().NoError(err)
	s.True(exists)
}

func (s *LinkRepositoryTestSuite) TestExists_False() {
	exists, err := s.repo.Exists(s.T().Context(), "missing")

	s.Require().NoError(err)
	s.False(exists)
}

func (s *LinkRepositoryTestSuite) TestIncrementViews_OK() {
	ctx := s.T().Context()

	l := &domain.Link{ID: "views-ok", TargetURL: "a", CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	s.Require().NoError(s.repo.IncrementViews(ctx, "views-ok"))
	s.Require().NoError(s.repo.IncrementViews(ctx, "views-ok"))

	got, err := s.repo.GetByID(ctx, "views-ok")
	s.Require().NoError(err)
	s.Equal(2, got.Views)
}

func (s *LinkRepositoryTestSuite) TestIncrementViews_NotFound() {
	err := s.repo.IncrementViews(s.T().Context(), "views-missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *LinkRepositoryTestSuite) TestConsume_BurnAfterClick() {
	ctx := s.T().Context()

	l := &domain.Link{ID: "consume-bac", TargetURL: "a", BurnAfterClick: true, CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	got, err := s.repo.Consume(ctx, "consume-bac")
	s.Require().NoError(err)
	s.Equal("consume-bac", got.ID)

	_, err = s.repo.GetByID(ctx, "consume-bac")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *LinkRepositoryTestSuite) TestConsume_Expired() {
	ctx := s.T().Context()

	past := time.Now().Add(-time.Hour)
	l := &domain.Link{ID: "consume-exp", TargetURL: "a", ExpiresAt: &past, CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	got, err := s.repo.Consume(ctx, "consume-exp")
	s.Require().NoError(err)
	s.Equal("consume-exp", got.ID)

	_, err = s.repo.GetByID(ctx, "consume-exp")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *LinkRepositoryTestSuite) TestConsume_NotEligible() {
	ctx := s.T().Context()

	l := &domain.Link{ID: "consume-no", TargetURL: "a", CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	_, err := s.repo.Consume(ctx, "consume-no")
	s.Require().ErrorIs(err, domain.ErrNotFound)

	got, err := s.repo.GetByID(ctx, "consume-no")
	s.Require().NoError(err)
	s.Equal("consume-no", got.ID)
}

func (s *LinkRepositoryTestSuite) TestConsume_NotFound() {
	_, err := s.repo.Consume(s.T().Context(), "consume-missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *LinkRepositoryTestSuite) TestDelete_OK() {
	ctx := s.T().Context()

	l := &domain.Link{ID: "del-1", TargetURL: "a", CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	s.Require().NoError(s.repo.Delete(ctx, "del-1"))

	_, err := s.repo.GetByID(ctx, "del-1")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *LinkRepositoryTestSuite) TestDelete_NotFound() {
	err := s.repo.Delete(s.T().Context(), "del-missing")

	s.ErrorIs(err, domain.ErrNotFound)
}
