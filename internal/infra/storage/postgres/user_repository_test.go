//go:build integration

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

type UserRepositoryTestSuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
	repo      *UserRepository
}

func (s *UserRepositoryTestSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_user_test"),
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

	s.repo = NewUserRepository(s.db)
}

func (s *UserRepositoryTestSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *UserRepositoryTestSuite) SetupTest() {
	ctx := s.T().Context()

	_, err := s.db.NewTruncateTable().
		TableExpr("sessions").
		Cascade().
		Exec(ctx)
	s.Require().NoError(err)

	_, err = s.db.NewTruncateTable().
		TableExpr("users").
		Cascade().
		Exec(ctx)
	s.Require().NoError(err)
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

func (s *UserRepositoryTestSuite) TestCreate_GetByUsername_RoundTrip() {
	ctx := s.T().Context()
	usr := s.newUser("alice")

	s.Require().NoError(s.repo.Create(ctx, usr))

	got, err := s.repo.GetByUsername(ctx, "alice")
	s.Require().NoError(err)
	s.Equal(usr.ID, got.ID)
	s.Equal(usr.Username, got.Username)
	s.Equal(usr.TOTPSecret, got.TOTPSecret)
	s.Equal(usr.PasswordHash, got.PasswordHash)
	s.Equal(usr.KDFSalt, got.KDFSalt)
	s.Equal(usr.IsAdmin, got.IsAdmin)
	s.Nil(got.LastLoginAt)
}

func (s *UserRepositoryTestSuite) TestCreate_DuplicateUsername_ReturnsErrUserExists() {
	ctx := s.T().Context()

	s.Require().NoError(s.repo.Create(ctx, s.newUser("bob")))

	err := s.repo.Create(ctx, s.newUser("bob"))
	s.ErrorIs(err, domain.ErrUserExists)
}

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

func (s *UserRepositoryTestSuite) TestGetByUsername_NotFound_ReturnsErrNotFound() {
	_, err := s.repo.GetByUsername(s.T().Context(), "nobody")
	s.ErrorIs(err, domain.ErrNotFound)
}

// ── GetByID ──.

func (s *UserRepositoryTestSuite) TestGetByID_Found() {
	ctx := s.T().Context()
	usr := s.newUser("carol")
	s.Require().NoError(s.repo.Create(ctx, usr))

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal(usr.ID, got.ID)
	s.Equal(usr.Username, got.Username)
}

func (s *UserRepositoryTestSuite) TestGetByID_NotFound_ReturnsErrNotFound() {
	_, err := s.repo.GetByID(s.T().Context(), uuid.New())
	s.ErrorIs(err, domain.ErrNotFound)
}

// ── List ──.

func (s *UserRepositoryTestSuite) TestList_Empty_ReturnsEmptySlice() {
	users, err := s.repo.List(s.T().Context())
	s.Require().NoError(err)
	s.Empty(users)
}

func (s *UserRepositoryTestSuite) TestList_OrderedByCreatedAt() {
	ctx := s.T().Context()

	first := s.newUser("dave")
	first.CreatedAt = time.Now().Add(-time.Hour).Truncate(time.Second)

	second := s.newUser("eve")
	second.CreatedAt = time.Now().Truncate(time.Second)

	s.Require().NoError(s.repo.Create(ctx, second))
	s.Require().NoError(s.repo.Create(ctx, first))

	users, err := s.repo.List(ctx)
	s.Require().NoError(err)
	s.Require().Len(users, 2)
	s.Equal("dave", users[0].Username)
	s.Equal("eve", users[1].Username)
}

// ── UpdateLastLogin ──.

func (s *UserRepositoryTestSuite) TestUpdateLastLogin_SetsField() {
	ctx := s.T().Context()
	usr := s.newUser("frank")
	s.Require().NoError(s.repo.Create(ctx, usr))

	loginAt := time.Now().Truncate(time.Second)
	s.Require().NoError(s.repo.UpdateLastLogin(ctx, usr.ID, loginAt))

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.LastLoginAt)
	s.Equal(loginAt.Unix(), got.LastLoginAt.Unix())
}

func (s *UserRepositoryTestSuite) TestUpdateLastLogin_NonExistentUser_NoError() {
	err := s.repo.UpdateLastLogin(s.T().Context(), uuid.New(), time.Now())
	s.Require().NoError(err)
}

// ── Revoke ──.

func (s *UserRepositoryTestSuite) TestRevoke_RemovesUser() {
	ctx := s.T().Context()
	usr := s.newUser("grace")
	s.Require().NoError(s.repo.Create(ctx, usr))

	s.Require().NoError(s.repo.Revoke(ctx, usr.ID))

	_, err := s.repo.GetByID(ctx, usr.ID)
	s.ErrorIs(err, domain.ErrNotFound)
}

// TestRevoke_LastAdminRefused verifies the atomic last-admin guard: the advisory-locked
// conditional delete must refuse to remove the only admin with domain.ErrForbidden and leave
// the user in place — two concurrent revocations would otherwise leave the server permanently
// admin-less.
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

func (s *UserRepositoryTestSuite) TestRevoke_NonExistentUser_ReturnsErrForbidden() {
	err := s.repo.Revoke(s.T().Context(), uuid.New())
	s.ErrorIs(err, domain.ErrForbidden)
}

// ── UpdatePassword ──.

func (s *UserRepositoryTestSuite) TestUpdatePassword_UpdatesAllFields() {
	ctx := s.T().Context()
	usr := s.newUser("heidi")
	s.Require().NoError(s.repo.Create(ctx, usr))

	newHash := "new-password-hash"
	newSalt := []byte("newsaltnewsalt16")
	newTOTP := "new-totp-secret"

	s.Require().NoError(s.repo.UpdatePassword(ctx, usr.ID, newHash, newSalt, newTOTP))

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal(newHash, got.PasswordHash)
	s.Equal(newSalt, got.KDFSalt)
	s.Equal(newTOTP, got.TOTPSecret)
	// Other fields must not change.
	s.Equal(usr.Username, got.Username)
	s.Equal(usr.IsAdmin, got.IsAdmin)
}

func (s *UserRepositoryTestSuite) TestUpdatePassword_KDFSalt_Base64RoundTrip() {
	ctx := s.T().Context()
	usr := s.newUser("ivan")
	s.Require().NoError(s.repo.Create(ctx, usr))

	// Use bytes with all values 0–255 to exercise base64 encoding.
	salt := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(i)
	}

	s.Require().NoError(s.repo.UpdatePassword(ctx, usr.ID, "h", salt, "t"))

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal(salt, got.KDFSalt)
}

func (s *UserRepositoryTestSuite) TestUpdatePassword_NonExistentUser_NoError() {
	err := s.repo.UpdatePassword(s.T().Context(), uuid.New(), "h", []byte("s"), "t")
	s.Require().NoError(err)
}

// ── ChangePassword ──.

// changePasswordFixture stores alice with two sessions and one API token, and bob with one
// session and one API token. It returns alice and the new session that ChangePassword stores for her.
func (s *UserRepositoryTestSuite) changePasswordFixture() (*domain.User, *domain.Session) {
	ctx := s.T().Context()
	alice := s.newUser("alice")
	bob := s.newUser("bob")
	s.Require().NoError(s.repo.Create(ctx, alice))
	s.Require().NoError(s.repo.Create(ctx, bob))

	sessions := NewSessionRepository(s.db, slog.New(slog.DiscardHandler))
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

func (s *UserRepositoryTestSuite) TestChangePassword_ReplacesCredentialsAndAccess() {
	ctx := s.T().Context()
	alice, newSess := s.changePasswordFixture()
	sessions := NewSessionRepository(s.db, slog.New(slog.DiscardHandler))
	tokens := NewAPITokenRepository(s.db)
	newSalt := []byte("newsaltnewsalt16")

	s.Require().NoError(s.repo.ChangePassword(ctx, newSess, "new-hash", newSalt, "new-seal"))

	got, err := s.repo.GetByID(ctx, alice.ID)
	s.Require().NoError(err)
	s.Equal("new-hash", got.PasswordHash)
	s.Equal(newSalt, got.KDFSalt)
	s.Equal("new-seal", got.TOTPSecret)

	_, err = sessions.Get(ctx, "alice-new")
	s.Require().NoError(err, "the new session must exist")

	for _, old := range []string{"alice-old-1", "alice-old-2"} {
		_, err = sessions.Get(ctx, old)
		s.Require().ErrorIs(err, domain.ErrSessionExpired, "old session %s must be revoked", old)
	}

	_, err = tokens.GetByHash(ctx, "alice-token")
	s.Require().ErrorIs(err, domain.ErrNotFound, "the API token of alice must be revoked")

	_, err = sessions.Get(ctx, "bob-session")
	s.Require().NoError(err, "the session of another user must stay")
	_, err = tokens.GetByHash(ctx, "bob-token")
	s.Require().NoError(err, "the API token of another user must stay")
}

// TestChangePassword_RevocationFailure_RollsBackAllWrites covers Finding 5: a failure in the last
// step (API-token revocation) must leave the old password, old sessions and API tokens valid,
// and must not keep the new session.
func (s *UserRepositoryTestSuite) TestChangePassword_RevocationFailure_RollsBackAllWrites() {
	ctx := s.T().Context()
	alice, newSess := s.changePasswordFixture()
	sessions := NewSessionRepository(s.db, slog.New(slog.DiscardHandler))
	tokens := NewAPITokenRepository(s.db)

	_, err := s.db.ExecContext(ctx, `CREATE OR REPLACE FUNCTION fail_api_token_delete() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected revocation failure'; END $$`)
	s.Require().NoError(err)
	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_api_token_delete BEFORE DELETE ON api_tokens
		FOR EACH ROW EXECUTE FUNCTION fail_api_token_delete()`)
	s.Require().NoError(err)

	// The suite shares one database: drop the trigger so other tests can delete API tokens.
	s.T().Cleanup(func() {
		_, dropErr := s.db.ExecContext(context.Background(), "DROP TRIGGER fail_api_token_delete ON api_tokens")
		s.NoError(dropErr)
	})

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

func (s *UserRepositoryTestSuite) TestChangePassword_NonExistentUser_ReturnsErrNotFound() {
	ctx := s.T().Context()
	sessions := NewSessionRepository(s.db, slog.New(slog.DiscardHandler))
	newSess := cpSession("ghost-session", uuid.New())

	err := s.repo.ChangePassword(ctx, newSess, "h", []byte("s"), "t")
	s.Require().ErrorIs(err, domain.ErrNotFound)

	_, err = sessions.Get(ctx, "ghost-session")
	s.Require().ErrorIs(err, domain.ErrSessionExpired)
}

// ── Case-insensitive usernames ──.

// TestCreate_DuplicateUsernameDifferentCase_ReturnsErrUserExists pins that "Admin" cannot be
// registered next to "admin": otherwise two accounts would differ only by case, which is an
// impersonation/phishing aid and splits one person's lockout bucket from their account.
func (s *UserRepositoryTestSuite) TestCreate_DuplicateUsernameDifferentCase_ReturnsErrUserExists() {
	ctx := s.T().Context()

	s.Require().NoError(s.repo.Create(ctx, s.newUser("admin")))

	err := s.repo.Create(ctx, s.newUser("Admin"))
	s.ErrorIs(err, domain.ErrUserExists)
}

// TestGetByUsername_IsCaseInsensitive verifies the lookup finds the stored row whatever case the
// login form used, and returns the username as it was registered.
func (s *UserRepositoryTestSuite) TestGetByUsername_IsCaseInsensitive() {
	ctx := s.T().Context()

	stored := s.newUser("Alice")
	s.Require().NoError(s.repo.Create(ctx, stored))

	for _, query := range []string{"Alice", "alice", "ALICE"} {
		got, err := s.repo.GetByUsername(ctx, query)
		s.Require().NoError(err, query)
		s.Equal(stored.ID, got.ID, query)
		s.Equal("Alice", got.Username, "the stored spelling is preserved")
	}

	_, err := s.repo.GetByUsername(ctx, "alic")
	s.ErrorIs(err, domain.ErrNotFound)
}

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
