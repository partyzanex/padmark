package postgres

import (
	"database/sql"
	"fmt"
	"sync"
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

// testAPITokenLimit is a generous cap used by tests that aren't exercising limit enforcement
// itself, so an ordinary sequence of Creates never trips it.
const testAPITokenLimit = 1000

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
func (s *APITokenRepositoryTestSuite) createUser(suffix string) uuid.UUID {
	s.T().Helper()

	id := uuid.New()
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

func (s *APITokenRepositoryTestSuite) newToken(userID uuid.UUID, hash string) *domain.APIToken {
	return &domain.APIToken{
		UserID:    userID,
		TokenHash: hash,
		CreatedAt: time.Now().Truncate(time.Second),
	}
}

// create is a test helper wrapping CreateIfUnderLimit with the generous testAPITokenLimit and
// asserting the token was actually created — for tests that only care about the round trip, not
// limit enforcement itself.
func (s *APITokenRepositoryTestSuite) create(tok *domain.APIToken) {
	s.T().Helper()

	created, err := s.repo.CreateIfUnderLimit(s.T().Context(), tok, testAPITokenLimit)
	s.Require().NoError(err)
	s.Require().True(created)
}

// ── Create / GetByHash ──

func (s *APITokenRepositoryTestSuite) TestCreate_GetByHash_RoundTrip() {
	ctx := s.T().Context()
	userID := s.createUser("u1")

	tok := s.newToken(userID, "hash-1")
	s.create(tok)

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
	s.create(tok)

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
	s.create(middle)
	s.create(oldest)
	s.create(newest)

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

// ── CreateIfUnderLimit ──

func (s *APITokenRepositoryTestSuite) TestCreateIfUnderLimit_CountsOnlyThatUsersTokens() {
	ctx := s.T().Context()
	userA := s.createUser("count-a")
	userB := s.createUser("count-b")

	s.create(s.newToken(userA, "count-a-1"))

	// userB's token must not count against userA's limit.
	s.create(s.newToken(userB, "count-b-1"))

	created, err := s.repo.CreateIfUnderLimit(ctx, s.newToken(userA, "count-a-2"), 2)
	s.Require().NoError(err)
	s.True(created, "userA has 1 token, limit 2 — must succeed")

	created, err = s.repo.CreateIfUnderLimit(ctx, s.newToken(userA, "count-a-3"), 2)
	s.Require().NoError(err)
	s.False(created, "userA now has 2 tokens, limit 2 — must be refused")

	count, err := s.db.NewSelect().TableExpr("api_tokens").Where("user_id = ?", userA).Count(ctx)
	s.Require().NoError(err)
	s.Equal(2, count, "the refused call must not have inserted a row")
}

func (s *APITokenRepositoryTestSuite) TestCreateIfUnderLimit_AtLimit_Refuses() {
	ctx := s.T().Context()
	userID := s.createUser("at-limit")

	s.create(s.newToken(userID, "at-limit-1"))

	created, err := s.repo.CreateIfUnderLimit(ctx, s.newToken(userID, "at-limit-2"), 1)
	s.Require().NoError(err)
	s.False(created)

	_, err = s.repo.GetByHash(ctx, "at-limit-2")
	s.ErrorIs(err, domain.ErrNotFound, "a refused create must not persist a row")
}

// TestCreateIfUnderLimit_ConcurrentRace_NeverExceedsLimit is the regression test for the fix:
// firing more concurrent CreateIfUnderLimit calls than the limit allows must still leave exactly
// `limit` rows stored, never more. Without the transaction-scoped advisory lock in
// CreateIfUnderLimit, PostgreSQL's default READ COMMITTED isolation lets concurrent transactions
// each read the same pre-insert count and all pass the limit check.
func (s *APITokenRepositoryTestSuite) TestCreateIfUnderLimit_ConcurrentRace_NeverExceedsLimit() {
	ctx := s.T().Context()
	owner := s.createUser("race-owner")

	const (
		limit    = 5
		attempts = 20
	)

	created := make([]bool, attempts)
	errs := make([]error, attempts)

	var wg sync.WaitGroup

	for i := range attempts {
		wg.Go(func() {
			created[i], errs[i] = s.repo.CreateIfUnderLimit(ctx, s.newToken(owner, fmt.Sprintf("race-%d", i)), limit)
		})
	}

	wg.Wait()

	successCount := 0

	for i, err := range errs {
		s.Require().NoError(err)

		if created[i] {
			successCount++
		}
	}

	s.Equal(limit, successCount, "exactly `limit` concurrent calls must succeed, no more")

	stored, err := s.db.NewSelect().TableExpr("api_tokens").Where("user_id = ?", owner).Count(ctx)
	s.Require().NoError(err)
	s.Equal(limit, stored, "the stored row count must never exceed the limit under concurrency")
}

// ── RevokeByHash ──

func (s *APITokenRepositoryTestSuite) TestRevokeByHash_RemovesToken() {
	ctx := s.T().Context()
	userID := s.createUser("u4")

	tok := s.newToken(userID, "hash-del")
	s.create(tok)
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
	s.create(tok)

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

	s.create(s.newToken(userID, "hash-cascade"))
	s.Require().NoError(s.users.Revoke(ctx, userID))

	count, err := s.db.NewSelect().
		TableExpr("api_tokens").
		Where("user_id = ?", userID).
		Count(ctx)
	s.Require().NoError(err)
	s.Zero(count, "api tokens must cascade-delete with the owning user")
}
