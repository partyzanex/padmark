//go:build integration

package integration

import (
	"log/slog"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// flowPassword is the test password used throughout the flow suite.
//

const flowPassword = "FlowP@ss123!"

// ── AuthFlowSuite ──

// AuthFlowSuite runs end-to-end auth flows against real storage repos.
// It embeds SQLiteAuthSuite to get a fresh in-memory DB per test.
type AuthFlowSuite struct {
	SQLiteAuthSuite

	mgr *auth.Manager
}

func (s *AuthFlowSuite) SetupTest() {
	s.SQLiteAuthSuite.SetupTest()

	mgr, err := auth.NewManager(
		s.Users,
		s.Invites,
		s.Sessions,
		nil, // this suite exercises TOTP/session flows, not API tokens
		crypto.New(),
		crypto.NewPasswordHasher(testArgon2Params),
		crypto.NewKDF(),
		crypto.NewTOTP(),
		slog.New(slog.DiscardHandler),
		"padmark-test",
		time.Hour,
	)
	s.Require().NoError(err)
	s.mgr = mgr
}

func (s *AuthFlowSuite) TearDownTest() {
	s.SQLiteAuthSuite.TearDownTest()
}

// decryptTOTPSecret derives the user key from flowPassword + kdfSalt and decrypts the TOTP secret.
func (s *AuthFlowSuite) decryptTOTPSecret(usr *domain.User) string {
	s.T().Helper()

	derivedKey, err := crypto.DeriveUserKey([]byte(flowPassword), usr.KDFSalt)
	s.Require().NoError(err)

	rawSecret, err := crypto.New().Decrypt(usr.TOTPSecret, derivedKey)
	s.Require().NoError(err)

	return rawSecret
}

// TestFlow_AcceptFirstAdmin_Login_GetSession exercises the full happy path:
// bootstrap → TOTP login → session retrieval.
func (s *AuthFlowSuite) TestFlow_AcceptFirstAdmin_Login_GetSession() {
	ctx := s.T().Context()

	qrURL, err := s.mgr.AcceptFirstAdmin(ctx, "root", flowPassword)
	s.Require().NoError(err)
	s.Contains(qrURL, "data:image/png;base64,")

	usr, err := s.Users.GetByUsername(ctx, "root")
	s.Require().NoError(err)
	s.True(usr.IsAdmin)

	rawSecret := s.decryptTOTPSecret(usr)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	sessID, err := s.mgr.Login(ctx, "root", flowPassword, code, "Mozilla/5.0", "127.0.0.1")
	s.Require().NoError(err)
	s.NotEmpty(sessID)

	got, err := s.mgr.GetSession(ctx, sessID)
	s.Require().NoError(err)
	s.Equal("root", got.Username)
	s.True(got.IsAdmin)
}

// TestFlow_AcceptInvite_Login_GetSession exercises the invite-link onboarding flow.
func (s *AuthFlowSuite) TestFlow_AcceptInvite_Login_GetSession() {
	ctx := s.T().Context()

	qrURL, err := s.mgr.AcceptFirstAdmin(ctx, "admin", flowPassword)
	s.Require().NoError(err)
	s.NotEmpty(qrURL)

	admin, err := s.Users.GetByUsername(ctx, "admin")
	s.Require().NoError(err)

	tok, err := s.mgr.GenerateInvite(ctx, admin.ID)
	s.Require().NoError(err)
	s.NotEmpty(tok)

	newQR, err := s.mgr.AcceptInvite(ctx, tok, "newuser", flowPassword)
	s.Require().NoError(err)
	s.Contains(newQR, "data:image/png;base64,")

	newUsr, err := s.Users.GetByUsername(ctx, "newuser")
	s.Require().NoError(err)

	rawSecret := s.decryptTOTPSecret(newUsr)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	sessID, err := s.mgr.Login(ctx, "newuser", flowPassword, code, "UA", "10.0.0.1")
	s.Require().NoError(err)

	got, err := s.mgr.GetSession(ctx, sessID)
	s.Require().NoError(err)
	s.Equal("newuser", got.Username)
	s.False(got.IsAdmin)
}

// TestFlow_Login_WrongTOTP_Rejected verifies invalid codes are rejected.
func (s *AuthFlowSuite) TestFlow_Login_WrongTOTP_Rejected() {
	ctx := s.T().Context()

	_, err := s.mgr.AcceptFirstAdmin(ctx, "root2", flowPassword)
	s.Require().NoError(err)

	_, err = s.mgr.Login(ctx, "root2", flowPassword, "000000", "", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

// TestFlow_Login_WrongPassword_Rejected verifies wrong passwords are rejected.
func (s *AuthFlowSuite) TestFlow_Login_WrongPassword_Rejected() {
	ctx := s.T().Context()

	_, err := s.mgr.AcceptFirstAdmin(ctx, "root4", flowPassword)
	s.Require().NoError(err)

	_, err = s.mgr.Login(ctx, "root4", "WrongP@ss12!", "000000", "", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

// TestFlow_Logout_SessionExpires verifies that after logout the session is gone.
func (s *AuthFlowSuite) TestFlow_Logout_SessionExpires() {
	ctx := s.T().Context()

	_, err := s.mgr.AcceptFirstAdmin(ctx, "root3", flowPassword)
	s.Require().NoError(err)

	usr, err := s.Users.GetByUsername(ctx, "root3")
	s.Require().NoError(err)

	rawSecret := s.decryptTOTPSecret(usr)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	sessID, err := s.mgr.Login(ctx, "root3", flowPassword, code, "", "")
	s.Require().NoError(err)

	s.Require().NoError(s.mgr.Logout(ctx, sessID))

	_, err = s.mgr.GetSession(ctx, sessID)
	s.ErrorIs(err, domain.ErrSessionExpired)
}

// TestFlow_ChangePassword_InvalidatesOldSessions verifies that after a password
// change the original session cookie is rejected and the returned new session is valid.
// A second "stolen" session is injected directly into the DB to simulate a concurrent
// device without going through the TOTP flow (which would reject the same one-time code).
func (s *AuthFlowSuite) TestFlow_ChangePassword_InvalidatesOldSessions() {
	ctx := s.T().Context()

	_, err := s.mgr.AcceptFirstAdmin(ctx, "pwchange-user", flowPassword)
	s.Require().NoError(err)

	usr, err := s.Users.GetByUsername(ctx, "pwchange-user")
	s.Require().NoError(err)

	rawSecret := s.decryptTOTPSecret(usr)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	sess1, err := s.mgr.Login(ctx, "pwchange-user", flowPassword, code, "UA1", "1.1.1.1")
	s.Require().NoError(err)

	// Simulate a second active session (e.g., stolen cookie) inserted directly.
	stolenSess := &domain.Session{
		SessionID: "stolen-session-id",
		UserID:    usr.ID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		UserAgent: "attacker-ua",
		IP:        "9.9.9.9",
	}
	s.Require().NoError(s.Sessions.Create(ctx, stolenSess))

	// Both sessions must be valid before the password change.
	_, err = s.mgr.GetSession(ctx, sess1)
	s.Require().NoError(err, "sess1 must be valid before password change")
	_, err = s.Sessions.Get(ctx, stolenSess.SessionID)
	s.Require().NoError(err, "stolen session must exist before password change")

	const newPassword = "N3wP@ssw0rd!"

	// Use the code for the NEXT 30-second period so it is not the same one used
	// during Login above (the TOTP counter replay guard would reject a reuse).
	changeCode, err := totp.GenerateCode(rawSecret, time.Now().Add(30*time.Second))
	s.Require().NoError(err)

	newSessID, err := s.mgr.ChangePassword(ctx, sess1, flowPassword, newPassword, changeCode)
	s.Require().NoError(err)
	s.NotEmpty(newSessID)
	s.NotEqual(sess1, newSessID, "returned session must be a fresh ID")
	s.NotEqual(stolenSess.SessionID, newSessID)

	// The original login session must now be rejected.
	_, err = s.mgr.GetSession(ctx, sess1)
	s.Require().ErrorIs(err, domain.ErrSessionExpired, "original session must be invalidated after password change")

	// The stolen session must also be rejected.
	_, err = s.Sessions.Get(ctx, stolenSess.SessionID)
	s.Require().ErrorIs(err, domain.ErrSessionExpired, "stolen session must be invalidated after password change")

	// The newly issued session must be valid and resolve to the correct user.
	got, err := s.mgr.GetSession(ctx, newSessID)
	s.Require().NoError(err, "new session returned by ChangePassword must be valid")
	s.Equal("pwchange-user", got.Username)
}

// TestFlow_ChangePassword_WrongTOTP_Rejected verifies that ChangePassword rejects
// a correct old password but invalid TOTP code (the step-up requirement).
func (s *AuthFlowSuite) TestFlow_ChangePassword_WrongTOTP_Rejected() {
	ctx := s.T().Context()

	_, err := s.mgr.AcceptFirstAdmin(ctx, "totp-stepup-user", flowPassword)
	s.Require().NoError(err)

	usr, err := s.Users.GetByUsername(ctx, "totp-stepup-user")
	s.Require().NoError(err)

	rawSecret := s.decryptTOTPSecret(usr)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	sessID, err := s.mgr.Login(ctx, "totp-stepup-user", flowPassword, code, "UA", "1.1.1.1")
	s.Require().NoError(err)

	_, err = s.mgr.ChangePassword(ctx, sessID, flowPassword, "N3wP@ssw0rd!", "000000")
	s.ErrorIs(err, domain.ErrInvalidTOTP,
		"ChangePassword must reject an invalid TOTP code even with correct old password")
}

// TestFlow_ChangePassword_CreateBeforeDelete verifies the atomicity fix:
// new session must be retrievable via GetSession immediately after ChangePassword,
// confirming Create ran before any potential Delete failure.
func (s *AuthFlowSuite) TestFlow_ChangePassword_NewSessionValidBeforeOldExpire() {
	ctx := s.T().Context()

	_, err := s.mgr.AcceptFirstAdmin(ctx, "atomic-cp-user", flowPassword)
	s.Require().NoError(err)

	usr, err := s.Users.GetByUsername(ctx, "atomic-cp-user")
	s.Require().NoError(err)

	rawSecret := s.decryptTOTPSecret(usr)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	sessID, err := s.mgr.Login(ctx, "atomic-cp-user", flowPassword, code, "UA", "1.1.1.1")
	s.Require().NoError(err)

	// Use the next 30-second window's code to avoid the replay guard rejecting a
	// code from the same period used during Login.
	changeCode, err := totp.GenerateCode(rawSecret, time.Now().Add(30*time.Second))
	s.Require().NoError(err)

	const newPassword = "N3wP@ssw0rd!"

	newSessID, err := s.mgr.ChangePassword(ctx, sessID, flowPassword, newPassword, changeCode)
	s.Require().NoError(err)
	s.NotEmpty(newSessID)

	// New session must be immediately accessible.
	got, err := s.mgr.GetSession(ctx, newSessID)
	s.Require().NoError(err, "new session must exist right after ChangePassword")
	s.Equal("atomic-cp-user", got.Username)

	// Old session must be gone (DeleteByUserID ran after Create).
	_, err = s.mgr.GetSession(ctx, sessID)
	s.ErrorIs(err, domain.ErrSessionExpired, "old session must be invalidated")
}

func TestAuthFlow(t *testing.T) {
	suite.Run(t, new(AuthFlowSuite))
}
