package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// testLinkSecret signs CLI login links in these tests. HMAC accepts any non-empty key.
const testLinkSecret = "test-link-secret-padmark-32bytes!!"

type APITokenSuite struct {
	suite.Suite

	ctrl      *gomock.Controller
	users     *MockUserStore
	apiTokens *MockAPITokenStore
	mgr       *Manager
}

func TestAPITokenSuite(t *testing.T) {
	suite.Run(t, new(APITokenSuite))
}

func (s *APITokenSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.users = NewMockUserStore(s.ctrl)
	s.apiTokens = NewMockAPITokenStore(s.ctrl)
	s.mgr = newAuthManagerWithTokens(s, s.users, s.apiTokens, []byte(testLinkSecret))
}

func (s *APITokenSuite) TearDownTest() {
	s.ctrl.Finish()
}

// newAuthManagerWithTokens wires a Manager with real crypto deps (mirroring ManagerSuite) and
// the API-token flow enabled. Keeps SetupTest free of the NewManager argument soup.
func newAuthManagerWithTokens(
	s *APITokenSuite,
	users *MockUserStore,
	apiTokens *MockAPITokenStore,
	linkSecret []byte,
) *Manager {
	return NewManager(
		users,
		NewMockInviteStore(s.ctrl),
		NewMockSessionStore(s.ctrl),
		crypto.New(),
		crypto.NewPasswordHasher(crypto.DefaultArgon2Params()),
		crypto.NewKDF(),
		crypto.NewTOTP(),
		discardLog,
		"padmark",
		0,
	).WithAPITokens(apiTokens, linkSecret)
}

// disabledMgr returns a Manager whose API-token flow is not enabled; used to assert
// domain.ErrFeatureNotSupported on every public entry point.
func (s *APITokenSuite) disabledMgr() *Manager {
	return NewManager(
		s.users,
		NewMockInviteStore(s.ctrl),
		NewMockSessionStore(s.ctrl),
		crypto.New(),
		crypto.NewPasswordHasher(crypto.DefaultArgon2Params()),
		crypto.NewKDF(),
		crypto.NewTOTP(),
		discardLog,
		"padmark",
		0,
	)
}

// ── helpers ──

func (s *APITokenSuite) adminUser() *domain.User {
	return &domain.User{ID: "admin-uuid", Username: "admin", IsAdmin: true}
}

func (s *APITokenSuite) regularUser() *domain.User {
	return &domain.User{ID: "user-uuid", Username: "alice", IsAdmin: false}
}

// hashToken mirrors the usecase's token hashing so tests can wire GetByHash expectations.
func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ── CreateLoginLink ──

func (s *APITokenSuite) TestCreateLoginLink_Disabled_ReturnsErrFeatureNotSupported() {
	_, _, err := s.disabledMgr().CreateLoginLink(s.T().Context())
	s.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (s *APITokenSuite) TestCreateLoginLink_ReturnsSignedTokenAndTTL() {
	token, ttl, err := s.mgr.CreateLoginLink(s.T().Context())
	s.Require().NoError(err)
	s.NotEmpty(token)
	s.Equal(loginLinkTTL, ttl)
	s.Len(strings.Split(token, "."), 3, "token must be payload.nonce.sig")
}

// ── ActivateAPIToken ──

func (s *APITokenSuite) TestActivateAPIToken_Disabled_ReturnsErrFeatureNotSupported() {
	_, err := s.disabledMgr().ActivateAPIToken(s.T().Context(), "tok", "user-1")
	s.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (s *APITokenSuite) TestActivateAPIToken_GarbageToken_ReturnsErrLoginLinkInvalid() {
	_, err := s.mgr.ActivateAPIToken(s.T().Context(), "not-a-signed-token", "user-1")
	s.ErrorIs(err, domain.ErrLoginLinkInvalid)
}

func (s *APITokenSuite) TestActivateAPIToken_TamperedSignature_ReturnsErrLoginLinkInvalid() {
	ctx := s.T().Context()

	token, _, err := s.mgr.CreateLoginLink(ctx)
	s.Require().NoError(err)

	// Append a valid base64url char to the signature: the decoded bytes no longer match the HMAC.
	tampered := token + "A"

	_, err = s.mgr.ActivateAPIToken(ctx, tampered, "user-1")
	s.ErrorIs(err, domain.ErrLoginLinkInvalid)
}

func (s *APITokenSuite) TestActivateAPIToken_ExpiredToken_ReturnsErrLoginLinkExpired() {
	ctx := s.T().Context()

	expired, err := s.mgr.signLoginLink(time.Now().Add(-time.Minute))
	s.Require().NoError(err)

	_, err = s.mgr.ActivateAPIToken(ctx, expired, "user-1")
	s.ErrorIs(err, domain.ErrLoginLinkExpired)
}

func (s *APITokenSuite) TestActivateAPIToken_WrongSecret_ReturnsErrLoginLinkInvalid() {
	ctx := s.T().Context()

	// Sign with a different secret than the Manager is configured with.
	other := NewManager(
		s.users,
		NewMockInviteStore(s.ctrl),
		NewMockSessionStore(s.ctrl),
		crypto.New(),
		crypto.NewPasswordHasher(crypto.DefaultArgon2Params()),
		crypto.NewKDF(),
		crypto.NewTOTP(),
		discardLog,
		"padmark",
		0,
	).WithAPITokens(s.apiTokens, []byte("a-different-secret-altogether"))
	token, _, err := other.CreateLoginLink(ctx)
	s.Require().NoError(err)

	_, err = s.mgr.ActivateAPIToken(ctx, token, "user-1")
	s.ErrorIs(err, domain.ErrLoginLinkInvalid)
}

func (s *APITokenSuite) TestActivateAPIToken_UserNotFound_ReturnsErrNotFound() {
	ctx := s.T().Context()

	token, _, err := s.mgr.CreateLoginLink(ctx)
	s.Require().NoError(err)

	s.users.EXPECT().GetByID(gomock.Any(), "ghost").Return(nil, domain.ErrNotFound)

	_, err = s.mgr.ActivateAPIToken(ctx, token, "ghost")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *APITokenSuite) TestActivateAPIToken_StoreCreateFails_WrapsError() {
	ctx := s.T().Context()

	token, _, err := s.mgr.CreateLoginLink(ctx)
	s.Require().NoError(err)

	usr := s.adminUser()
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	s.apiTokens.EXPECT().Create(gomock.Any(), gomock.Any()).Return(errors.New("db down"))

	_, err = s.mgr.ActivateAPIToken(ctx, token, usr.ID)
	s.ErrorContains(err, "create api token")
}

func (s *APITokenSuite) TestActivateAPIToken_Success_ReturnsPlainKeyAndPersistsHash() {
	ctx := s.T().Context()

	token, _, err := s.mgr.CreateLoginLink(ctx)
	s.Require().NoError(err)

	usr := s.adminUser()
	var stored *domain.APIToken
	s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	s.apiTokens.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, tk *domain.APIToken) error {
			stored = tk
			s.Equal(usr.ID, tk.UserID)
			s.NotEmpty(tk.ID)
			s.NotEmpty(tk.TokenHash)
			s.False(tk.CreatedAt.IsZero())
			return nil
		})

	plain, err := s.mgr.ActivateAPIToken(ctx, token, usr.ID)
	s.Require().NoError(err)
	s.NotEmpty(plain)

	// The stored hash is the SHA-256 of the plain key, never the plain key itself.
	s.Equal(hashToken(plain), stored.TokenHash)

	// The plain key is 32 random bytes, base64url-encoded.
	raw, decErr := base64.RawURLEncoding.DecodeString(plain)
	s.Require().NoError(decErr)
	s.Len(raw, apiTokenBytes)
}

// ── ResolveAPIToken ──

func (s *APITokenSuite) TestResolveAPIToken_Disabled_ReturnsErrFeatureNotSupported() {
	_, err := s.disabledMgr().ResolveAPIToken(s.T().Context(), "plain")
	s.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (s *APITokenSuite) TestResolveAPIToken_UnknownHash_ReturnsErrNotFound() {
	s.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("bogus")).Return(nil, domain.ErrNotFound)

	_, err := s.mgr.ResolveAPIToken(s.T().Context(), "bogus")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *APITokenSuite) TestResolveAPIToken_ExpiredToken_ReturnsErrNotFound() {
	exp := time.Now().Add(-time.Minute)
	tok := &domain.APIToken{
		ID:        "tok-1",
		UserID:    "user-uuid",
		TokenHash: hashToken("plain"),
		CreatedAt: time.Now(),
		ExpiresAt: &exp,
	}
	s.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil)

	_, err := s.mgr.ResolveAPIToken(s.T().Context(), "plain")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *APITokenSuite) TestResolveAPIToken_UpdateLastUsedFails_StillReturnsUser() {
	ctx := s.T().Context()
	usr := s.regularUser()
	tok := &domain.APIToken{ID: "tok-1", UserID: usr.ID, TokenHash: hashToken("plain"), CreatedAt: time.Now()}

	gomock.InOrder(
		s.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil),
		s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil),
		s.apiTokens.EXPECT().UpdateLastUsed(gomock.Any(), "tok-1", gomock.Any()).Return(errors.New("boom")),
	)

	resolved, err := s.mgr.ResolveAPIToken(ctx, "plain")
	s.Require().NoError(err)
	s.Equal(usr.ID, resolved.ID)
}

func (s *APITokenSuite) TestResolveAPIToken_Success_ReturnsUserAndRecordsLastUsed() {
	ctx := s.T().Context()
	usr := s.regularUser()
	tok := &domain.APIToken{ID: "tok-1", UserID: usr.ID, TokenHash: hashToken("plain"), CreatedAt: time.Now()}

	gomock.InOrder(
		s.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil),
		s.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil),
		s.apiTokens.EXPECT().UpdateLastUsed(gomock.Any(), "tok-1", gomock.Any()).Return(nil),
	)

	resolved, err := s.mgr.ResolveAPIToken(ctx, "plain")
	s.Require().NoError(err)
	s.Equal(usr.ID, resolved.ID)
	s.True(usr.IsAdmin == resolved.IsAdmin)
}

// ── ListAPITokens ──

func (s *APITokenSuite) TestListAPITokens_Disabled_ReturnsErrFeatureNotSupported() {
	_, err := s.disabledMgr().ListAPITokens(s.T().Context(), "admin-uuid")
	s.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (s *APITokenSuite) TestListAPITokens_NonAdmin_ReturnsErrForbidden() {
	nonAdmin := s.regularUser()
	s.users.EXPECT().GetByID(gomock.Any(), nonAdmin.ID).Return(nonAdmin, nil)

	_, err := s.mgr.ListAPITokens(s.T().Context(), nonAdmin.ID)
	s.ErrorIs(err, domain.ErrForbidden)
}

func (s *APITokenSuite) TestListAPITokens_Admin_ReturnsTokensWithUsernames() {
	ctx := s.T().Context()
	admin := s.adminUser()
	tokens := []*domain.APIToken{
		{ID: "tok-1", UserID: "user-uuid", CreatedAt: time.Now()},
		{ID: "tok-2", UserID: "ghost-uuid", CreatedAt: time.Now()},
	}
	users := []*domain.User{
		{ID: "user-uuid", Username: "alice"},
	}

	gomock.InOrder(
		s.users.EXPECT().GetByID(gomock.Any(), admin.ID).Return(admin, nil),
		s.apiTokens.EXPECT().List(gomock.Any()).Return(tokens, nil),
		s.users.EXPECT().List(gomock.Any()).Return(users, nil),
	)

	infos, err := s.mgr.ListAPITokens(ctx, admin.ID)
	s.Require().NoError(err)
	s.Require().Len(infos, 2)
	s.Equal("tok-1", infos[0].ID)
	s.Equal("alice", infos[0].Username)
	s.Equal("tok-2", infos[1].ID)
	s.Empty(infos[1].Username, "unknown owner resolves to empty username, not an error")
}

// ── RevokeAPIToken ──

func (s *APITokenSuite) TestRevokeAPIToken_Disabled_ReturnsErrFeatureNotSupported() {
	err := s.disabledMgr().RevokeAPIToken(s.T().Context(), "admin-uuid", "tok-1")
	s.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (s *APITokenSuite) TestRevokeAPIToken_NonAdmin_ReturnsErrForbidden() {
	nonAdmin := s.regularUser()
	s.users.EXPECT().GetByID(gomock.Any(), nonAdmin.ID).Return(nonAdmin, nil)

	err := s.mgr.RevokeAPIToken(s.T().Context(), nonAdmin.ID, "tok-1")
	s.ErrorIs(err, domain.ErrForbidden)
}

func (s *APITokenSuite) TestRevokeAPIToken_Admin_RevokesByID() {
	ctx := s.T().Context()
	admin := s.adminUser()
	s.users.EXPECT().GetByID(gomock.Any(), admin.ID).Return(admin, nil)
	s.apiTokens.EXPECT().RevokeByID(gomock.Any(), "tok-1").Return(nil)

	err := s.mgr.RevokeAPIToken(ctx, admin.ID, "tok-1")
	s.NoError(err)
}

func (s *APITokenSuite) TestRevokeAPIToken_StoreError_WrapsError() {
	ctx := s.T().Context()
	admin := s.adminUser()
	s.users.EXPECT().GetByID(gomock.Any(), admin.ID).Return(admin, nil)
	s.apiTokens.EXPECT().RevokeByID(gomock.Any(), "tok-1").Return(errors.New("db down"))

	err := s.mgr.RevokeAPIToken(ctx, admin.ID, "tok-1")
	s.ErrorContains(err, "revoke api token")
}
