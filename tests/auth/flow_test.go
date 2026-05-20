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

const flowPassword = "FlowP@ss12!"

// ── AuthFlowSuite ──

// AuthFlowSuite runs end-to-end auth flows against real storage repos.
// It embeds SQLiteAuthSuite to get a fresh in-memory DB per test.
type AuthFlowSuite struct {
	SQLiteAuthSuite

	mgr *auth.Manager
}

func (s *AuthFlowSuite) SetupTest() {
	s.SQLiteAuthSuite.SetupTest()

	s.mgr = auth.NewManager(
		s.Users,
		s.Invites,
		s.Sessions,
		crypto.New(),
		slog.New(slog.DiscardHandler),
		"padmark-test",
		time.Hour,
	)
}

func (s *AuthFlowSuite) TearDownTest() {
	s.SQLiteAuthSuite.TearDownTest()
}

// decryptTOTPSecret derives the user key from password + kdfSalt and decrypts the TOTP secret.
func (s *AuthFlowSuite) decryptTOTPSecret(usr *domain.User, password string) string {
	s.T().Helper()

	derivedKey, err := crypto.DeriveUserKey([]byte(password), usr.KDFSalt)
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

	rawSecret := s.decryptTOTPSecret(usr, flowPassword)

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

	rawSecret := s.decryptTOTPSecret(newUsr, flowPassword)

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

	rawSecret := s.decryptTOTPSecret(usr, flowPassword)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	sessID, err := s.mgr.Login(ctx, "root3", flowPassword, code, "", "")
	s.Require().NoError(err)

	s.Require().NoError(s.mgr.Logout(ctx, sessID))

	_, err = s.mgr.GetSession(ctx, sessID)
	s.ErrorIs(err, domain.ErrSessionExpired)
}

func TestAuthFlow(t *testing.T) {
	suite.Run(t, new(AuthFlowSuite))
}
