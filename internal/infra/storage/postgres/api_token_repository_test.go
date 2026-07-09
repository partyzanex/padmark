package postgres

import (
	"database/sql"
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

type APITokenRepositoryTestSuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
	repo      *APITokenRepository
	users     *UserRepository
}

func (s *APITokenRepositoryTestSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_apitoken_test"),
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

	_, err = Migrate(ctx, s.db)
	s.Require().NoError(err)

	s.repo = NewAPITokenRepository(s.db)
	s.users = NewUserRepository(s.db)
}

func (s *APITokenRepositoryTestSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *APITokenRepositoryTestSuite) SetupTest() {
	ctx := s.T().Context()

	_, err := s.db.NewTruncateTable().
		TableExpr("api_tokens").
		Cascade().
		Exec(ctx)
	s.Require().NoError(err)

	_, err = s.db.NewTruncateTable().
		TableExpr("users").
		Cascade().
		Exec(ctx)
	s.Require().NoError(err)
}

func TestAPITokenRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(APITokenRepositoryTestSuite))
}

// createUser inserts a test user and returns the generated UUID (user_id is UUID in Postgres).
func (s *APITokenRepositoryTestSuite) createUser(suffix string) string {
	s.T().Helper()

	id := uuid.New().String()
	err := s.users.Create(s.T().Context(), &domain.User{
		ID:           id,
		Username:     "user-" + suffix,
		TOTPSecret:   "secret",
		PasswordHash: "hash",
		KDFSalt:      []byte("saltsaltsalt1234"),
		CreatedAt:    time.Now().Truncate(time.Second),
	})
	s.Require().NoError(err)

	return id
}

func (s *APITokenRepositoryTestSuite) newToken(userID, hash string) *domain.APIToken {
	return &domain.APIToken{
		UserID:    userID,
		TokenHash: hash,
		CreatedAt: time.Now().Truncate(time.Second),
	}
}

// ── Create / GetByHash ──

func (s *APITokenRepositoryTestSuite) TestCreate_GetByHash_RoundTrip() {
	ctx := s.T().Context()
	userID := s.createUser("u1")

	tok := s.newToken(userID, "hash-1")
	s.Require().NoError(s.repo.Create(ctx, tok))

	got, err := s.repo.GetByHash(ctx, "hash-1")
	s.Require().NoError(err)
	s.Equal(tok.UserID, got.UserID)
	s.Equal(tok.TokenHash, got.TokenHash)
	s.WithinDuration(tok.CreatedAt, got.CreatedAt, time.Second)
	s.Nil(got.ExpiresAt, "freshly issued token has no expiry")
	s.Nil(got.LastUsedAt, "freshly issued token was never used")
}

func (s *APITokenRepositoryTestSuite) TestGetByHash_Unknown_ReturnsErrNotFound() {
	_, err := s.repo.GetByHash(s.T().Context(), "no-such-hash")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *APITokenRepositoryTestSuite) TestCreate_WithExpiresAt_RoundTrip() {
	ctx := s.T().Context()
	userID := s.createUser("u2")

	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	tok := s.newToken(userID, "hash-exp")
	tok.ExpiresAt = &exp
	s.Require().NoError(s.repo.Create(ctx, tok))

	got, err := s.repo.GetByHash(ctx, "hash-exp")
	s.Require().NoError(err)
	s.Require().NotNil(got.ExpiresAt)
	s.WithinDuration(exp, *got.ExpiresAt, time.Second)
}

// ── List ──

func (s *APITokenRepositoryTestSuite) TestList_ReturnsNewestFirst() {
	ctx := s.T().Context()
	userID := s.createUser("u3")

	now := time.Now().Truncate(time.Second)

	newest := s.newToken(userID, "h-new")
	newest.CreatedAt = now

	middle := s.newToken(userID, "h-mid")
	middle.CreatedAt = now.Add(-time.Hour)

	oldest := s.newToken(userID, "h-old")
	oldest.CreatedAt = now.Add(-2 * time.Hour)

	// Insert out of chronological order to prove ordering is by created_at, not insertion.
	s.Require().NoError(s.repo.Create(ctx, middle))
	s.Require().NoError(s.repo.Create(ctx, oldest))
	s.Require().NoError(s.repo.Create(ctx, newest))

	tokens, err := s.repo.List(ctx)
	s.Require().NoError(err)
	s.Require().Len(tokens, 3)
	s.Equal("h-new", tokens[0].TokenHash)
	s.Equal("h-mid", tokens[1].TokenHash)
	s.Equal("h-old", tokens[2].TokenHash)
}

func (s *APITokenRepositoryTestSuite) TestList_Empty_ReturnsEmpty() {
	tokens, err := s.repo.List(s.T().Context())
	s.Require().NoError(err)
	s.Empty(tokens)
}

// ── CountByUser ──

func (s *APITokenRepositoryTestSuite) TestCountByUser_CountsOnlyThatUsersTokens() {
	ctx := s.T().Context()
	userA := s.createUser("count-a")
	userB := s.createUser("count-b")

	s.Require().NoError(s.repo.Create(ctx, s.newToken(userA, "count-a-1")))
	s.Require().NoError(s.repo.Create(ctx, s.newToken(userA, "count-a-2")))
	s.Require().NoError(s.repo.Create(ctx, s.newToken(userB, "count-b-1")))

	count, err := s.repo.CountByUser(ctx, userA)
	s.Require().NoError(err)
	s.Equal(2, count)
}

func (s *APITokenRepositoryTestSuite) TestCountByUser_NoTokens_ReturnsZero() {
	userID := s.createUser("count-none")

	count, err := s.repo.CountByUser(s.T().Context(), userID)
	s.Require().NoError(err)
	s.Equal(0, count)
}

// ── RevokeByHash ──

func (s *APITokenRepositoryTestSuite) TestRevokeByHash_RemovesToken() {
	ctx := s.T().Context()
	userID := s.createUser("u4")

	tok := s.newToken(userID, "hash-del")
	s.Require().NoError(s.repo.Create(ctx, tok))
	s.Require().NoError(s.repo.RevokeByHash(ctx, "hash-del"))

	_, err := s.repo.GetByHash(ctx, "hash-del")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *APITokenRepositoryTestSuite) TestRevokeByHash_Unknown_ReturnsErrNotFound() {
	err := s.repo.RevokeByHash(s.T().Context(), "ghost-hash")
	s.ErrorIs(err, domain.ErrNotFound)
}

// ── UpdateLastUsed ──

func (s *APITokenRepositoryTestSuite) TestUpdateLastUsed_SetsTimestamp() {
	ctx := s.T().Context()
	userID := s.createUser("u5")

	tok := s.newToken(userID, "hash-lu")
	s.Require().NoError(s.repo.Create(ctx, tok))

	used := time.Now().Add(time.Minute).Truncate(time.Second)
	s.Require().NoError(s.repo.UpdateLastUsed(ctx, "hash-lu", used))

	got, err := s.repo.GetByHash(ctx, "hash-lu")
	s.Require().NoError(err)
	s.Require().NotNil(got.LastUsedAt)
	s.WithinDuration(used, *got.LastUsedAt, time.Second)
}

// TestUpdateLastUsed_UnknownHash_NoError pins the advisory contract documented on the
// method: a missing row must not turn a valid auth into a failure, so no error is returned.
func (s *APITokenRepositoryTestSuite) TestUpdateLastUsed_UnknownHash_NoError() {
	err := s.repo.UpdateLastUsed(s.T().Context(), "ghost-hash", time.Now())
	s.Require().NoError(err)
}

// ── Cascade ──

// TestCreate_CascadesWithUser verifies the ON DELETE CASCADE from users(id): deleting the
// owning user removes their API tokens. Postgres enforces foreign keys unconditionally, and
// idx_api_tokens_user_id backs the referencing-column scan this delete performs.
func (s *APITokenRepositoryTestSuite) TestCreate_CascadesWithUser() {
	ctx := s.T().Context()
	userID := s.createUser("owner")

	s.Require().NoError(s.repo.Create(ctx, s.newToken(userID, "hash-cascade")))
	s.Require().NoError(s.users.Revoke(ctx, userID))

	count, err := s.db.NewSelect().
		TableExpr("api_tokens").
		Where("user_id = ?", userID).
		Count(ctx)
	s.Require().NoError(err)
	s.Zero(count, "api tokens must cascade-delete with the owning user")
}
