package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/testutil"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// testArgon2Params is the minimal argon2id cost (testutil.Argon2Params): these tests exercise
// real hash/verify logic, and the production cost is covered on its own in internal/infra/crypto's
// tests.
//
//nolint:gochecknoglobals // test helper
var testArgon2Params = testutil.Argon2Params()

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
	mgr, err := NewManager(
		Stores{Users: s.users, Invites: s.invites, Sessions: s.sessions, APITokens: nil},
		crypto.NewPasswordHasher(testArgon2Params),
		crypto.NewSealer(testArgon2Params), crypto.NewTOTP(), discardLog(), "padmark", 0)
	s.Require().NoError(err)
	s.mgr = mgr
	s.adminID = uuid.New()
	s.userID = uuid.New()
}

func (s *ManagerSuite) TearDownTest() {
	s.ctrl.Finish()
}

// ── helpers ──.

func (s *ManagerSuite) adminUser() *domain.User {
	return &domain.User{
		ID:       s.adminID,
		Username: "admin",
		IsAdmin:  true,

		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
}

// testUser builds a domain.User whose TOTP secret is sealed under testPassword at the suite's
// argon2 cost — i.e. an already-current seal, so login never triggers the re-seal migration.
// Returns the user and the raw (unsealed) TOTP secret.
func (s *ManagerSuite) testUser() (*domain.User, string) {
	s.T().Helper()

	return s.userWithSealedSecret(func(rawSecret string, kdfSalt []byte) string {
		sealed, err := crypto.NewSealer(testArgon2Params).Seal(rawSecret, []byte(testPassword), kdfSalt)
		s.Require().NoError(err)

		return sealed
	})
}

// legacyTestUser builds a user whose TOTP secret uses the pre-versioning layout: a bare base64
// ciphertext keyed by argon2id, with no envelope recording the cost. Logging in as this user
// must still work and must migrate the secret forward.
func (s *ManagerSuite) legacyTestUser() (*domain.User, string) {
	s.T().Helper()

	return s.userWithSealedSecret(func(rawSecret string, kdfSalt []byte) string {
		derivedKey, err := crypto.DeriveUserKey([]byte(testPassword), kdfSalt, testArgon2Params)
		s.Require().NoError(err)

		ciphertext, err := crypto.New().Encrypt(rawSecret, derivedKey)
		s.Require().NoError(err)

		return ciphertext
	})
}

// userWithSealedSecret assembles the shared user fixture, delegating only the on-disk
// representation of the TOTP secret to seal, so each fixture differs in exactly that one field.
func (s *ManagerSuite) userWithSealedSecret(seal func(rawSecret string, kdfSalt []byte) string) (*domain.User, string) {
	s.T().Helper()

	kdfSalt, err := crypto.GenerateKDFSalt()
	s.Require().NoError(err)

	rawSecret, err := crypto.GenerateTOTPSecret()
	s.Require().NoError(err)

	encSecret := seal(rawSecret, kdfSalt)

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

		CreatedAt: time.Time{}, LastLoginAt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	return usr, rawSecret
}

// ── Login ──.

func (s *ManagerSuite) TestLogin_InvalidUsername_ReturnsErrInvalidTOTP() {
	s.users.EXPECT().
		GetByUsername(gomock.Any(), "unknown").
		Return(nil, domain.ErrNotFound)

	_, err := s.mgr.Login(s.T().Context(), "unknown", testPassword, "123456", "", "", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

func (s *ManagerSuite) TestLogin_WrongPassword_ReturnsErrInvalidTOTP() {
	usr, _ := s.testUser()
	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)

	_, err := s.mgr.Login(s.T().Context(), "alice", "WrongP@ss12!", "000000", "", "", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

func (s *ManagerSuite) TestLogin_WrongTOTP_ReturnsErrInvalidTOTP() {
	usr, _ := s.testUser()
	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)

	_, err := s.mgr.Login(s.T().Context(), "alice", testPassword, "000000", "", "", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

func (s *ManagerSuite) TestLogin_ValidTOTP_CreatesSession() {
	usr, rawSecret := s.testUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil) // confirmCredentials re-reads the user.
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.sessions.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), s.userID, gomock.Any()).Return(nil)

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "")
	s.Require().NoError(err)
	s.NotEmpty(sessID)
}

// TestLogin_RevokesPreviousSession pins that signing in again does not leave the session the
// browser presented behind: it is deleted right after the new one is created, so a stolen or
// forgotten cookie cannot outlive a re-login.
func (s *ManagerSuite) TestLogin_RevokesPreviousSession() {
	usr, rawSecret := s.testUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil) // confirmCredentials re-reads the user.
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)

	gomock.InOrder(
		s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
		s.sessions.EXPECT().Delete(gomock.Any(), hashToken("old-session")).Return(nil),
	)
	s.sessions.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), s.userID, gomock.Any()).Return(nil)

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "old-session")
	s.Require().NoError(err)
	s.NotEmpty(sessID)
	s.NotEqual("old-session", sessID)
}

// TestLogin_PreviousSessionDeleteFailure_DoesNotFailLogin — revoking the old session is
// best-effort (fail-soft): the new session is already valid and the caller holds a working login.
func (s *ManagerSuite) TestLogin_PreviousSessionDeleteFailure_DoesNotFailLogin() {
	usr, rawSecret := s.testUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil) // confirmCredentials re-reads the user.
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.sessions.EXPECT().Delete(gomock.Any(), hashToken("old-session")).Return(errors.New("db down"))
	s.sessions.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), s.userID, gomock.Any()).Return(nil)

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "old-session")
	s.Require().NoError(err)
	s.NotEmpty(sessID)
}

// TestLogin_DeleteExpiredFailure_DoesNotFailLogin pins that the session cleanup after a login
// is fail-soft: the new session is valid, so a failed cleanup is only logged.
func (s *ManagerSuite) TestLogin_DeleteExpiredFailure_DoesNotFailLogin() {
	usr, rawSecret := s.testUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil) // confirmCredentials re-reads the user.
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	gomock.InOrder(
		s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
		s.sessions.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(errors.New("db down")),
	)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), s.userID, gomock.Any()).Return(nil)

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "")
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

	_, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

// ── TOTP secret migration on login ──.

// TestLogin_LegacySeal_MigratesSecret pins the migration path for accounts whose TOTP secret
// predates the versioned envelope: the login succeeds and the secret is rewritten in the
// current format, so the account survives a later change to the configured argon2 cost.
func (s *ManagerSuite) TestLogin_LegacySeal_MigratesSecret() {
	usr, rawSecret := s.legacyTestUser()
	legacySealed := usr.TOTPSecret

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	var resealed string

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil) // confirmCredentials re-reads the user.
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.users.EXPECT().
		UpdateTOTPSecret(gomock.Any(), s.userID, usr.TOTPSecret, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _, secret string) (bool, error) {
			resealed = secret

			return true, nil
		})
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.sessions.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), s.userID, gomock.Any()).Return(nil)

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "")
	s.Require().NoError(err)
	s.NotEmpty(sessID)

	s.NotEqual(legacySealed, resealed, "the stored secret must have been rewritten")

	// The rewritten value must still yield the same TOTP secret, now under a versioned envelope
	// that no longer reports itself stale.
	got, stale, openErr := crypto.NewSealer(testArgon2Params).
		Open(resealed, []byte(testPassword), usr.KDFSalt)
	s.Require().NoError(openErr)
	s.Equal(rawSecret, got)
	s.False(stale)
}

// TestLogin_CurrentSeal_DoesNotRewrite guards against a needless write on every single login:
// an up-to-date secret must not touch the store. The suite's strict mock has no
// UpdateTOTPSecret expectation, so any call fails the test.
func (s *ManagerSuite) TestLogin_CurrentSeal_DoesNotRewrite() {
	usr, rawSecret := s.testUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil) // confirmCredentials re-reads the user.
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.sessions.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), s.userID, gomock.Any()).Return(nil)

	_, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "")
	s.Require().NoError(err)
}

// TestLogin_LegacySeal_ResealFailureDoesNotFailLogin pins the fail-soft contract: both factors
// have already passed and the old secret is still perfectly usable, so a failed migration write
// must not cost the user their login. The next login retries it.
func (s *ManagerSuite) TestLogin_LegacySeal_ResealFailureDoesNotFailLogin() {
	usr, rawSecret := s.legacyTestUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil) // confirmCredentials re-reads the user.
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.users.EXPECT().
		UpdateTOTPSecret(gomock.Any(), s.userID, gomock.Any(), gomock.Any()).
		Return(false, errors.New("db down"))
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.sessions.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), s.userID, gomock.Any()).Return(nil)

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "")
	s.Require().NoError(err)
	s.NotEmpty(sessID)
}

// TestLogin_WrongTOTP_DoesNotMigrate keeps the migration behind the second factor: a correct
// password with a wrong code must not trigger a write. The strict mock enforces this.
func (s *ManagerSuite) TestLogin_WrongTOTP_DoesNotMigrate() {
	usr, _ := s.legacyTestUser()
	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)

	_, err := s.mgr.Login(s.T().Context(), "alice", testPassword, "000000", "", "", "")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

// ── Logout ──.

func (s *ManagerSuite) TestLogout_DeletesSession() {
	s.sessions.EXPECT().Delete(gomock.Any(), hashToken("sess-1")).Return(nil)

	s.Require().NoError(s.mgr.Logout(s.T().Context(), "sess-1"))
}

// ── GetSession ──.

func (s *ManagerSuite) TestGetSession_ExpiredSession_ReturnsErrSessionExpired() {
	s.sessions.EXPECT().
		Get(gomock.Any(), hashToken("bad-sess")).
		Return(nil, domain.ErrSessionExpired)

	_, err := s.mgr.GetSession(s.T().Context(), "bad-sess")
	s.ErrorIs(err, domain.ErrSessionExpired)
}

func (s *ManagerSuite) TestGetSession_ValidSession_ReturnsUser() {
	sess := &domain.Session{
		SessionHash: "s1", UserID: s.userID, ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Time{}, UserAgent: "", IP: "",
	}
	usr := &domain.User{
		ID: s.userID, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}

	s.sessions.EXPECT().Get(gomock.Any(), hashToken("s1")).Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)

	got, err := s.mgr.GetSession(s.T().Context(), "s1")
	s.Require().NoError(err)
	s.Equal("alice", got.Username)
}

// ── GenerateInvite ──.

func (s *ManagerSuite) TestGenerateInvite_NonAdmin_ReturnsErrForbidden() {
	nonAdmin := &domain.User{
		ID: s.userID, IsAdmin: false, CreatedAt: time.Time{},
		LastLoginAt: nil, Username: "", TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(nonAdmin, nil)

	_, err := s.mgr.GenerateInvite(s.T().Context(), s.userID, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	})
	s.ErrorIs(err, domain.ErrForbidden)
}

// TestGenerateInvite_Admin_ReturnsToken pins the invite rules that the manager owns: the store
// receives only the hash of the returned token, the invite expires after inviteTTL, and stale
// invites are deleted after the insert.
func (s *ManagerSuite) TestGenerateInvite_Admin_ReturnsToken() {
	var stored *domain.Invite

	before := time.Now()

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	gomock.InOrder(
		s.invites.EXPECT().Create(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, inv *domain.Invite) error {
				stored = inv

				return nil
			}),
		s.invites.EXPECT().DeleteStale(gomock.Any(), gomock.Any()).Return(nil),
	)

	tok, err := s.mgr.GenerateInvite(s.T().Context(), s.adminID, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	})
	s.Require().NoError(err)
	s.Require().NotNil(stored)
	s.NotEmpty(tok)
	s.Equal(domain.HashInviteToken(tok), stored.TokenHash, "the store keeps only the token hash")
	s.NotEqual(tok, stored.TokenHash)
	s.Equal(s.adminID, stored.CreatedBy)
	s.Nil(stored.UsedAt)
	s.WithinDuration(before.Add(inviteTTL), stored.ExpiresAt, time.Minute)
}

// TestGenerateInvite_DeleteStaleFails_ReturnsToken pins that the cleanup is fail-soft: the
// invite is stored, so a failed cleanup must not hide the token from the admin.
func (s *ManagerSuite) TestGenerateInvite_DeleteStaleFails_ReturnsToken() {
	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.invites.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.invites.EXPECT().DeleteStale(gomock.Any(), gomock.Any()).Return(errors.New("db down"))

	tok, err := s.mgr.GenerateInvite(s.T().Context(), s.adminID, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	})
	s.Require().NoError(err)
	s.NotEmpty(tok)
}

func (s *ManagerSuite) TestGenerateInvite_AdminLookupFails_WrapsError() {
	boom := errors.New("boom")
	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(nil, boom)

	_, err := s.mgr.GenerateInvite(s.T().Context(), s.adminID, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	})
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

func (s *ManagerSuite) TestGenerateInvite_IssueFails_WrapsError() {
	boom := errors.New("boom")

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.invites.EXPECT().Create(gomock.Any(), gomock.Any()).Return(boom)

	_, err := s.mgr.GenerateInvite(s.T().Context(), s.adminID, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	})
	s.Require().Error(err)
	s.ErrorIs(err, boom)
}

// ── AcceptInvite ──.

func (s *ManagerSuite) TestAcceptInvite_WeakPassword_ReturnsErrWeakPassword() {
	_, err := s.mgr.AcceptInvite(s.T().Context(), "tok", "newuser", "weak")
	s.ErrorIs(err, domain.ErrWeakPassword)
}

func (s *ManagerSuite) TestAcceptInvite_InvalidToken_ReturnsErrInviteExpired() {
	s.users.EXPECT().GetByUsername(gomock.Any(), "newuser").Return(nil, domain.ErrNotFound)
	s.invites.EXPECT().
		RedeemInvite(gomock.Any(), domain.HashInviteToken("expired-tok"), "newuser", gomock.Any()).
		Return(domain.ErrInviteExpired)

	_, err := s.mgr.AcceptInvite(s.T().Context(), "expired-tok", "newuser", testPassword)
	s.ErrorIs(err, domain.ErrInviteExpired)
}

func (s *ManagerSuite) TestAcceptInvite_ValidToken_CreatesUserAndReturnsQR() {
	s.users.EXPECT().GetByUsername(gomock.Any(), "newuser").Return(nil, domain.ErrNotFound)
	s.invites.EXPECT().
		RedeemInvite(gomock.Any(), domain.HashInviteToken("good-tok"), "newuser", gomock.Any()).
		Return(nil)

	enr, err := s.mgr.AcceptInvite(s.T().Context(), "good-tok", "newuser", testPassword)
	s.Require().NoError(err)
	s.Contains(enr.QRCode, "data:image/png;base64,")
	s.Contains(enr.TOTPURI, "otpauth://totp/")

	// The URI must be a parseable otpauth key — CLI consumers rely on that.
	_, parseErr := otp.NewKeyFromURL(enr.TOTPURI)
	s.Require().NoError(parseErr)
}

func (s *ManagerSuite) TestAcceptInvite_RaceLoser_ReturnsErrUserExistsAndKeepsInvite() {
	// A username race lost inside the transaction: RedeemInvite rolls back, so the
	// invite is not burned and the caller sees ErrUserExists.
	s.users.EXPECT().GetByUsername(gomock.Any(), "newuser").Return(nil, domain.ErrNotFound)
	s.invites.EXPECT().
		RedeemInvite(gomock.Any(), domain.HashInviteToken("good-tok"), "newuser", gomock.Any()).
		Return(domain.ErrUserExists)

	_, err := s.mgr.AcceptInvite(s.T().Context(), "good-tok", "newuser", testPassword)
	s.ErrorIs(err, domain.ErrUserExists)
}

func (s *ManagerSuite) TestAcceptInvite_DuplicateUsername_ReturnsErrUserExistsWithoutBurningInvite() {
	// GetByUsername finds existing user → ErrUserExists returned before RedeemInvite is called.
	s.users.EXPECT().
		GetByUsername(gomock.Any(), "existing").
		Return(&domain.User{
			ID: uuid.New(), Username: "existing",
			CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
			KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
		}, nil)
	// RedeemInvite must NOT be called.

	_, err := s.mgr.AcceptInvite(s.T().Context(), "tok", "existing", testPassword)
	s.ErrorIs(err, domain.ErrUserExists)
}

// ── AcceptFirstAdmin ──.

func (s *ManagerSuite) TestAcceptFirstAdmin_WeakPassword_ReturnsErrWeakPassword() {
	_, err := s.mgr.AcceptFirstAdmin(s.T().Context(), "admin", "tooshort")
	s.ErrorIs(err, domain.ErrWeakPassword)
}

func (s *ManagerSuite) TestAcceptFirstAdmin_NonEmptyDB_ReturnsErrForbidden() {
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{{
		ID:        uuid.New(),
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}}, nil)

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

	enr, err := s.mgr.AcceptFirstAdmin(s.T().Context(), "firstadmin", testPassword)
	s.Require().NoError(err)
	s.Contains(enr.QRCode, "data:image/png;base64,")
	s.Contains(enr.TOTPURI, "otpauth://totp/")
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

// ── ChangePassword ──.

func (s *ManagerSuite) TestChangePassword_WrongOldPassword_ReturnsErrInvalidPassword() {
	usr, _ := s.testUser()
	sess := &domain.Session{
		SessionHash: "sess", UserID: s.userID,
		CreatedAt: time.Time{}, ExpiresAt: time.Time{}, UserAgent: "", IP: "",
	}

	s.sessions.EXPECT().Get(gomock.Any(), hashToken("sess")).Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)

	_, err := s.mgr.ChangePassword(s.T().Context(), "sess", "WrongP@ss12!", "NewP@ssw0rd!", "000000")
	s.ErrorIs(err, domain.ErrInvalidPassword)
}

func (s *ManagerSuite) TestChangePassword_InvalidTOTP_ReturnsErrInvalidTOTP() {
	usr, _ := s.testUser()
	sess := &domain.Session{
		SessionHash: "sess", UserID: s.userID,
		CreatedAt: time.Time{}, ExpiresAt: time.Time{}, UserAgent: "", IP: "",
	}

	s.sessions.EXPECT().Get(gomock.Any(), hashToken("sess")).Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)

	_, err := s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "NewP@ssw0rd!", "000000")
	s.ErrorIs(err, domain.ErrInvalidTOTP)
}

func (s *ManagerSuite) TestChangePassword_WeakNewPassword_ReturnsErrWeakPassword() {
	usr, rawSecret := s.testUser()
	sess := &domain.Session{
		SessionHash: "sess", UserID: s.userID,
		CreatedAt: time.Time{}, ExpiresAt: time.Time{}, UserAgent: "", IP: "",
	}

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	s.sessions.EXPECT().Get(gomock.Any(), hashToken("sess")).Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)

	_, err = s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "weak", code)
	s.ErrorIs(err, domain.ErrWeakPassword)
}

func (s *ManagerSuite) TestChangePassword_Success_UpdatesPasswordAndRotatesSession() {
	usr, rawSecret := s.testUser()
	sess := &domain.Session{
		SessionHash: "sess", UserID: s.userID, UserAgent: "ua", IP: "1.2.3.4",
		CreatedAt: time.Time{}, ExpiresAt: time.Time{},
	}

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	var stored *domain.Session

	s.sessions.EXPECT().Get(gomock.Any(), hashToken("sess")).Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	// One store call does all writes (Finding 5): no separate session create or revocation.
	s.users.EXPECT().ChangePassword(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, newSess *domain.Session, hash string, salt []byte, sealed string) error {
			stored = newSess

			s.NotEqual(usr.PasswordHash, hash)
			s.NotEqual(usr.KDFSalt, salt)
			s.NotEqual(usr.TOTPSecret, sealed)

			return nil
		})

	newSessID, err := s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "NewP@ssw0rd!", code)
	s.Require().NoError(err)
	s.NotEmpty(newSessID)
	s.NotEqual("sess", newSessID)
	s.Require().NotNil(stored)
	s.Equal(hashToken(newSessID), stored.SessionHash, "only the hash of the new session ID is stored")
	s.Equal(s.userID, stored.UserID)
	s.Equal("ua", stored.UserAgent)
	s.Equal("1.2.3.4", stored.IP)
	s.True(stored.ExpiresAt.After(stored.CreatedAt))
}

// TestChangePassword_StoreFailure_ReturnsNoSession covers Finding 5: when the one atomic store
// call fails, nothing changed, so ChangePassword returns no session ID and makes no other write.
func (s *ManagerSuite) TestChangePassword_StoreFailure_ReturnsNoSession() {
	usr, rawSecret := s.testUser()
	sess := &domain.Session{
		SessionHash: "sess", UserID: s.userID, UserAgent: "ua", IP: "1.2.3.4",
		CreatedAt: time.Time{}, ExpiresAt: time.Time{},
	}

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	storeErr := errors.New("db down")

	s.sessions.EXPECT().Get(gomock.Any(), hashToken("sess")).Return(sess, nil)
	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.users.EXPECT().ChangePassword(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(storeErr)

	newSessID, err := s.mgr.ChangePassword(s.T().Context(), "sess", testPassword, "NewP@ssw0rd!", code)
	s.Require().ErrorIs(err, storeErr)
	s.Empty(newSessID)
}

// TestLogin_PasswordChangedAfterCheck_DeletesSession covers audit finding R6: a login that
// verified the old password while a password change completed must not keep its new session.
func (s *ManagerSuite) TestLogin_PasswordChangedAfterCheck_DeletesSession() {
	usr, rawSecret := s.testUser()
	changed := *usr
	changed.PasswordHash = "hash-of-the-new-password"

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	var created string

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, newSess *domain.Session) error {
			created = newSess.SessionHash

			return nil
		})
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(&changed, nil)
	s.sessions.EXPECT().Delete(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, sessionHash string) error {
			s.Equal(created, sessionHash, "the session of the stale login must be deleted")

			return nil
		})

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "")
	s.Require().ErrorIs(err, domain.ErrInvalidTOTP)
	s.Empty(sessID)
}

// TestLogin_StaleReseal_SkipsWrite covers the reseal half of R6: when the compare-and-set finds
// another seal, the login still succeeds and nothing else is written.
func (s *ManagerSuite) TestLogin_StaleReseal_SkipsWrite() {
	usr, rawSecret := s.legacyTestUser()

	totpCode, codeErr := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(codeErr)

	s.users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(usr, nil)
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.users.EXPECT().UpdateTOTPSecret(gomock.Any(), s.userID, usr.TOTPSecret, gomock.Any()).Return(false, nil)
	s.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.sessions.EXPECT().DeleteExpired(gomock.Any(), gomock.Any()).Return(nil)
	s.users.EXPECT().UpdateLastLogin(gomock.Any(), s.userID, gomock.Any()).Return(nil)

	sessID, err := s.mgr.Login(s.T().Context(), "alice", testPassword, totpCode, "UA", "127.0.0.1", "")
	s.Require().NoError(err)
	s.NotEmpty(sessID)
}

// ── VerifyStepUp / GenerateInvite step-up (M-6) ──.

func (s *ManagerSuite) TestVerifyStepUp() {
	usr, rawSecret := s.testUser()

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(usr, nil).Times(3)

	s.Require().ErrorIs(s.mgr.VerifyStepUp(s.T().Context(), s.userID, StepUp{Password: "WrongP@ss12!", TOTPCode: code}),
		domain.ErrStepUpFailed, "wrong password")
	s.Require().ErrorIs(s.mgr.VerifyStepUp(s.T().Context(), s.userID, StepUp{Password: testPassword, TOTPCode: "000000"}),
		domain.ErrStepUpFailed, "wrong code")

	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.Require().NoError(s.mgr.VerifyStepUp(s.T().Context(), s.userID, StepUp{Password: testPassword, TOTPCode: code}))
}

func (s *ManagerSuite) TestGenerateInvite_Bearer_RequiresStepUp() {
	admin, rawSecret := s.testUser()
	admin.IsAdmin = true

	s.users.EXPECT().GetByID(gomock.Any(), s.userID).Return(admin, nil).AnyTimes()

	_, err := s.mgr.GenerateInvite(s.T().Context(), s.userID, Issuer{
		APITokenHash: "parent",
		StepUp:       StepUp{Password: "", TOTPCode: ""},
	})
	s.Require().ErrorIs(err, domain.ErrStepUpRequired, "a bearer without step-up mints no invite")

	code, err := totp.GenerateCode(rawSecret, time.Now())
	s.Require().NoError(err)

	s.users.EXPECT().UpdateTOTPCounter(gomock.Any(), s.userID, gomock.Any()).Return(true, nil)
	s.invites.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.invites.EXPECT().DeleteStale(gomock.Any(), gomock.Any()).Return(nil)

	tok, err := s.mgr.GenerateInvite(s.T().Context(), s.userID,
		Issuer{APITokenHash: "parent", StepUp: StepUp{Password: testPassword, TOTPCode: code}})
	s.Require().NoError(err)
	s.NotEmpty(tok)
}

// ── IsEmpty ──.

func (s *ManagerSuite) TestIsEmpty_NoUsers_ReturnsTrue() {
	s.users.EXPECT().List(gomock.Any()).Return(nil, nil)

	empty, err := s.mgr.IsEmpty(s.T().Context())
	s.Require().NoError(err)
	s.True(empty)
}

func (s *ManagerSuite) TestIsEmpty_HasUsers_ReturnsFalse() {
	s.users.EXPECT().List(gomock.Any()).Return([]*domain.User{{
		ID:        uuid.New(),
		CreatedAt: time.Time{}, LastLoginAt: nil, Username: "", TOTPSecret: "",
		PasswordHash: "", KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}}, nil)

	empty, err := s.mgr.IsEmpty(s.T().Context())
	s.Require().NoError(err)
	s.False(empty)
}

// ── ListUsers ──.

func (s *ManagerSuite) TestListUsers_Admin_ReturnsUsers() {
	users := []*domain.User{s.adminUser(), {
		ID: uuid.New(), Username: "bob",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}}

	s.users.EXPECT().GetByID(gomock.Any(), s.adminID).Return(s.adminUser(), nil)
	s.users.EXPECT().List(gomock.Any()).Return(users, nil)

	got, err := s.mgr.ListUsers(s.T().Context(), s.adminID)
	s.Require().NoError(err)
	s.Equal(users, got)
}

func (s *ManagerSuite) TestListUsers_NonAdmin_ReturnsErrForbidden() {
	caller := uuid.New()
	nonAdmin := &domain.User{
		ID: caller, IsAdmin: false, CreatedAt: time.Time{},
		LastLoginAt: nil, Username: "", TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
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

func TestAuthManager(t *testing.T) {
	suite.Run(t, new(ManagerSuite))
}

func (s *ManagerSuite) TestOnboardingInvalidUsernameNeverTouchesStorage() {
	for _, username := range []string{"", "  ", "a/b", "a&secret=x", strings.Repeat("a", 65)} {
		_, err := s.mgr.AcceptFirstAdmin(s.T().Context(), username, testPassword)
		s.Require().ErrorIs(err, domain.ErrInvalidUsername)
		_, err = s.mgr.AcceptInvite(s.T().Context(), "unused-invite", username, testPassword)
		s.Require().ErrorIs(err, domain.ErrInvalidUsername)
	}
}
