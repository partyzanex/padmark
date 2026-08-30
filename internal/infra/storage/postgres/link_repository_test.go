package postgres

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
)

type LinkRepositoryTestSuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
	repo      *LinkRepository
}

func (s *LinkRepositoryTestSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	s.Require().NoError(err)

	s.container = container

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	s.Require().NoError(err)

	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))
	s.db = bun.NewDB(sqldb, pgdialect.New())
	s.Require().NoError(s.db.PingContext(ctx))

	_, migrateErr := Migrate(ctx, s.db)
	s.Require().NoError(migrateErr)

	s.repo = NewLinkRepository(s.db, slog.Default())
}

func (s *LinkRepositoryTestSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *LinkRepositoryTestSuite) SetupTest() {
	_, err := s.db.NewTruncateTable().
		TableExpr("links").
		Cascade().
		Exec(s.T().Context())
	s.Require().NoError(err)
}

func TestLinkRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(LinkRepositoryTestSuite))
}

// createTestUser inserts a minimal user row so links.owner_id (a real FK on PostgreSQL) can
// reference it — mirrors note_repository_test.go's helper of the same name.
func (s *LinkRepositoryTestSuite) createTestUser(ctx context.Context) uuid.UUID {
	s.T().Helper()

	id := uuid.New()
	err := NewUserRepository(s.db).Create(ctx, &domain.User{
		ID: id, Username: "owner-" + id.String(), TOTPSecret: "secret", PasswordHash: "hash",
		CreatedAt: time.Now(),
	})
	s.Require().NoError(err)

	return id
}

func (s *LinkRepositoryTestSuite) TestCreate_OK() {
	domLink := &domain.Link{ID: "link-1", TargetURL: "ciphertext", CreatedAt: time.Now()}

	err := s.repo.Create(s.T().Context(), domLink)

	s.Require().NoError(err)
}

// TestCreate_SweepsExpiredLinks mirrors the SQLite version — see its doc comment.
func (s *LinkRepositoryTestSuite) TestCreate_SweepsExpiredLinks() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Hour)

	_, err := s.db.NewInsert().Model(&link{
		ID: "already-expired-pg", TargetURL: "a", CreatedAt: past, ExpiresAt: &past,
	}).Exec(ctx)
	s.Require().NoError(err)

	s.Require().NoError(s.repo.Create(ctx, &domain.Link{ID: "fresh-pg", TargetURL: "b", CreatedAt: time.Now()}))

	_, err = s.repo.GetByID(ctx, "already-expired-pg")
	s.Require().ErrorIs(err, domain.ErrNotFound, "expired link must be swept by the next Create")

	_, err = s.repo.GetByID(ctx, "fresh-pg")
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
	owner := s.createTestUser(ctx)
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

// TestCreate_DirectRedirectDefaultsToNil mirrors the SQLite version — see its doc comment.
func (s *LinkRepositoryTestSuite) TestCreate_DirectRedirectDefaultsToNil() {
	ctx := s.T().Context()

	l := &domain.Link{ID: "no-override", TargetURL: "a", CreatedAt: time.Now()}
	s.Require().NoError(s.repo.Create(ctx, l))

	got, err := s.repo.GetByID(ctx, "no-override")
	s.Require().NoError(err)
	s.Nil(got.DirectRedirect)
}

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
