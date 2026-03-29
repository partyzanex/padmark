package postgres

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
)

type RepositoryTestSuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
	repo      *Repository
}

func (s *RepositoryTestSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
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

	s.Require().NoError(Migrate(ctx, s.db))

	s.repo = NewRepository(s.db)
}

func (s *RepositoryTestSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *RepositoryTestSuite) SetupTest() {
	_, err := s.db.NewTruncateTable().
		TableExpr("notes").
		Cascade().
		Exec(s.T().Context())
	s.Require().NoError(err)
}

func TestRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(RepositoryTestSuite))
}

func newNote(id, title, content string) *domain.Note {
	now := time.Now().Truncate(time.Microsecond)

	return &domain.Note{
		ID:          id,
		Title:       title,
		Content:     content,
		ContentType: domain.ContentTypeMarkdown,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// ── Create ──

func (s *RepositoryTestSuite) TestCreate_OK() {
	err := s.repo.Create(s.T().Context(), newNote("c1", "hello", "world"))

	s.Require().NoError(err)
}

func (s *RepositoryTestSuite) TestCreate_DuplicateSlug() {
	ctx := s.T().Context()
	s.Require().NoError(s.repo.Create(ctx, newNote("dup", "a", "b")))

	err := s.repo.Create(ctx, newNote("dup", "c", "d"))

	s.ErrorIs(err, domain.ErrSlugConflict)
}

func (s *RepositoryTestSuite) TestCreate_AllFields() {
	ctx := s.T().Context()

	future := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	n := newNote("full", "title", "content")
	n.EditCode = "secret123"
	n.ExpiresAt = &future
	n.BurnAfterReading = true

	s.Require().NoError(s.repo.Create(ctx, n))

	got, err := s.repo.Get(ctx, "full")
	s.Require().NoError(err)
	s.Equal("secret123", got.EditCode)
	s.True(got.BurnAfterReading)
	s.Require().NotNil(got.ExpiresAt)
	s.WithinDuration(future, *got.ExpiresAt, time.Second)
}

// ── Get ──

func (s *RepositoryTestSuite) TestGet_OK() {
	ctx := s.T().Context()
	n := newNote("g1", "test", "body")
	s.Require().NoError(s.repo.Create(ctx, n))

	got, err := s.repo.Get(ctx, "g1")

	s.Require().NoError(err)
	s.Equal("test", got.Title)
	s.Equal("body", got.Content)
	s.Equal(domain.ContentTypeMarkdown, got.ContentType)
	s.Equal(0, got.Views)
}

func (s *RepositoryTestSuite) TestGet_NotFound() {
	_, err := s.repo.Get(s.T().Context(), "nonexistent")

	s.ErrorIs(err, domain.ErrNotFound)
}

// ── Update ──

func (s *RepositoryTestSuite) TestUpdate_OK() {
	ctx := s.T().Context()
	s.Require().NoError(s.repo.Create(ctx, newNote("u1", "old", "old")))

	updated := &domain.Note{
		Title:       "new",
		Content:     "new",
		ContentType: domain.ContentTypePlain,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	err := s.repo.Update(ctx, "u1", updated)
	s.Require().NoError(err)

	got, err := s.repo.Get(ctx, "u1")
	s.Require().NoError(err)
	s.Equal("new", got.Title)
	s.Equal("new", got.Content)
	s.Equal(domain.ContentTypePlain, got.ContentType)
}

func (s *RepositoryTestSuite) TestUpdate_ExpiresAt() {
	ctx := s.T().Context()
	s.Require().NoError(s.repo.Create(ctx, newNote("u2", "t", "c")))

	future := time.Now().Add(2 * time.Hour).Truncate(time.Microsecond)

	updated := &domain.Note{
		Title:     "t",
		Content:   "c",
		ExpiresAt: &future,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	s.Require().NoError(s.repo.Update(ctx, "u2", updated))

	got, err := s.repo.Get(ctx, "u2")
	s.Require().NoError(err)
	s.Require().NotNil(got.ExpiresAt)
	s.WithinDuration(future, *got.ExpiresAt, time.Second)
}

func (s *RepositoryTestSuite) TestUpdate_BurnAfterReading() {
	ctx := s.T().Context()
	s.Require().NoError(s.repo.Create(ctx, newNote("u3", "t", "c")))

	updated := &domain.Note{
		Title:            "t",
		Content:          "c",
		BurnAfterReading: true,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	s.Require().NoError(s.repo.Update(ctx, "u3", updated))

	got, err := s.repo.Get(ctx, "u3")
	s.Require().NoError(err)
	s.True(got.BurnAfterReading)
}

func (s *RepositoryTestSuite) TestUpdate_NotFound() {
	note := &domain.Note{
		Title:     "x",
		Content:   "x",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err := s.repo.Update(s.T().Context(), "nonexistent", note)

	s.ErrorIs(err, domain.ErrNotFound)
}

// ── IncrementViews ──

func (s *RepositoryTestSuite) TestIncrementViews_OK() {
	ctx := s.T().Context()
	s.Require().NoError(s.repo.Create(ctx, newNote("v1", "t", "c")))

	s.Require().NoError(s.repo.IncrementViews(ctx, "v1"))
	s.Require().NoError(s.repo.IncrementViews(ctx, "v1"))
	s.Require().NoError(s.repo.IncrementViews(ctx, "v1"))

	got, err := s.repo.Get(ctx, "v1")
	s.Require().NoError(err)
	s.Equal(3, got.Views)
}

func (s *RepositoryTestSuite) TestIncrementViews_NotFound() {
	err := s.repo.IncrementViews(s.T().Context(), "nonexistent")

	s.ErrorIs(err, domain.ErrNotFound)
}

// ── Delete ──

func (s *RepositoryTestSuite) TestDelete_OK() {
	ctx := s.T().Context()
	s.Require().NoError(s.repo.Create(ctx, newNote("d1", "del", "me")))

	s.Require().NoError(s.repo.Delete(ctx, "d1"))

	_, err := s.repo.Get(ctx, "d1")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *RepositoryTestSuite) TestDelete_NotFound() {
	err := s.repo.Delete(s.T().Context(), "nonexistent")

	s.ErrorIs(err, domain.ErrNotFound)
}

// Consume

func (s *RepositoryTestSuite) TestConsume_OK() {
	ctx := s.T().Context()
	s.Require().NoError(s.repo.Create(ctx, newNote("con-ok", "burn", "me")))

	got, err := s.repo.Consume(ctx, "con-ok")
	s.Require().NoError(err)
	s.Equal("con-ok", got.ID)
	s.Equal("burn", got.Title)

	_, err = s.repo.Get(ctx, "con-ok")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *RepositoryTestSuite) TestConsume_NotFound() {
	_, err := s.repo.Consume(s.T().Context(), "con-missing")

	s.ErrorIs(err, domain.ErrNotFound)
}

// ── Migrate ──

func (s *RepositoryTestSuite) TestMigrate_Idempotent() {
	// Running Migrate again should not fail (all migrations already applied).
	err := Migrate(s.T().Context(), s.db)

	s.Require().NoError(err)
}

// ── Roundtrip ──

func (s *RepositoryTestSuite) TestRoundtrip_CRUD() {
	ctx := s.T().Context()

	// Create
	n := newNote("rt1", "original", "# Hello")
	n.EditCode = "edit123"
	s.Require().NoError(s.repo.Create(ctx, n))

	// Read
	got, err := s.repo.Get(ctx, "rt1")
	s.Require().NoError(err)
	s.Equal("original", got.Title)
	s.Equal("# Hello", got.Content)
	s.Equal("edit123", got.EditCode)

	// Update
	updated := &domain.Note{
		Title:       "updated",
		Content:     "# Updated",
		ContentType: domain.ContentTypePlain,
		CreatedAt:   got.CreatedAt,
		UpdatedAt:   time.Now(),
	}
	s.Require().NoError(s.repo.Update(ctx, "rt1", updated))

	got, err = s.repo.Get(ctx, "rt1")
	s.Require().NoError(err)
	s.Equal("updated", got.Title)
	s.Equal(domain.ContentTypePlain, got.ContentType)

	// Views
	s.Require().NoError(s.repo.IncrementViews(ctx, "rt1"))

	got, err = s.repo.Get(ctx, "rt1")
	s.Require().NoError(err)
	s.Equal(1, got.Views)

	// Delete
	s.Require().NoError(s.repo.Delete(ctx, "rt1"))

	_, err = s.repo.Get(ctx, "rt1")
	s.True(errors.Is(err, domain.ErrNotFound))
}
