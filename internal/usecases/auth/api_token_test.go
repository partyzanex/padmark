package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
)

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

func (suite *APITokenSuite) SetupTest() {
	suite.ctrl = gomock.NewController(suite.T())
	suite.users = NewMockUserStore(suite.ctrl)
	suite.apiTokens = NewMockAPITokenStore(suite.ctrl)
	suite.mgr = newAuthManagerWithTokens(suite, suite.users, suite.apiTokens)
}

func (suite *APITokenSuite) TearDownTest() {
	suite.ctrl.Finish()
}

// newAuthManagerWithTokens wires a Manager with real crypto deps (mirroring ManagerSuite) and the
// API-token flow enabled. Keeps SetupTest free of the NewManager argument soup.
func newAuthManagerWithTokens(
	suite *APITokenSuite,
	users *MockUserStore,
	apiTokens *MockAPITokenStore,
) *Manager {
	mgr := NewManager(
		users,
		NewMockInviteStore(suite.ctrl),
		NewMockSessionStore(suite.ctrl),
		crypto.New(),
		crypto.NewPasswordHasher(crypto.DefaultArgon2Params()),
		crypto.NewKDF(),
		crypto.NewTOTP(),
		discardLog,
		"padmark",
		0,
	)
	mgr.apiTokens = apiTokens

	return mgr
}

// disabledMgr returns a Manager whose API-token flow is not enabled; used to assert
// domain.ErrFeatureNotSupported on every public entry point.
func (suite *APITokenSuite) disabledMgr() *Manager {
	return NewManager(
		suite.users,
		NewMockInviteStore(suite.ctrl),
		NewMockSessionStore(suite.ctrl),
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

func (suite *APITokenSuite) adminUser() *domain.User {
	return &domain.User{ID: "admin-uuid", Username: "admin", IsAdmin: true}
}

func (suite *APITokenSuite) regularUser() *domain.User {
	return &domain.User{ID: "user-uuid", Username: "alice", IsAdmin: false}
}

// hashToken mirrors the usecase's token hashing so tests can wire GetByHash expectations.
func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ── CreateAPIToken ──

func (suite *APITokenSuite) TestCreateAPIToken_Disabled_ReturnsErrFeatureNotSupported() {
	_, err := suite.disabledMgr().CreateAPIToken(suite.T().Context(), "user-uuid")
	suite.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (suite *APITokenSuite) TestCreateAPIToken_UserNotFound_ReturnsErrNotFound() {
	ctx := suite.T().Context()
	suite.users.EXPECT().GetByID(gomock.Any(), "ghost").Return(nil, domain.ErrNotFound)

	_, err := suite.mgr.CreateAPIToken(ctx, "ghost")
	suite.ErrorIs(err, domain.ErrNotFound)
}

func (suite *APITokenSuite) TestCreateAPIToken_StoreCreateFails_WrapsError() {
	ctx := suite.T().Context()
	usr := suite.adminUser()
	suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	suite.apiTokens.EXPECT().Create(gomock.Any(), gomock.Any()).Return(errors.New("db down"))

	_, err := suite.mgr.CreateAPIToken(ctx, usr.ID)
	suite.ErrorContains(err, "create api token")
}

func (suite *APITokenSuite) TestCreateAPIToken_Success_ReturnsPlainKeyAndPersistsHash() {
	ctx := suite.T().Context()
	usr := suite.adminUser()

	var stored *domain.APIToken

	suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	suite.apiTokens.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, tk *domain.APIToken) error {
			stored = tk
			suite.Equal(usr.ID, tk.UserID)
			suite.NotEmpty(tk.TokenHash)
			suite.False(tk.CreatedAt.IsZero())

			return nil
		})

	plain, err := suite.mgr.CreateAPIToken(ctx, usr.ID)
	suite.Require().NoError(err)
	suite.NotEmpty(plain)

	// The stored hash is the SHA-256 of the plain key, never the plain key itself.
	suite.Equal(hashToken(plain), stored.TokenHash)

	// The plain key is 32 random bytes, base64url-encoded.
	raw, decErr := base64.RawURLEncoding.DecodeString(plain)
	suite.Require().NoError(decErr)
	suite.Len(raw, apiTokenBytes)
}

// ── ResolveAPIToken ──

func (suite *APITokenSuite) TestResolveAPIToken_Disabled_ReturnsErrFeatureNotSupported() {
	_, err := suite.disabledMgr().ResolveAPIToken(suite.T().Context(), "plain")
	suite.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (suite *APITokenSuite) TestResolveAPIToken_UnknownHash_ReturnsErrNotFound() {
	suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("bogus")).Return(nil, domain.ErrNotFound)

	_, err := suite.mgr.ResolveAPIToken(suite.T().Context(), "bogus")
	suite.ErrorIs(err, domain.ErrNotFound)
}

func (suite *APITokenSuite) TestResolveAPIToken_ExpiredToken_ReturnsErrNotFound() {
	exp := time.Now().Add(-time.Minute)
	tok := &domain.APIToken{
		UserID:    "user-uuid",
		TokenHash: hashToken("plain"),
		CreatedAt: time.Now(),
		ExpiresAt: &exp,
	}
	suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil)

	_, err := suite.mgr.ResolveAPIToken(suite.T().Context(), "plain")
	suite.ErrorIs(err, domain.ErrNotFound)
}

func (suite *APITokenSuite) TestResolveAPIToken_UpdateLastUsedFails_StillReturnsUser() {
	ctx := suite.T().Context()
	usr := suite.regularUser()
	tok := &domain.APIToken{UserID: usr.ID, TokenHash: hashToken("plain"), CreatedAt: time.Now()}

	gomock.InOrder(
		suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil),
		suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil),
		suite.apiTokens.EXPECT().UpdateLastUsed(gomock.Any(), hashToken("plain"), gomock.Any()).Return(errors.New("boom")),
	)

	resolved, err := suite.mgr.ResolveAPIToken(ctx, "plain")
	suite.Require().NoError(err)
	suite.Equal(usr, resolved)
}

func (suite *APITokenSuite) TestResolveAPIToken_Success_ReturnsUserAndRecordsLastUsed() {
	ctx := suite.T().Context()
	usr := suite.regularUser()
	tok := &domain.APIToken{UserID: usr.ID, TokenHash: hashToken("plain"), CreatedAt: time.Now()}

	gomock.InOrder(
		suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil),
		suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil),
		suite.apiTokens.EXPECT().UpdateLastUsed(gomock.Any(), hashToken("plain"), gomock.Any()).Return(nil),
	)

	resolved, err := suite.mgr.ResolveAPIToken(ctx, "plain")
	suite.Require().NoError(err)
	suite.Equal(usr, resolved)
}

// TestResolveAPIToken_UserLookupFails_WrapsError covers a resolved token whose owning user can
// no longer be loaded: the error is wrapped and last-used is never touched.
func (suite *APITokenSuite) TestResolveAPIToken_UserLookupFails_WrapsError() {
	ctx := suite.T().Context()
	tok := &domain.APIToken{UserID: "user-uuid", TokenHash: hashToken("plain"), CreatedAt: time.Now()}

	gomock.InOrder(
		suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil),
		suite.users.EXPECT().GetByID(gomock.Any(), "user-uuid").Return(nil, errors.New("db down")),
	)

	_, err := suite.mgr.ResolveAPIToken(ctx, "plain")
	suite.ErrorContains(err, "get token user")
}

// ── ListAPITokens ──

func (suite *APITokenSuite) TestListAPITokens_Disabled_ReturnsErrFeatureNotSupported() {
	_, err := suite.disabledMgr().ListAPITokens(suite.T().Context(), "admin-uuid")
	suite.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (suite *APITokenSuite) TestListAPITokens_NonAdmin_ReturnsErrForbidden() {
	nonAdmin := suite.regularUser()
	suite.users.EXPECT().GetByID(gomock.Any(), nonAdmin.ID).Return(nonAdmin, nil)

	_, err := suite.mgr.ListAPITokens(suite.T().Context(), nonAdmin.ID)
	suite.ErrorIs(err, domain.ErrForbidden)
}

func (suite *APITokenSuite) TestListAPITokens_Admin_ReturnsTokensWithUsernames() {
	ctx := suite.T().Context()
	admin := suite.adminUser()
	tokens := []*domain.APIToken{
		{TokenHash: "tok-1", UserID: "user-uuid", CreatedAt: time.Now()},
		{TokenHash: "tok-2", UserID: "ghost-uuid", CreatedAt: time.Now()},
	}
	users := []*domain.User{
		{ID: "user-uuid", Username: "alice"},
	}

	gomock.InOrder(
		suite.users.EXPECT().GetByID(gomock.Any(), admin.ID).Return(admin, nil),
		suite.apiTokens.EXPECT().List(gomock.Any()).Return(tokens, nil),
		suite.users.EXPECT().List(gomock.Any()).Return(users, nil),
	)

	infos, err := suite.mgr.ListAPITokens(ctx, admin.ID)
	suite.Require().NoError(err)
	suite.Require().Len(infos, 2)
	suite.Equal("tok-1", infos[0].ID)
	suite.Equal("alice", infos[0].Username)
	suite.Equal("tok-2", infos[1].ID)
	suite.Empty(infos[1].Username, "unknown owner resolves to empty username, not an error")
}

func (suite *APITokenSuite) TestListAPITokens_AdminLookupFails_WrapsError() {
	ctx := suite.T().Context()
	suite.users.EXPECT().GetByID(gomock.Any(), "admin-uuid").Return(nil, errors.New("db down"))

	_, err := suite.mgr.ListAPITokens(ctx, "admin-uuid")
	suite.ErrorContains(err, "get admin user")
}

func (suite *APITokenSuite) TestListAPITokens_StoreListFails_WrapsError() {
	ctx := suite.T().Context()
	admin := suite.adminUser()

	gomock.InOrder(
		suite.users.EXPECT().GetByID(gomock.Any(), admin.ID).Return(admin, nil),
		suite.apiTokens.EXPECT().List(gomock.Any()).Return(nil, errors.New("db down")),
	)

	_, err := suite.mgr.ListAPITokens(ctx, admin.ID)
	suite.ErrorContains(err, "list api tokens")
}

func (suite *APITokenSuite) TestListAPITokens_UsersListFails_WrapsError() {
	ctx := suite.T().Context()
	admin := suite.adminUser()

	gomock.InOrder(
		suite.users.EXPECT().GetByID(gomock.Any(), admin.ID).Return(admin, nil),
		suite.apiTokens.EXPECT().List(gomock.Any()).Return([]*domain.APIToken{}, nil),
		suite.users.EXPECT().List(gomock.Any()).Return(nil, errors.New("db down")),
	)

	_, err := suite.mgr.ListAPITokens(ctx, admin.ID)
	suite.ErrorContains(err, "list users")
}

// ── RevokeAPIToken ──

func (suite *APITokenSuite) TestRevokeAPIToken_Disabled_ReturnsErrFeatureNotSupported() {
	err := suite.disabledMgr().RevokeAPIToken(suite.T().Context(), "admin-uuid", "tok-1")
	suite.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (suite *APITokenSuite) TestRevokeAPIToken_NonAdmin_ReturnsErrForbidden() {
	nonAdmin := suite.regularUser()
	suite.users.EXPECT().GetByID(gomock.Any(), nonAdmin.ID).Return(nonAdmin, nil)

	err := suite.mgr.RevokeAPIToken(suite.T().Context(), nonAdmin.ID, "tok-1")
	suite.ErrorIs(err, domain.ErrForbidden)
}

func (suite *APITokenSuite) TestRevokeAPIToken_Admin_RevokesByHash() {
	ctx := suite.T().Context()
	admin := suite.adminUser()
	suite.users.EXPECT().GetByID(gomock.Any(), admin.ID).Return(admin, nil)
	suite.apiTokens.EXPECT().RevokeByHash(gomock.Any(), "tok-1").Return(nil)

	err := suite.mgr.RevokeAPIToken(ctx, admin.ID, "tok-1")
	suite.NoError(err)
}

func (suite *APITokenSuite) TestRevokeAPIToken_StoreError_WrapsError() {
	ctx := suite.T().Context()
	admin := suite.adminUser()
	suite.users.EXPECT().GetByID(gomock.Any(), admin.ID).Return(admin, nil)
	suite.apiTokens.EXPECT().RevokeByHash(gomock.Any(), "tok-1").Return(errors.New("db down"))

	err := suite.mgr.RevokeAPIToken(ctx, admin.ID, "tok-1")
	suite.ErrorContains(err, "revoke api token")
}

func (suite *APITokenSuite) TestRevokeAPIToken_AdminLookupFails_WrapsError() {
	ctx := suite.T().Context()
	suite.users.EXPECT().GetByID(gomock.Any(), "admin-uuid").Return(nil, errors.New("db down"))

	err := suite.mgr.RevokeAPIToken(ctx, "admin-uuid", "tok-1")
	suite.ErrorContains(err, "get admin user")
}
