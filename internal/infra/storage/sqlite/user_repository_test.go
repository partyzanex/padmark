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

type UserRepositoryTestSuite struct {
	suite.Suite

	db   *bun.DB
	repo *UserRepository
}

func (s *UserRepositoryTestSuite) SetupTest() {
	db, err := Open("file::memory:?cache=shared&_busy_timeout=5000", slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	s.db = db
	s.Require().NoError(s.db.Ping())

	_, err = Migrate(s.T().Context(), s.db)
	s.Require().NoError(err)

	s.repo = NewUserRepository(s.db)
}

func (s *UserRepositoryTestSuite) TearDownTest() {
	s.Require().NoError(s.db.Close())
}

func TestUserRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(UserRepositoryTestSuite))
}

func (s *UserRepositoryTestSuite) newUser(username string) *domain.User {
	return &domain.User{
		ID:           uuid.New(),
		Username:     username,
		TOTPSecret:   "totp-secret",
		PasswordHash: "hash",
		KDFSalt:      []byte("saltsaltsalt1234"),
		IsAdmin:      false,
		CreatedAt:    time.Now().Truncate(time.Second),

		LastLoginAt: nil, LastTOTPCounter: 0,
	}
}

// ── Create ──.

func (s *UserRepositoryTestSuite) TestCreate_IsAdmin_StoredCorrectly() {
	ctx := s.T().Context()
	usr := s.newUser("admin")
	usr.IsAdmin = true

	s.Require().NoError(s.repo.Create(ctx, usr))

	got, err := s.repo.GetByUsername(ctx, "admin")
	s.Require().NoError(err)
	s.True(got.IsAdmin)
}

// ── GetByUsername ──.

// ── GetByID ──.

// ── List ──.

func (s *UserRepositoryTestSuite) TestList_Empty_ReturnsEmptySlice() {
	users, err := s.repo.List(s.T().Context())
	s.Require().NoError(err)
	s.Empty(users)
}

// ── UpdateLastLogin ──.

// ── Revoke ──.

// TestRevoke_LastAdminRefused verifies the atomic last-admin guard: deleting the only admin
// must fail with domain.ErrForbidden and leave the user in place — otherwise two concurrent
// revocations could leave the server permanently admin-less.
func (s *UserRepositoryTestSuite) TestRevoke_LastAdminRefused() {
	ctx := s.T().Context()
	admin := s.newUser("admin")
	admin.IsAdmin = true
	s.Require().NoError(s.repo.Create(ctx, admin))

	err := s.repo.Revoke(ctx, admin.ID)

	s.Require().ErrorIs(err, domain.ErrForbidden)

	_, getErr := s.repo.GetByID(ctx, admin.ID)
	s.Require().NoError(getErr, "the last admin must survive a refused revocation")
}

// ── RedeemInvite (atomic consume + insert) ──.

// ── UpdateTOTPCounter ──.

// ── UpdatePassword ──.

// ── ChangePassword ──.

// changePasswordFixture stores alice with two sessions and one API token, and bob with one
// session and one API token. It returns alice and the new session that ChangePassword stores for her.
func (s *UserRepositoryTestSuite) changePasswordFixture() (*domain.User, *domain.Session) {
	ctx := s.T().Context()
	alice := s.newUser("alice")
	bob := s.newUser("bob")
	s.Require().NoError(s.repo.Create(ctx, alice))
	s.Require().NoError(s.repo.Create(ctx, bob))

	sessions := NewSessionRepository(s.db)
	tokens := NewAPITokenRepository(s.db)

	for _, fixture := range []struct {
		hash  string
		owner uuid.UUID
	}{{"alice-old-1", alice.ID}, {"alice-old-2", alice.ID}, {"bob-session", bob.ID}} {
		s.Require().NoError(sessions.Create(ctx, cpSession(fixture.hash, fixture.owner)))
	}

	for _, fixture := range []struct {
		hash  string
		owner uuid.UUID
	}{{"alice-token", alice.ID}, {"bob-token", bob.ID}} {
		created, err := tokens.CreateIfUnderLimit(ctx, cpToken(fixture.hash, fixture.owner), 10)
		s.Require().NoError(err)
		s.Require().True(created)
	}

	return alice, cpSession("alice-new", alice.ID)
}

// TestChangePassword_RevocationFailure_RollsBackAllWrites covers Finding 5: a failure in the last
// step (API-token revocation) must leave the old password, old sessions and API tokens valid,
// and must not keep the new session.
func (s *UserRepositoryTestSuite) TestChangePassword_RevocationFailure_RollsBackAllWrites() {
	ctx := s.T().Context()
	alice, newSess := s.changePasswordFixture()
	sessions := NewSessionRepository(s.db)
	tokens := NewAPITokenRepository(s.db)

	_, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_api_token_delete BEFORE DELETE ON api_tokens
		BEGIN SELECT RAISE(ABORT, 'injected revocation failure'); END`)
	s.Require().NoError(err)

	err = s.repo.ChangePassword(ctx, newSess, "new-hash", []byte("newsaltnewsalt16"), "new-seal")
	s.Require().ErrorContains(err, "injected revocation failure")

	got, err := s.repo.GetByID(ctx, alice.ID)
	s.Require().NoError(err)
	s.Equal(alice.PasswordHash, got.PasswordHash, "the old password must stay valid")
	s.Equal(alice.KDFSalt, got.KDFSalt)
	s.Equal(alice.TOTPSecret, got.TOTPSecret)

	_, err = sessions.Get(ctx, "alice-new")
	s.Require().ErrorIs(err, domain.ErrSessionExpired, "the new session must not exist")

	for _, old := range []string{"alice-old-1", "alice-old-2"} {
		_, err = sessions.Get(ctx, old)
		s.Require().NoError(err, "old session %s must stay after a rollback", old)
	}

	_, err = tokens.GetByHash(ctx, "alice-token")
	s.Require().NoError(err, "the API token must stay after a rollback")
}

// ── Case-insensitive usernames ──.

// --- helpers ---

// cpSession returns a live session with the given at-rest hash for owner.
func cpSession(hash string, owner uuid.UUID) *domain.Session {
	now := time.Now()

	return &domain.Session{
		SessionHash: hash, UserID: owner, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		UserAgent: "ua", IP: "127.0.0.1",
	}
}

// cpToken returns an API token without expiry with the given hash for owner.
func cpToken(hash string, owner uuid.UUID) *domain.APIToken {
	return &domain.APIToken{
		UserID: owner, TokenHash: hash, CreatedAt: time.Now(),
		ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
	}
}
