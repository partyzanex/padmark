package sqlite

import (
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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

// TestUpdateTOTPSecret_CompareAndSet covers audit finding R6: the reseal writes only while the
// stored seal equals expected, so a reseal from before a password change writes nothing.
func (s *UserRepositoryTestSuite) TestUpdateTOTPSecret_CompareAndSet() {
	ctx := s.T().Context()
	usr := s.newUser("reseal")
	s.Require().NoError(s.repo.Create(ctx, usr))

	s.Require().NoError(s.repo.UpdatePassword(ctx, usr.ID, "new-hash", []byte("new-salt"), "new-seal"))

	updated, err := s.repo.UpdateTOTPSecret(ctx, usr.ID, usr.TOTPSecret, "stale-reseal")
	s.Require().NoError(err)
	s.False(updated)

	updated, err = s.repo.UpdateTOTPSecret(ctx, usr.ID, "new-seal", "fresh-reseal")
	s.Require().NoError(err)
	s.True(updated)

	got, err := s.repo.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal("fresh-reseal", got.TOTPSecret)
	s.Equal("new-hash", got.PasswordHash)
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

func (s *UserRepositoryTestSuite) TestRevoke_NonExistentUser_ReturnsErrForbidden() {
	err := s.repo.Revoke(s.T().Context(), uuid.New())
	s.ErrorIs(err, domain.ErrForbidden)
}

// TestRevoke_CascadesSessionsAndInvites verifies that with foreign_keys enabled (as in
// production — Open applies it via WithPragmas), deleting a user also removes their sessions
// and the invites they issued. Without ON DELETE CASCADE this DELETE would fail (PostgreSQL) or
// leave orphan rows (SQLite). Uses its own dedicated DSN (distinct from the suite's shared one)
// purely for isolation, not to opt into FK enforcement — that's on by default for every Open
// call now.
func TestRevoke_CascadesSessionsAndInvites(t *testing.T) {
	ctx := t.Context()

	db, err := Open("file:cascade_fk?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	defer func() { require.NoError(t, db.Close()) }()

	require.NoError(t, db.Ping())

	_, err = Migrate(ctx, db)
	require.NoError(t, err)

	users := NewUserRepository(db)
	sessions := NewSessionRepository(db, slog.Default())
	invites := NewInviteRepository(db, slog.Default())

	usr := &domain.User{
		ID: uuid.New(), Username: "usr", TOTPSecret: "s", PasswordHash: "h",
		KDFSalt: []byte("saltsaltsalt1234"), IsAdmin: false, CreatedAt: time.Now().Truncate(time.Second),

		LastLoginAt: nil, LastTOTPCounter: 0,
	}
	require.NoError(t, users.Create(ctx, usr))

	require.NoError(t, sessions.Create(ctx, &domain.Session{
		SessionHash: "sess-1", UserID: usr.ID,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),

		UserAgent: "", IP: "",
	}))

	_, err = invites.Issue(ctx, usr.ID)
	require.NoError(t, err)

	require.NoError(t, users.Revoke(ctx, usr.ID))

	sessCount, err := db.NewSelect().TableExpr("sessions").Where("user_id = ?", usr.ID).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, sessCount, "sessions must cascade-delete with the user")

	invCount, err := db.NewSelect().TableExpr("invites").Where("created_by = ?", usr.ID).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, invCount, "invites must cascade-delete with the issuing user")
}

// ── RedeemInvite (atomic consume + insert) ──.

func (s *UserRepositoryTestSuite) TestRedeemInvite_UsernameCollision_RollsBackInvite() {
	ctx := s.T().Context()
	invites := NewInviteRepository(s.db, slog.Default())

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

// ── UpdateTOTPCounter ──.

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
