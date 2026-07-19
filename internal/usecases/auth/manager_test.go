package auth

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
)

var discardLog = slog.New(slog.DiscardHandler) //nolint:gochecknoglobals // test helper

// testArgon2Params uses minimal argon2id cost so these tests exercise real hash/verify logic
// without paying crypto.DefaultArgon2Params' production-strength (64 MiB) cost on every one of
// them; that cost is deliberate for brute-force resistance in production, not something these
// business-logic tests need to pay, and is covered on its own in internal/infra/crypto's tests.
//
//nolint:gochecknoglobals // test helper
var testArgon2Params = crypto.Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1}

const testPassword = "ValidP@ss12!"

type ManagerSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	users    *MockUserStore
	invites  *MockInviteStore
	sessions *MockSessionStore
	mgr      *Manager

	// adminID and userID are fixed per-test identities (fresh each SetupTest) reused by the
	// adminUser/testUser helpers and referenced directly by tests that need to assert the exact
	// ID a mock call was made with.
	adminID uuid.UUID
	userID  uuid.UUID
}

func (s *ManagerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.users = NewMockUserStore(s.ctrl)
	s.invites = NewMockInviteStore(s.ctrl)
	s.sessions = NewMockSessionStore(s.ctrl)
	mgr, err := NewManager(s.users, s.invites, s.sessions, nil, crypto.New(),
		crypto.NewPasswordHasher(testArgon2Params),
		crypto.NewKDF(), crypto.NewTOTP(), discardLog, "padmark", 0)
	s.Require().NoError(err)
	s.mgr = mgr
	s.adminID = uuid.New()
	s.userID = uuid.New()
}

func (s *ManagerSuite) TearDownTest() {
	s.ctrl.Finish()
}

// ── helpers ──

func (s *ManagerSuite) adminUser() *domain.User {
	return &domain.User{
		ID:       s.adminID,
		Username: "admin",
		IsAdmin:  true,
	}
}

// testUser builds a domain.User whose TOTP secret is encrypted with a key derived from testPassword.
// Returns the user and the raw (unencrypted) TOTP secret.
func (s *ManagerSuite) testUser() (*domain.User, string) {
	s.T().Helper()

	kdfSalt, err := crypto.GenerateKDFSalt()
	s.Require().NoError(err)

	derivedKey, err := crypto.DeriveUserKey([]byte(testPassword), kdfSalt)
	s.Require().NoError(err)

	rawSecret, err := crypto.GenerateTOTPSecret()
	s.Require().NoError(err)

	encSecret, err := crypto.New().Encrypt(rawSecret, derivedKey)
	s.Require().NoError(err)

	// verifyArgon2 reads its cost parameters from the stored hash itself, so hashing the
	// fixture with testArgon2Params (cheap) verifies identically to crypto.HashPassword's
	// production cost — just without paying it on every test.
	pwHash, err := crypto.NewPasswordHasher(testArgon2Params).Hash(testPassword)
	s.Require().NoError(err)

	usr := &domain.User{
		ID:           s.userID,
		Username:     "alice",
		TOTPSecret:   encSecret,
		PasswordHash: pwHash,
		KDFSalt:      kdfSalt,
	}

	return usr, rawSecret
}

// ── Login ──

func (s *ManagerSuite) TestLogin_InvalidUsername_ReturnsErrInvalidTOTP() {
	s.users.EXPECT().
		GetByUsername(gomock.Any(), "unknown").
		Return(nil, domain.ErrNotFound)

	_, err := s.mgr.Login(s.T().Context(), "unknown", testPassword, "123456", "", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

func (s *ManagerSuite) TestLogin_WrongPassword_ReturnsErrInvalidTOTP() {
	usr, _ := s.testUser()
	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)

	_, err := s.mgr.Login(s.T().Context(), "alice", "WrongP@ss12!", "000000", "", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

func (s *ManagerSuite) TestLogin_WrongTOTP_ReturnsErrInvalidTOTP() {
	usr, _ := s.testUser()
	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)

	_, err := s.mgr.Login(s.T().Context(), "alice", testPassword, "000000", "", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

func (s *ManagerSuite) TestLogin_ValidTOTP_CreatesSession() {
	usr, rawSecret := s.testUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), s.userID, gomock.Any()).Return(nil)

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1")
	s.Require().NoError(err)
	s.NotEmpty(sessID)
}

func (s *ManagerSuite) TestLogin_ReplayedTOTP_ReturnsErrInvalidTOTP() {
	usr, rawSecret := s.testUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	// The store's conditional counter update reports the code as already used.
	// This guard lives in the DB, so it holds across restarts and instances.
	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(false, nil)

	_, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

// ── Logout ──

func (s *ManagerSuite) TestLogout_DeletesSession() {
	s.sessions.EXPECT().Delete(gomock.Any(), "sess-1").Return(nil)

	s.Require().NoError(s.mgr.Logout(s.T().Context(), "sess-1"))
}

// ── GetSession ──

func (s *ManagerSuite) TestGetSession_ExpiredSession_ReturnsErrSessionExpired() {
	s.sessions.EXPECT().
		Get(gomock.Any(), "bad-sess").
		Return(nil, domain.ErrSessionExpired)

	_, err := s.mgr.GetSession(s.T().Context(), "bad-sess")
	s.ErrorIs(err, domain.ErrSessionExpired)
}

func (s *ManagerSuite) TestGetSession_ValidSession_ReturnsUser() {
	sess := &domain.Session{SessionID: "s1", UserID: s.userID, ExpiresAt: time.Now().Add(time.Hour)}
	usr := &domain.User{ID: s.userID, Username: "alice"}

	s.sessions.EXPECT().Get(gomock.Any(), "s1").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)

	got, err := s.mgr.GetSession(s.T().Context(), "s1")
	s.Require().NoError(err)
	s.Equal("alice", got.Username)
}

// ── GenerateInvite ──

func (s *ManagerSuite) TestGenerateInvite_NonAdmin_ReturnsErrForbidden() {
	nonAdmin := &domain.User{ID: s.userID, IsAdmin: false}
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(nonAdmin, nil)

	_, err := s.mgr.GenerateInvite(s.T().Context(), s.userID)
	s.ErrorIs(err, domain.ErrForbidden)
}

func (s *ManagerSuite) TestGenerateInvite_Admin_ReturnsToken() {
	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.invites.EXPECT().Issue(gomock.Any(), s.adminID).Return("tok-abc", nil)

	tok, err := s.mgr.GenerateInvite(s.T().Context(), s.adminID)
	s.Require().NoError(err)
	s.Equal("tok-abc", tok)
}

func (s *ManagerSuite) TestGenerateInvite_AdminLookupFails_WrapsError() {
	boom := errors.New("boom")
	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(nil, boom)

	_, err := s.mgr.GenerateInvite(s.T().Context(), s.adminID)
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

func (s *ManagerSuite) TestGenerateInvite_IssueFails_WrapsError() {
	boom := errors.New("boom")

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.invites.EXPECT().Issue(gomock.Any(), s.adminID).Return("", boom)

	_, err := s.mgr.GenerateInvite(s.T().Context(), s.adminID)
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

// ── AcceptInvite ──

func (s *ManagerSuite) TestAcceptInvite_WeakPassword_ReturnsErrWeakPassword() {
	_, err := s.mgr.AcceptInvite(s.T().Context(), "tok", "newuser", "weak")
	s.ErrorIs(err, domain.ErrWeakPassword)
}

func (s *ManagerSuite) TestAcceptInvite_InvalidToken_ReturnsErrInviteExpired() {
	s.users.EXPECT().GetByUsername(gomock.Any(), "newuser").Return(nil, domain.ErrNotFound)
	s.invites.EXPECT().
		RedeemInvite(gomock.Any(), "expired-tok", "newuser", gomock.Any()).
		Return(domain.ErrInviteExpired)

	_, err := s.mgr.AcceptInvite(s.T().Context(), "expired-tok", "newuser", testPassword)
	s.ErrorIs(err, domain.ErrInviteExpired)
}

func (s *ManagerSuite) TestAcceptInvite_ValidToken_CreatesUserAndReturnsQR() {
	s.users.EXPECT().GetByUsername(gomock.Any(), "newuser").Return(nil, domain.ErrNotFound)
	s.invites.EXPECT().
		RedeemInvite(gomock.Any(), "good-tok", "newuser", gomock.Any()).
		Return(nil)

	qrURL, err := s.mgr.AcceptInvite(s.T().Context(), "good-tok", "newuser", testPassword)
	s.Require().NoError(err)
	s.Contains(qrURL, "data:image/png;base64,")
}

func (s *ManagerSuite) TestAcceptInvite_RaceLoser_ReturnsErrUserExistsAndKeepsInvite() {
	// A username race lost inside the transaction: RedeemInvite rolls back, so the
	// invite is not burned and the caller sees ErrUserExists.
	s.users.EXPECT().GetByUsername(gomock.Any(), "newuser").Return(nil, domain.ErrNotFound)
	s.invites.EXPECT().
		RedeemInvite(gomock.Any(), "good-tok", "newuser", gomock.Any()).
		Return(domain.ErrUserExists)

	_, err := s.mgr.AcceptInvite(s.T().Context(), "good-tok", "newuser", testPassword)
	s.ErrorIs(err, domain.ErrUserExists)
}

func (s *ManagerSuite) TestAcceptInvite_DuplicateUsername_ReturnsErrUserExistsWithoutBurningInvite() {
	// GetByUsername finds existing user → ErrUserExists returned before RedeemInvite is called.
	s.users.EXPECT().
		GetByUsername(gomock.Any(), "existing").
		Return(&domain.User{ID: uuid.New(), Username: "existing"}, nil)
	// RedeemInvite must NOT be called.

	_, err := s.mgr.AcceptInvite(s.T().Context(), "tok", "existing", testPassword)
	s.ErrorIs(err, domain.ErrUserExists)
}

// ── AcceptFirstAdmin ──

func (s *ManagerSuite) TestAcceptFirstAdmin_WeakPassword_ReturnsErrWeakPassword() {
	_, err := s.mgr.AcceptFirstAdmin(s.T().Context(), "admin", "tooshort")
	s.ErrorIs(err, domain.ErrWeakPassword)
}

func (s *ManagerSuite) TestAcceptFirstAdmin_NonEmptyDB_ReturnsErrForbidden() {
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{{ID: uuid.New()}}, nil)

	_, err := s.mgr.AcceptFirstAdmin(s.T().Context(), "admin", testPassword)
	s.ErrorIs(err, domain.ErrForbidden)
}

func (s *ManagerSuite) TestAcceptFirstAdmin_EmptyDB_CreatesAdminAndReturnsQR() {
	s.users.EXPECT().List(gomock.Any()).Return(nil, nil)
	s.users.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ any, usr *domain.User) error {
			s.True(usr.IsAdmin, "first user must be admin")
			s.NotEmpty(usr.PasswordHash, "password hash must be set")
			s.NotEmpty(usr.KDFSalt, "kdf salt must be set")

			return nil
		},
	)

	qrURL, err := s.mgr.AcceptFirstAdmin(s.T().Context(), "firstadmin", testPassword)
	s.Require().NoError(err)
	s.Contains(qrURL, "data:image/png;base64,")
}

func (s *ManagerSuite) TestAcceptFirstAdmin_RaceLoser_PropagatesErrUserExists() {
	// Serialization is no longer done in-process: the partial unique index on
	// users.is_admin is the atomic guard. When a concurrent bootstrap (e.g. another
	// instance) wins the race, this caller's Create hits the index and the store
	// returns ErrUserExists, which AcceptFirstAdmin must propagate unchanged.
	s.users.EXPECT().List(gomock.Any()).Return(nil, nil)
	s.users.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrUserExists)

	_, err := s.mgr.AcceptFirstAdmin(s.T().Context(), "admin", testPassword)
	s.ErrorIs(err, domain.ErrUserExists)
}

// ── ChangePassword ──

func (s *ManagerSuite) TestChangePassword_WrongOldPassword_ReturnsErrInvalidPassword() {
	usr, _ := s.testUser()
	sess := &domain.Session{SessionID: "sess", UserID: s.userID}

	s.sessions.EXPECT().Get(gomock.Any(), "sess").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)

	_, err := s.mgr.ChangePassword(s.T().Context(), "sess", "WrongP@ss12!", "NewP@ssw0rd!", "000000")
	s.ErrorIs(err, domain.ErrInvalidPassword)
}

func (s *ManagerSuite) TestChangePassword_InvalidTOTP_ReturnsErrInvalidTOTP() {
	usr, _ := s.testUser()
	sess := &domain.Session{SessionID: "sess", UserID: s.userID}

	s.sessions.EXPECT().Get(gomock.Any(), "sess").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)

	_, err := s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "NewP@ssw0rd!", "000000")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

func (s *ManagerSuite) TestChangePassword_WeakNewPassword_ReturnsErrWeakPassword() {
	usr, rawSecret := s.testUser()
	sess := &domain.Session{SessionID: "sess", UserID: s.userID}

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	s.sessions.EXPECT().Get(gomock.Any(), "sess").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)

	_, err = s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "weak", code)
	s.ErrorIs(err, domain.ErrWeakPassword)
}

func (s *ManagerSuite) TestChangePassword_Success_UpdatesPasswordAndRotatesSession() {
	usr, rawSecret := s.testUser()
	sess := &domain.Session{SessionID: "sess", UserID: s.userID, UserAgent: "ua", IP: "1.2.3.4"}

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	s.sessions.EXPECT().Get(gomock.Any(), "sess").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.users.EXPECT().UpdatePassword(gomock.Any(), s.userID, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil)
	// Create must be called BEFORE DeleteByUserIDExcept (atomicity fix).
	// DeleteByUserIDExcept preserves the new session while wiping old ones.
	gomock.InOrder(
		s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
		s.sessions.EXPECT().DeleteByUserIDExcept(gomock.Any(), s.userID, gomock.Any()).Return(nil),
	)

	newSessID, err := s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "NewP@ssw0rd!", code)
	s.Require().NoError(err)
	s.NotEmpty(newSessID)
	s.NotEqual("sess", newSessID)
}

func (s *ManagerSuite) TestChangePassword_DeleteByUserIDFails_ReturnsNewSessionID() {
	usr, rawSecret := s.testUser()
	sess := &domain.Session{SessionID: "sess", UserID: s.userID, UserAgent: "ua", IP: "1.2.3.4"}

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	s.sessions.EXPECT().Get(gomock.Any(), "sess").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.users.EXPECT().UpdatePassword(gomock.Any(), s.userID, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil)
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.sessions.EXPECT().DeleteByUserIDExcept(gomock.Any(), s.userID, gomock.Any()).Return(errors.New("db timeout"))

	// DeleteByUserIDExcept failure should warn but not fail the caller — new session was created.
	newSessID, err := s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "NewP@ssw0rd!", code)
	s.Require().NoError(err)
	s.NotEmpty(newSessID)
}

// ── IsEmpty ──

func (s *ManagerSuite) TestIsEmpty_NoUsers_ReturnsTrue() {
	s.users.EXPECT().List(gomock.Any()).Return(nil, nil)

	empty, err := s.mgr.IsEmpty(s.T().Context())
	s.Require().NoError(err)
	s.True(empty)
}

func (s *ManagerSuite) TestIsEmpty_HasUsers_ReturnsFalse() {
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{{ID: uuid.New()}}, nil)

	empty, err := s.mgr.IsEmpty(s.T().Context())
	s.Require().NoError(err)
	s.False(empty)
}

// ── ListUsers ──

func (s *ManagerSuite) TestListUsers_Admin_ReturnsUsers() {
	users := []*domain.User{s.adminUser(), {ID: uuid.New(), Username: "bob"}}

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.users.EXPECT().List(gomock.Any()).Return(users, nil)

	got, err := s.mgr.ListUsers(s.T().Context(), s.adminID)
	s.Require().NoError(err)
	s.Equal(users, got)
}

func (s *ManagerSuite) TestListUsers_NonAdmin_ReturnsErrForbidden() {
	caller := uuid.New()
	nonAdmin := &domain.User{ID: caller, IsAdmin: false}
	s.users.EXPECT().GetByID(gomock.Any(), caller).Return(nonAdmin, nil)

	_, err := s.mgr.ListUsers(s.T().Context(), caller)
	s.ErrorIs(err, domain.ErrForbidden)
}

func (s *ManagerSuite) TestListUsers_AdminLookupFails_WrapsError() {
	boom := errors.New("boom")
	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(nil, boom)

	_, err := s.mgr.ListUsers(s.T().Context(), s.adminID)
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

func (s *ManagerSuite) TestListUsers_ListFails_WrapsError() {
	boom := errors.New("boom")

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.users.EXPECT().List(gomock.Any()).Return(nil, boom)

	_, err := s.mgr.ListUsers(s.T().Context(), s.adminID)
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

// ── RevokeUser ──

func (s *ManagerSuite) TestRevokeUser_SelfRevoke_ReturnsErrForbidden() {
	// Self-revoke rejected before any DB call.
	s.ErrorIs(s.mgr.RevokeUser(s.T().Context(), s.adminID, s.adminID), domain.ErrForbidden)
}

func (s *ManagerSuite) TestRevokeUser_NonAdmin_ReturnsErrForbidden() {
	caller := uuid.New()
	target := uuid.New()
	nonAdmin := &domain.User{ID: caller, IsAdmin: false}
	s.users.EXPECT().GetByID(gomock.Any(), caller).Return(nonAdmin, nil)

	s.ErrorIs(s.mgr.RevokeUser(s.T().Context(), caller, target), domain.ErrForbidden)
}

func (s *ManagerSuite) TestRevokeUser_AdminLookupFails_WrapsError() {
	boom := errors.New("boom")
	target := uuid.New()

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(nil, boom)

	err := s.mgr.RevokeUser(s.T().Context(), s.adminID, target)
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

func (s *ManagerSuite) TestRevokeUser_TargetLookupFails_WrapsError() {
	boom := errors.New("boom")
	target := uuid.New()

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.users.EXPECT().GetByID(gomock.Any(), target).Return(nil, boom)

	err := s.mgr.RevokeUser(s.T().Context(), s.adminID, target)
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

func (s *ManagerSuite) TestRevokeUser_LastAdminListFails_WrapsError() {
	targetID := uuid.New()
	target := &domain.User{ID: targetID, IsAdmin: true}
	boom := errors.New("boom")

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.users.EXPECT().GetByID(gomock.Any(), targetID).Return(target, nil)
	s.users.EXPECT().List(gomock.Any()).Return(nil, boom)

	err := s.mgr.RevokeUser(s.T().Context(), s.adminID, targetID)
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

func (s *ManagerSuite) TestRevokeUser_RevokeFails_WrapsError() {
	targetID := uuid.New()
	nonAdminTarget := &domain.User{ID: targetID, IsAdmin: false}
	boom := errors.New("boom")

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.users.EXPECT().GetByID(gomock.Any(), targetID).Return(nonAdminTarget, nil)
	s.users.EXPECT().Revoke(gomock.Any(), targetID).Return(boom)

	err := s.mgr.RevokeUser(s.T().Context(), s.adminID, targetID)
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

func (s *ManagerSuite) TestRevokeUser_LastAdmin_ReturnsErrForbidden() {
	targetID := uuid.New()
	target := &domain.User{ID: targetID, IsAdmin: true}

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.users.EXPECT().GetByID(gomock.Any(), targetID).Return(target, nil)
	// Only admin in the system is the target — no others remain.
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{target}, nil)

	s.ErrorIs(s.mgr.RevokeUser(s.T().Context(), s.adminID, targetID), domain.ErrForbidden)
}

func (s *ManagerSuite) TestRevokeUser_Admin_RevokesNonAdminTarget() {
	targetID := uuid.New()
	nonAdminTarget := &domain.User{ID: targetID, IsAdmin: false}

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.users.EXPECT().GetByID(gomock.Any(), targetID).Return(nonAdminTarget, nil)
	s.users.EXPECT().Revoke(gomock.Any(), targetID).Return(nil)

	s.Require().NoError(s.mgr.RevokeUser(s.T().Context(), s.adminID, targetID))
}

func (s *ManagerSuite) TestRevokeUser_Admin_RevokesSecondAdmin() {
	admin2ID := uuid.New()
	secondAdmin := &domain.User{ID: admin2ID, IsAdmin: true}

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.users.EXPECT().GetByID(gomock.Any(), admin2ID).Return(secondAdmin, nil)
	// Two admins exist; after revoke one remains → allowed.
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{s.adminUser(), secondAdmin}, nil)
	s.users.EXPECT().Revoke(gomock.Any(), admin2ID).Return(nil)

	s.Require().NoError(s.mgr.RevokeUser(s.T().Context(), s.adminID, admin2ID))
}

func TestAuthManager(t *testing.T) {
	suite.Run(t, new(ManagerSuite))
}
