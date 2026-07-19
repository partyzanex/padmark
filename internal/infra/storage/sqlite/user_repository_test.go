package sqlite

import (
	"database/sql"
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

type UserRepositoryTestSuite struct {
	suite.Suite

	db   *bun.DB
	repo *UserRepository
}

func (s *UserRepositoryTestSuite) SetupTest() {
	sqldb, err := sql.Open(sqliteshim.DriverName(), "file::memory:?cache=shared&_busy_timeout=5000")
	s.Require().NoError(err)

	s.db = bun.NewDB(sqldb, sqlitedialect.New())
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
	}
}

// ── Create ──

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

func (s *UserRepositoryTestSuite) TestCreate_SecondAdmin_RejectedByPartialUniqueIndex() {
	ctx := s.T().Context()

	first := s.newUser("admin1")
	first.IsAdmin = true
	s.Require().NoError(s.repo.Create(ctx, first))

	// A second admin (different username) must be rejected by idx_users_single_admin —
	// this is the atomic guard against the first-admin bootstrap race.
	second := s.newUser("admin2")
	second.IsAdmin = true
	s.Require().ErrorIs(s.repo.Create(ctx, second), domain.ErrUserExists)

	// Non-admins are unaffected.
	s.Require().NoError(s.repo.Create(ctx, s.newUser("regular")))
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

// ── GetByUsername ──

func (s *UserRepositoryTestSuite) TestGetByUsername_NotFound_ReturnsErrNotFound() {
	_, err := s.repo.GetByUsername(s.T().Context(), "nobody")
	s.ErrorIs(err, domain.ErrNotFound)
}

// ── GetByID ──

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

// ── List ──

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

// ── UpdateLastLogin ──

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

// ── Revoke ──

func (s *UserRepositoryTestSuite) TestRevoke_RemovesUser() {
	ctx := s.T().Context()
	usr := s.newUser("grace")
	s.Require().NoError(s.repo.Create(ctx, usr))

	s.Require().NoError(s.repo.Revoke(ctx, usr.ID))

	_, err := s.repo.GetByID(ctx, usr.ID)
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *UserRepositoryTestSuite) TestRevoke_NonExistentUser_NoError() {
	err := s.repo.Revoke(s.T().Context(), uuid.New())
	s.Require().NoError(err)
}

// TestRevoke_CascadesSessionsAndInvites verifies that with foreign_keys enabled
// (as in production), deleting a user also removes their sessions and the invites
// they issued. Without ON DELETE CASCADE this DELETE would fail (PostgreSQL) or
// leave orphan rows (SQLite). Uses a dedicated FK-enabled connection because the
// suite's shared DSN does not set the pragma.
func TestRevoke_CascadesSessionsAndInvites(t *testing.T) {
	ctx := t.Context()

	sqldb, err := sql.Open(sqliteshim.DriverName(),
		"file:cascade_fk?mode=memory&cache=shared&_busy_timeout=5000&_pragma=foreign_keys(1)")
	require.NoError(t, err)

	defer func() { require.NoError(t, sqldb.Close()) }()

	db := bun.NewDB(sqldb, sqlitedialect.New())
	require.NoError(t, db.Ping())

	_, err = Migrate(ctx, db)
	require.NoError(t, err)

	users := NewUserRepository(db)
	sessions := NewSessionRepository(db)
	invites := NewInviteRepository(db)

	admin := &domain.User{
		ID: uuid.New(), Username: "admin", TOTPSecret: "s", PasswordHash: "h",
		KDFSalt: []byte("saltsaltsalt1234"), IsAdmin: true, CreatedAt: time.Now().Truncate(time.Second),
	}
	require.NoError(t, users.Create(ctx, admin))

	require.NoError(t, sessions.Create(ctx, &domain.Session{
		SessionID: "sess-1", UserID: admin.ID,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))

	_, err = invites.Issue(ctx, admin.ID)
	require.NoError(t, err)

	require.NoError(t, users.Revoke(ctx, admin.ID))

	sessCount, err := db.NewSelect().TableExpr("sessions").Where("user_id = ?", admin.ID).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, sessCount, "sessions must cascade-delete with the user")

	invCount, err := db.NewSelect().TableExpr("invites").Where("created_by = ?", admin.ID).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, invCount, "invites must cascade-delete with the issuing user")
}

// ── RedeemInvite (atomic consume + insert) ──

func (s *UserRepositoryTestSuite) TestRedeemInvite_UsernameCollision_RollsBackInvite() {
	ctx := s.T().Context()
	invites := NewInviteRepository(s.db)

	creator := s.newUser("creator")
	s.Require().NoError(s.repo.Create(ctx, creator))
	taken := s.newUser("taken")
	s.Require().NoError(s.repo.Create(ctx, taken))

	tok, err := invites.Issue(ctx, creator.ID)
	s.Require().NoError(err)

	// Redeeming for an already-taken username hits the unique constraint inside the
	// transaction → the whole tx rolls back with ErrUserExists.
	dup := s.newUser("taken")
	dup.ID = uuid.New()
	s.Require().ErrorIs(invites.RedeemInvite(ctx, tok, "taken", dup), domain.ErrUserExists)

	// The invite must NOT have been burned: a retry with a free username succeeds.
	fresh := s.newUser("fresh")
	s.Require().NoError(invites.RedeemInvite(ctx, tok, "fresh", fresh))

	got, err := s.repo.GetByID(ctx, fresh.ID)
	s.Require().NoError(err)
	s.Equal("fresh", got.Username)
}

// ── UpdateTOTPCounter ──

func (s *UserRepositoryTestSuite) TestUpdateTOTPCounter_PersistsAndBlocksReplay() {
	ctx := s.T().Context()
	usr := s.newUser("totp")
	s.Require().NoError(s.repo.Create(ctx, usr))

	// First accepted code advances the counter.
	accepted, err := s.repo.UpdateTOTPCounter(ctx, usr.ID, 100)
	s.Require().NoError(err)
	s.True(accepted)

	// Same counter (replay) and an older one are both rejected.
	for _, c := range []int64{100, 99} {
		accepted, err = s.repo.UpdateTOTPCounter(ctx, usr.ID, c)
		s.Require().NoError(err)
		s.False(accepted, "counter %d must be rejected", c)
	}

	// A strictly newer counter is accepted and persisted.
	accepted, err = s.repo.UpdateTOTPCounter(ctx, usr.ID, 101)
	s.Require().NoError(err)
	s.True(accepted)

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal(int64(101), got.LastTOTPCounter)
}

// ── UpdatePassword ──

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
