package auth

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
)

var discardLog = slog.New(slog.DiscardHandler) //nolint:gochecknoglobals // test helper

const testPassword = "ValidP@ss12!"

type ManagerSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	users    *MockUserStore
	invites  *MockInviteStore
	sessions *MockSessionStore
	mgr      *Manager
}

func (s *ManagerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.users = NewMockUserStore(s.ctrl)
	s.invites = NewMockInviteStore(s.ctrl)
	s.sessions = NewMockSessionStore(s.ctrl)
	s.mgr = NewManager(s.users, s.invites, s.sessions, crypto.New(), discardLog, "padmark", 0)
}

func (s *ManagerSuite) TearDownTest() {
	s.ctrl.Finish()
}

// ── helpers ──

func (s *ManagerSuite) adminUser() *domain.User {
	return &domain.User{
		ID:       "admin-uuid",
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

	pwHash, err := crypto.HashPassword(testPassword)
	s.Require().NoError(err)

	usr := &domain.User{
		ID:           "user-id",
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
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), "user-id", gomock.Any()).Return(nil)

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1")
	s.Require().NoError(err)
	s.NotEmpty(sessID)
}

func (s *ManagerSuite) TestLogin_ReplayedTOTP_ReturnsErrInvalidTOTP() {
	usr, rawSecret := s.testUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	// First login succeeds.
	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil).Times(2)
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), "user-id", gomock.Any()).Return(nil)

	_, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1")
	s.Require().NoError(err)

	// Second attempt with the same code must be rejected.
	_, err = s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1")
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
	sess := &domain.Session{SessionID: "s1", UserID: "user-id", ExpiresAt: time.Now().Add(time.Hour)}
	usr := &domain.User{ID: "user-id", Username: "alice"}

	s.sessions.EXPECT().Get(gomock.Any(), "s1").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), "user-id").Return(usr, nil)

	got, err := s.mgr.GetSession(s.T().Context(), "s1")
	s.Require().NoError(err)
	s.Equal("alice", got.Username)
}

// ── GenerateInvite ──

func (s *ManagerSuite) TestGenerateInvite_NonAdmin_ReturnsErrForbidden() {
	nonAdmin := &domain.User{ID: "user-id", IsAdmin: false}
	s.users.EXPECT().GetByID(gomock.Any(), "user-id").Return(nonAdmin, nil)

	_, err := s.mgr.GenerateInvite(s.T().Context(), "user-id")
	s.ErrorIs(err, domain.ErrForbidden)
}

func (s *ManagerSuite) TestGenerateInvite_Admin_ReturnsToken() {
	s.users.EXPECT().GetByID(gomock.Any(), "admin-uuid").Return(s.adminUser(), nil)
	s.invites.EXPECT().Issue(gomock.Any(), "admin-uuid").Return("tok-abc", nil)

	tok, err := s.mgr.GenerateInvite(s.T().Context(), "admin-uuid")
	s.Require().NoError(err)
	s.Equal("tok-abc", tok)
}

// ── AcceptInvite ──

func (s *ManagerSuite) TestAcceptInvite_WeakPassword_ReturnsErrWeakPassword() {
	_, err := s.mgr.AcceptInvite(s.T().Context(), "tok", "newuser", "weak")
	s.ErrorIs(err, domain.ErrWeakPassword)
}

func (s *ManagerSuite) TestAcceptInvite_InvalidToken_ReturnsErrInviteExpired() {
	s.users.EXPECT().GetByUsername(gomock.Any(), "newuser").Return(nil, domain.ErrNotFound)
	s.invites.EXPECT().
		Consume(gomock.Any(), "expired-tok", "newuser").
		Return(nil, domain.ErrInviteExpired)

	_, err := s.mgr.AcceptInvite(s.T().Context(), "expired-tok", "newuser", testPassword)
	s.ErrorIs(err, domain.ErrInviteExpired)
}

func (s *ManagerSuite) TestAcceptInvite_ValidToken_CreatesUserAndReturnsQR() {
	s.users.EXPECT().GetByUsername(gomock.Any(), "newuser").Return(nil, domain.ErrNotFound)
	s.invites.EXPECT().
		Consume(gomock.Any(), "good-tok", "newuser").
		Return(&domain.Invite{Token: "good-tok"}, nil)
	s.users.EXPECT().
		Create(gomock.Any(), gomock.Any()).
		Return(nil)

	qrURL, err := s.mgr.AcceptInvite(s.T().Context(), "good-tok", "newuser", testPassword)
	s.Require().NoError(err)
	s.Contains(qrURL, "data:image/png;base64,")
}

func (s *ManagerSuite) TestAcceptInvite_DuplicateUsername_ReturnsErrUserExistsWithoutBurningInvite() {
	// GetByUsername finds existing user → ErrUserExists returned before Consume is called.
	s.users.EXPECT().
		GetByUsername(gomock.Any(), "existing").
		Return(&domain.User{ID: "x", Username: "existing"}, nil)
	// Consume and Create must NOT be called.

	_, err := s.mgr.AcceptInvite(s.T().Context(), "tok", "existing", testPassword)
	s.ErrorIs(err, domain.ErrUserExists)
}

// ── AcceptFirstAdmin ──

func (s *ManagerSuite) TestAcceptFirstAdmin_WeakPassword_ReturnsErrWeakPassword() {
	_, err := s.mgr.AcceptFirstAdmin(s.T().Context(), "admin", "tooshort")
	s.ErrorIs(err, domain.ErrWeakPassword)
}

func (s *ManagerSuite) TestAcceptFirstAdmin_NonEmptyDB_ReturnsErrForbidden() {
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{{ID: "existing"}}, nil)

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

func (s *ManagerSuite) TestAcceptFirstAdmin_ConcurrentCalls_OnlyOneBecomesAdmin() {
	// Both goroutines see an empty DB on their first List call.
	// The mutex guarantees only one Create proceeds; the second List call returns a non-empty
	// result so the second goroutine gets ErrForbidden.
	callCount := 0

	s.users.EXPECT().List(gomock.Any()).DoAndReturn(func(_ any) ([]*domain.User, error) {
		callCount++
		if callCount == 1 {
			return nil, nil
		}

		return []*domain.User{{ID: "admin-created"}}, nil
	}).Times(2)

	s.users.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	results := make([]error, 2)
	done := make(chan int, 2)

	for i := range 2 {
		go func(idx int) {
			_, err := s.mgr.AcceptFirstAdmin(s.T().Context(), "admin", testPassword)

			results[idx] = err
			done <- idx
		}(i)
	}

	<-done
	<-done

	successes := 0
	forbidden := 0

	for _, err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, domain.ErrForbidden) {
			forbidden++
		}
	}

	s.Equal(1, successes, "exactly one goroutine should become admin")
	s.Equal(1, forbidden, "the other goroutine should get ErrForbidden")
}

// ── ChangePassword ──

func (s *ManagerSuite) TestChangePassword_WrongOldPassword_ReturnsErrInvalidPassword() {
	usr, _ := s.testUser()
	sess := &domain.Session{SessionID: "sess", UserID: "user-id"}

	s.sessions.EXPECT().Get(gomock.Any(), "sess").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), "user-id").Return(usr, nil)

	err := s.mgr.ChangePassword(s.T().Context(), "sess", "WrongP@ss12!", "NewP@ssw0rd!")
	s.ErrorIs(err, domain.ErrInvalidPassword)
}

func (s *ManagerSuite) TestChangePassword_WeakNewPassword_ReturnsErrWeakPassword() {
	usr, _ := s.testUser()
	sess := &domain.Session{SessionID: "sess", UserID: "user-id"}

	s.sessions.EXPECT().Get(gomock.Any(), "sess").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), "user-id").Return(usr, nil)

	err := s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "weak")
	s.ErrorIs(err, domain.ErrWeakPassword)
}

func (s *ManagerSuite) TestChangePassword_Success_UpdatesPassword() {
	usr, _ := s.testUser()
	sess := &domain.Session{SessionID: "sess", UserID: "user-id"}

	s.sessions.EXPECT().Get(gomock.Any(), "sess").Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), "user-id").Return(usr, nil)
	s.users.EXPECT().UpdatePassword(gomock.Any(), "user-id", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil)

	err := s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "NewP@ssw0rd!")
	s.Require().NoError(err)
}

// ── IsEmpty ──

func (s *ManagerSuite) TestIsEmpty_NoUsers_ReturnsTrue() {
	s.users.EXPECT().List(gomock.Any()).Return(nil, nil)

	empty, err := s.mgr.IsEmpty(s.T().Context())
	s.Require().NoError(err)
	s.True(empty)
}

func (s *ManagerSuite) TestIsEmpty_HasUsers_ReturnsFalse() {
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{{ID: "u1"}}, nil)

	empty, err := s.mgr.IsEmpty(s.T().Context())
	s.Require().NoError(err)
	s.False(empty)
}

// ── RevokeUser ──

func (s *ManagerSuite) TestRevokeUser_SelfRevoke_ReturnsErrForbidden() {
	// Self-revoke rejected before any DB call.
	s.ErrorIs(s.mgr.RevokeUser(s.T().Context(), "admin-uuid", "admin-uuid"), domain.ErrForbidden)
}

func (s *ManagerSuite) TestRevokeUser_NonAdmin_ReturnsErrForbidden() {
	nonAdmin := &domain.User{ID: "caller", IsAdmin: false}
	s.users.EXPECT().GetByID(gomock.Any(), "caller").Return(nonAdmin, nil)

	s.ErrorIs(s.mgr.RevokeUser(s.T().Context(), "caller", "target"), domain.ErrForbidden)
}

func (s *ManagerSuite) TestRevokeUser_LastAdmin_ReturnsErrForbidden() {
	target := &domain.User{ID: "target-uuid", IsAdmin: true}

	s.users.EXPECT().GetByID(gomock.Any(), "admin-uuid").Return(s.adminUser(), nil)
	s.users.EXPECT().GetByID(gomock.Any(), "target-uuid").Return(target, nil)
	// Only admin in the system is the target — no others remain.
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{target}, nil)

	s.ErrorIs(s.mgr.RevokeUser(s.T().Context(), "admin-uuid", "target-uuid"), domain.ErrForbidden)
}

func (s *ManagerSuite) TestRevokeUser_Admin_RevokesNonAdminTarget() {
	nonAdminTarget := &domain.User{ID: "target-uuid", IsAdmin: false}

	s.users.EXPECT().GetByID(gomock.Any(), "admin-uuid").Return(s.adminUser(), nil)
	s.users.EXPECT().GetByID(gomock.Any(), "target-uuid").Return(nonAdminTarget, nil)
	s.users.EXPECT().Revoke(gomock.Any(), "target-uuid").Return(nil)

	s.Require().NoError(s.mgr.RevokeUser(s.T().Context(), "admin-uuid", "target-uuid"))
}

func (s *ManagerSuite) TestRevokeUser_Admin_RevokesSecondAdmin() {
	secondAdmin := &domain.User{ID: "admin2", IsAdmin: true}

	s.users.EXPECT().GetByID(gomock.Any(), "admin-uuid").Return(s.adminUser(), nil)
	s.users.EXPECT().GetByID(gomock.Any(), "admin2").Return(secondAdmin, nil)
	// Two admins exist; after revoke one remains → allowed.
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{s.adminUser(), secondAdmin}, nil)
	s.users.EXPECT().Revoke(gomock.Any(), "admin2").Return(nil)

	s.Require().NoError(s.mgr.RevokeUser(s.T().Context(), "admin-uuid", "admin2"))
}

func TestAuthManager(t *testing.T) {
	suite.Run(t, new(ManagerSuite))
}
