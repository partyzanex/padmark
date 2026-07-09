package sqlite

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	"github.com/partyzanex/padmark/internal/domain"
)

type APITokenRepositoryTestSuite struct {
	suite.Suite

	db    *bun.DB
	repo  *APITokenRepository
	users *UserRepository
}

func (s *APITokenRepositoryTestSuite) SetupTest() {
	sqldb, err := sql.Open(sqliteshim.DriverName(), "file::memory:?cache=shared&_busy_timeout=5000")
	s.Require().NoError(err)

	s.db = bun.NewDB(sqldb, sqlitedialect.New())
	s.Require().NoError(s.db.Ping())

	_, err = Migrate(s.T().Context(), s.db)
	s.Require().NoError(err)

	s.repo = NewAPITokenRepository(s.db)
	s.users = NewUserRepository(s.db)
}

func (s *APITokenRepositoryTestSuite) TearDownTest() {
	s.Require().NoError(s.db.Close())
}

func TestAPITokenRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(APITokenRepositoryTestSuite))
}

func (s *APITokenRepositoryTestSuite) createUser(id string) {
	s.T().Helper()

	err := s.users.Create(s.T().Context(), &domain.User{
		ID:           id,
		Username:     "user-" + id,
		TOTPSecret:   "secret",
		PasswordHash: "hash",
		KDFSalt:      []byte("saltsaltsalt1234"),
		CreatedAt:    time.Now().Truncate(time.Second),
	})
	s.Require().NoError(err)
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
	s.createUser("u1")

	tok := s.newToken("u1", "hash-1")
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
	s.createUser("u2")

	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	tok := s.newToken("u2", "hash-exp")
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
	s.createUser("u3")

	now := time.Now().Truncate(time.Second)

	newest := s.newToken("u3", "h-new")
	newest.CreatedAt = now

	middle := s.newToken("u3", "h-mid")
	middle.CreatedAt = now.Add(-time.Hour)

	oldest := s.newToken("u3", "h-old")
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
	s.createUser("count-a")
	s.createUser("count-b")

	s.Require().NoError(s.repo.Create(ctx, s.newToken("count-a", "count-a-1")))
	s.Require().NoError(s.repo.Create(ctx, s.newToken("count-a", "count-a-2")))
	s.Require().NoError(s.repo.Create(ctx, s.newToken("count-b", "count-b-1")))

	count, err := s.repo.CountByUser(ctx, "count-a")
	s.Require().NoError(err)
	s.Equal(2, count)
}

func (s *APITokenRepositoryTestSuite) TestCountByUser_NoTokens_ReturnsZero() {
	s.createUser("count-none")

	count, err := s.repo.CountByUser(s.T().Context(), "count-none")
	s.Require().NoError(err)
	s.Equal(0, count)
}

// ── RevokeByHash ──

func (s *APITokenRepositoryTestSuite) TestRevokeByHash_RemovesToken() {
	ctx := s.T().Context()
	s.createUser("u4")

	tok := s.newToken("u4", "hash-del")
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
	s.createUser("u5")

	tok := s.newToken("u5", "hash-lu")
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

// TestCreate_CascadesWithUser verifies that with foreign_keys enabled (as in
// production), deleting a user also removes their API tokens via ON DELETE CASCADE.
// Uses a dedicated FK-enabled connection because the suite's shared DSN does not
// set the pragma.
func TestCreate_CascadesWithUser(t *testing.T) {
	ctx := t.Context()

	sqldb, err := sql.Open(sqliteshim.DriverName(),
		"file:apitoken_cascade_fk?mode=memory&cache=shared&_busy_timeout=5000&_pragma=foreign_keys(1)")
	require.NoError(t, err)

	defer func() { require.NoError(t, sqldb.Close()) }()

	db := bun.NewDB(sqldb, sqlitedialect.New())
	require.NoError(t, db.Ping())

	_, err = Migrate(ctx, db)
	require.NoError(t, err)

	users := NewUserRepository(db)
	tokens := NewAPITokenRepository(db)

	usr := &domain.User{
		ID: "owner-id", Username: "owner", TOTPSecret: "s", PasswordHash: "h",
		KDFSalt: []byte("saltsaltsalt1234"), CreatedAt: time.Now().Truncate(time.Second),
	}
	require.NoError(t, users.Create(ctx, usr))
	require.NoError(t, tokens.Create(ctx, &domain.APIToken{
		UserID: usr.ID, TokenHash: "hash-cascade", CreatedAt: time.Now().Truncate(time.Second),
	}))

	require.NoError(t, users.Revoke(ctx, usr.ID))

	count, err := db.NewSelect().TableExpr("api_tokens").Where("user_id = ?", usr.ID).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count, "api tokens must cascade-delete with the owning user")
}
