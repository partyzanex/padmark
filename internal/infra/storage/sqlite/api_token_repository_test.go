package sqlite

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	"github.com/partyzanex/padmark/internal/domain"
)

// testAPITokenLimit is a generous cap used by tests that aren't exercising limit enforcement
// itself, so an ordinary sequence of Creates never trips it.
const testAPITokenLimit = 1000

type APITokenRepositoryTestSuite struct {
	suite.Suite

	db    *bun.DB
	repo  *APITokenRepository
	users *UserRepository
}

func (s *APITokenRepositoryTestSuite) SetupTest() {
	sqldb, err := sql.Open(sqliteshim.DriverName(), "file::memory:?cache=shared&_busy_timeout=5000")
	s.Require().NoError(err)

	// Shared-cache in-memory SQLite promotes a read lock to a write lock immediately when a
	// concurrent connection also holds a read lock, reporting SQLITE_LOCKED ("database is
	// deadlocked") instead of waiting out busy_timeout like ordinary file-backed SQLite does.
	// A single pooled connection sidesteps that shared-cache-specific artifact entirely, which
	// is what TestCreateIfUnderLimit_ConcurrentRace_NeverExceedsLimit needs to actually exercise
	// concurrent callers against this test DB. Production doesn't use cache=shared, so this is a
	// test-only accommodation, not a production behavior change.
	sqldb.SetMaxOpenConns(1)

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

func (s *APITokenRepositoryTestSuite) createUser(id uuid.UUID) {
	s.T().Helper()

	err := s.users.Create(s.T().Context(), &domain.User{
		ID:           id,
		Username:     "user-" + id.String(),
		TOTPSecret:   "secret",
		PasswordHash: "hash",
		KDFSalt:      []byte("saltsaltsalt1234"),
		CreatedAt:    time.Now().Truncate(time.Second),
	})
	s.Require().NoError(err)
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
	u1 := uuid.New()
	s.createUser(u1)

	tok := s.newToken(u1, "hash-1")
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
	u2 := uuid.New()
	s.createUser(u2)

	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	tok := s.newToken(u2, "hash-exp")
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
	tokenOwner := uuid.New()
	s.createUser(tokenOwner)

	now := time.Now().Truncate(time.Second)

	newest := s.newToken(tokenOwner, "h-new")
	newest.CreatedAt = now

	middle := s.newToken(tokenOwner, "h-mid")
	middle.CreatedAt = now.Add(-time.Hour)

	oldest := s.newToken(tokenOwner, "h-old")
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
	countA := uuid.New()
	countB := uuid.New()

	s.createUser(countA)
	s.createUser(countB)

	s.create(s.newToken(countA, "count-a-1"))

	// countB's token must not count against countA's limit.
	s.create(s.newToken(countB, "count-b-1"))

	created, err := s.repo.CreateIfUnderLimit(ctx, s.newToken(countA, "count-a-2"), 2)
	s.Require().NoError(err)
	s.True(created, "countA has 1 token, limit 2 — must succeed")

	created, err = s.repo.CreateIfUnderLimit(ctx, s.newToken(countA, "count-a-3"), 2)
	s.Require().NoError(err)
	s.False(created, "countA now has 2 tokens, limit 2 — must be refused")

	count, err := s.db.NewSelect().Model((*apiTokenRow)(nil)).Where("user_id = ?", countA).Count(ctx)
	s.Require().NoError(err)
	s.Equal(2, count, "the refused call must not have inserted a row")
}

func (s *APITokenRepositoryTestSuite) TestCreateIfUnderLimit_AtLimit_Refuses() {
	ctx := s.T().Context()
	owner := uuid.New()
	s.createUser(owner)

	s.create(s.newToken(owner, "at-limit-1"))

	created, err := s.repo.CreateIfUnderLimit(ctx, s.newToken(owner, "at-limit-2"), 1)
	s.Require().NoError(err)
	s.False(created)

	_, err = s.repo.GetByHash(ctx, "at-limit-2")
	s.ErrorIs(err, domain.ErrNotFound, "a refused create must not persist a row")
}

// TestCreateIfUnderLimit_ConcurrentRace_NeverExceedsLimit is the regression test for the fix:
// firing more concurrent CreateIfUnderLimit calls than the limit allows must still leave exactly
// `limit` rows stored, never more — proving the count-then-insert is race-free under concurrency,
// not just correct when called sequentially.
func (s *APITokenRepositoryTestSuite) TestCreateIfUnderLimit_ConcurrentRace_NeverExceedsLimit() {
	ctx := s.T().Context()
	owner := uuid.New()
	s.createUser(owner)

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

	stored, err := s.db.NewSelect().Model((*apiTokenRow)(nil)).Where("user_id = ?", owner).Count(ctx)
	s.Require().NoError(err)
	s.Equal(limit, stored, "the stored row count must never exceed the limit under concurrency")
}

// ── RevokeByHash ──

func (s *APITokenRepositoryTestSuite) TestRevokeByHash_RemovesToken() {
	ctx := s.T().Context()
	u4 := uuid.New()
	s.createUser(u4)

	tok := s.newToken(u4, "hash-del")
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
	u5 := uuid.New()
	s.createUser(u5)

	tok := s.newToken(u5, "hash-lu")
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
		ID: uuid.New(), Username: "owner", TOTPSecret: "s", PasswordHash: "h",
		KDFSalt: []byte("saltsaltsalt1234"), CreatedAt: time.Now().Truncate(time.Second),
	}
	require.NoError(t, users.Create(ctx, usr))

	created, err := tokens.CreateIfUnderLimit(ctx, &domain.APIToken{
		UserID: usr.ID, TokenHash: "hash-cascade", CreatedAt: time.Now().Truncate(time.Second),
	}, testAPITokenLimit)
	require.NoError(t, err)
	require.True(t, created)

	require.NoError(t, users.Revoke(ctx, usr.ID))

	count, err := db.NewSelect().TableExpr("api_tokens").Where("user_id = ?", usr.ID).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count, "api tokens must cascade-delete with the owning user")
}
