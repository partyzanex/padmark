package postgres

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

type RevealRepositoryTestSuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
	repo      *RevealRepository
}

func (s *RevealRepositoryTestSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_reveal_test"),
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

	s.repo = NewRevealRepository(s.db)
}

func (s *RevealRepositoryTestSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *RevealRepositoryTestSuite) SetupTest() {
	_, err := s.db.NewTruncateTable().
		TableExpr("reveal_tokens").
		Cascade().
		Exec(s.T().Context())
	s.Require().NoError(err)
}

func TestRevealRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(RevealRepositoryTestSuite))
}

// ── Issue ──

func (s *RevealRepositoryTestSuite) TestIssue_ReturnsNonEmptyToken() {
	tok, err := s.repo.Issue(s.T().Context(), "note-1")

	s.Require().NoError(err)
	s.NotEmpty(tok)
}

func (s *RevealRepositoryTestSuite) TestIssue_StoredInDB() {
	ctx := s.T().Context()

	tok, err := s.repo.Issue(ctx, "note-abc")
	s.Require().NoError(err)

	var row revealTokenRow

	err = s.db.NewSelect().Model(&row).Where("token = ?", tok).Scan(ctx)
	s.Require().NoError(err)
	s.Equal("note-abc", row.NoteID)
	s.Nil(row.UsedAt)
	s.True(row.ExpiresAt.After(time.Now()))
}

func (s *RevealRepositoryTestSuite) TestIssue_SweepsExpiredTokens() {
	ctx := s.T().Context()

	// Insert a stale token directly.
	stale := &revealTokenRow{
		Token:     "stale-token",
		NoteID:    "note-stale",
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	_, err := s.db.NewInsert().Model(stale).Exec(ctx)
	s.Require().NoError(err)

	// Issue a new token — triggers lazy sweep.
	_, err = s.repo.Issue(ctx, "note-new")
	s.Require().NoError(err)

	var count int

	err = s.db.NewSelect().
		TableExpr("reveal_tokens").
		ColumnExpr("count(*)").
		Where("token = ?", "stale-token").
		Scan(ctx, &count)
	s.Require().NoError(err)
	s.Zero(count, "expired token must be swept")
}

// ── Consume ──

func (s *RevealRepositoryTestSuite) TestConsume_OK() {
	ctx := s.T().Context()

	tok, err := s.repo.Issue(ctx, "note-consume")
	s.Require().NoError(err)

	ok := s.repo.Consume(ctx, tok, "note-consume")

	s.True(ok)
}

func (s *RevealRepositoryTestSuite) TestConsume_MarksUsedAt() {
	ctx := s.T().Context()

	tok, err := s.repo.Issue(ctx, "note-mark")
	s.Require().NoError(err)

	ok := s.repo.Consume(ctx, tok, "note-mark")
	s.Require().True(ok)

	var row revealTokenRow

	err = s.db.NewSelect().Model(&row).Where("token = ?", tok).Scan(ctx)
	s.Require().NoError(err)
	s.NotNil(row.UsedAt, "used_at must be set after Consume")
}

func (s *RevealRepositoryTestSuite) TestConsume_AlreadyUsed() {
	ctx := s.T().Context()

	tok, err := s.repo.Issue(ctx, "note-double")
	s.Require().NoError(err)

	ok := s.repo.Consume(ctx, tok, "note-double")
	s.Require().True(ok)

	ok = s.repo.Consume(ctx, tok, "note-double")
	s.False(ok, "second Consume must fail")
}

func (s *RevealRepositoryTestSuite) TestConsume_UnknownToken() {
	ok := s.repo.Consume(s.T().Context(), "no-such-token", "any-note")

	s.False(ok)
}

func (s *RevealRepositoryTestSuite) TestConsume_ExpiredToken() {
	ctx := s.T().Context()

	expired := &revealTokenRow{
		Token:     "expired-token",
		NoteID:    "note-exp",
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	_, err := s.db.NewInsert().Model(expired).Exec(ctx)
	s.Require().NoError(err)

	ok := s.repo.Consume(ctx, "expired-token", "note-exp")
	s.False(ok)
}

// TestConsume_ExactExpiryBoundary verifies that a token whose expires_at equals
// the current time is rejected. The query uses expires_at > now (strict),
// so a token at the exact boundary must not be consumed.
func (s *RevealRepositoryTestSuite) TestConsume_ExactExpiryBoundary_Rejected() {
	ctx := s.T().Context()

	boundary := &revealTokenRow{
		Token:     "boundary-token",
		NoteID:    "note-boundary",
		ExpiresAt: time.Now(),
	}
	_, err := s.db.NewInsert().Model(boundary).Exec(ctx)
	s.Require().NoError(err)

	ok := s.repo.Consume(ctx, "boundary-token", "note-boundary")
	s.False(ok, "token at exact expiry boundary must be rejected (expires_at > now, not >=)")
}

// TestConsume_WrongNoteID_TokenPreserved verifies that a valid token is NOT consumed
// when the noteID argument does not match the token's bound note_id.
func (s *RevealRepositoryTestSuite) TestConsume_WrongNoteID_TokenPreserved() {
	ctx := s.T().Context()

	tok, err := s.repo.Issue(ctx, "note-real")
	s.Require().NoError(err)

	// Attempt Consume with wrong noteID.
	ok := s.repo.Consume(ctx, tok, "note-wrong")
	s.False(ok, "must reject token bound to a different noteID")

	// Token must still be consumable with the correct noteID.
	ok = s.repo.Consume(ctx, tok, "note-real")
	s.True(ok, "token must still be valid after cross-note Consume attempt")
}
