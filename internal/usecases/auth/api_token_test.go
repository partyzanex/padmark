package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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
	mgr, err := NewManager(
		users,
		NewMockInviteStore(suite.ctrl),
		NewMockSessionStore(suite.ctrl),
		apiTokens,
		crypto.NewPasswordHasher(testArgon2Params),
		crypto.NewSealer(testArgon2Params),
		crypto.NewTOTP(),
		discardLog,
		"padmark",
		0,
	)
	suite.Require().NoError(err)

	return mgr
}

// disabledMgr returns a Manager whose API-token flow is not enabled; used to assert
// domain.ErrFeatureNotSupported on every public entry point.
func (suite *APITokenSuite) disabledMgr() *Manager {
	mgr, err := NewManager(
		suite.users,
		NewMockInviteStore(suite.ctrl),
		NewMockSessionStore(suite.ctrl),
		nil, // API-token flow disabled → methods return domain.ErrFeatureNotSupported
		crypto.NewPasswordHasher(testArgon2Params),
		crypto.NewSealer(testArgon2Params),
		crypto.NewTOTP(),
		discardLog,
		"padmark",
		0,
	)
	suite.Require().NoError(err)

	return mgr
}

// ── helpers ──

func (suite *APITokenSuite) adminUser() *domain.User {
	return &domain.User{ID: uuid.New(), Username: "admin", IsAdmin: true,
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
}

func (suite *APITokenSuite) regularUser() *domain.User {
	return &domain.User{ID: uuid.New(), Username: "alice", IsAdmin: false, CreatedAt: time.Time{},
		LastLoginAt: nil, TOTPSecret: "", PasswordHash: "", KDFSalt: nil,
		LastTOTPCounter: 0,
	}
}

// ── CreateAPIToken ──

func (suite *APITokenSuite) TestCreateAPIToken_Disabled_ReturnsErrFeatureNotSupported() {
	_, err := suite.disabledMgr().CreateAPIToken(suite.T().Context(), uuid.New(), Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	}, 0)
	suite.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (suite *APITokenSuite) TestCreateAPIToken_UserNotFound_ReturnsErrNotFound() {
	ctx := suite.T().Context()
	ghost := uuid.New()
	suite.users.EXPECT().GetByID(gomock.Any(), ghost).Return(nil, domain.ErrNotFound)

	_, err := suite.mgr.CreateAPIToken(ctx, ghost, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	}, 0)
	suite.ErrorIs(err, domain.ErrNotFound)
}

func (suite *APITokenSuite) TestCreateAPIToken_StoreFails_WrapsError() {
	ctx := suite.T().Context()
	usr := suite.adminUser()
	suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	suite.apiTokens.EXPECT().
		CreateIfUnderLimit(gomock.Any(), gomock.Any(), maxAPITokensPerUser).
		Return(false, errors.New("db down"))

	_, err := suite.mgr.CreateAPIToken(ctx, usr.ID, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	}, 0)
	suite.ErrorContains(err, "create api token")
}

func (suite *APITokenSuite) TestCreateAPIToken_LimitReached_ReturnsErrAPITokenLimit() {
	ctx := suite.T().Context()
	usr := suite.adminUser()
	suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	suite.apiTokens.EXPECT().
		CreateIfUnderLimit(gomock.Any(), gomock.Any(), maxAPITokensPerUser).
		Return(false, nil)

	_, err := suite.mgr.CreateAPIToken(ctx, usr.ID, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	}, 0)
	suite.ErrorIs(err, domain.ErrAPITokenLimit)
}

func (suite *APITokenSuite) TestCreateAPIToken_Success_ReturnsPlainKeyAndPersistsHash() {
	ctx := suite.T().Context()
	usr := suite.adminUser()

	var stored *domain.APIToken

	suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	suite.apiTokens.EXPECT().
		CreateIfUnderLimit(gomock.Any(), gomock.Any(), maxAPITokensPerUser).
		DoAndReturn(func(_ context.Context, tk *domain.APIToken, _ int) (bool, error) {
			stored = tk
			suite.Equal(usr.ID, tk.UserID)
			suite.NotEmpty(tk.TokenHash)
			suite.False(tk.CreatedAt.IsZero())

			return true, nil
		})

	plain, err := suite.mgr.CreateAPIToken(ctx, usr.ID, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	}, 0)
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
	_, _, err := suite.disabledMgr().ResolveAPIToken(suite.T().Context(), "plain")
	suite.ErrorIs(err, domain.ErrFeatureNotSupported)
}

func (suite *APITokenSuite) TestResolveAPIToken_UnknownHash_ReturnsErrNotFound() {
	suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("bogus")).Return(nil, domain.ErrNotFound)

	_, _, err := suite.mgr.ResolveAPIToken(suite.T().Context(), "bogus")
	suite.ErrorIs(err, domain.ErrNotFound)
}

func (suite *APITokenSuite) TestResolveAPIToken_ExpiredToken_ReturnsErrNotFound() {
	exp := time.Now().Add(-time.Minute)
	tok := &domain.APIToken{
		UserID:    uuid.New(),
		TokenHash: hashToken("plain"),
		CreatedAt: time.Now(),
		ExpiresAt: &exp,

		LastUsedAt: nil, IssuedBy: nil,
	}
	suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil)

	_, _, err := suite.mgr.ResolveAPIToken(suite.T().Context(), "plain")
	suite.ErrorIs(err, domain.ErrNotFound)
}

func (suite *APITokenSuite) TestResolveAPIToken_UpdateLastUsedFails_StillReturnsUser() {
	ctx := suite.T().Context()
	usr := suite.regularUser()
	tok := &domain.APIToken{UserID: usr.ID, TokenHash: hashToken("plain"), CreatedAt: time.Now(),
		ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
	}

	gomock.InOrder(
		suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil),
		suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil),
		suite.apiTokens.EXPECT().UpdateLastUsed(gomock.Any(), hashToken("plain"), gomock.Any()).Return(errors.New("boom")),
	)

	resolved, _, err := suite.mgr.ResolveAPIToken(ctx, "plain")
	suite.Require().NoError(err)
	suite.Equal(usr, resolved)
}

func (suite *APITokenSuite) TestResolveAPIToken_Success_ReturnsUserAndRecordsLastUsed() {
	ctx := suite.T().Context()
	usr := suite.regularUser()
	tok := &domain.APIToken{UserID: usr.ID, TokenHash: hashToken("plain"), CreatedAt: time.Now(),
		ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
	}

	gomock.InOrder(
		suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil),
		suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil),
		suite.apiTokens.EXPECT().UpdateLastUsed(gomock.Any(), hashToken("plain"), gomock.Any()).Return(nil),
	)

	resolved, tokenID, err := suite.mgr.ResolveAPIToken(ctx, "plain")
	suite.Require().NoError(err)
	suite.Equal(usr, resolved)
	suite.Equal(hashToken("plain"), tokenID, "the resolved key's public ID is its hash")
}

// TestResolveAPIToken_UserLookupFails_WrapsError covers a resolved token whose owning user can
// no longer be loaded: the error is wrapped and last-used is never touched.
func (suite *APITokenSuite) TestResolveAPIToken_UserLookupFails_WrapsError() {
	ctx := suite.T().Context()
	userID := uuid.New()
	tok := &domain.APIToken{UserID: userID, TokenHash: hashToken("plain"), CreatedAt: time.Now(),
		ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
	}

	gomock.InOrder(
		suite.apiTokens.EXPECT().GetByHash(gomock.Any(), hashToken("plain")).Return(tok, nil),
		suite.users.EXPECT().GetByID(gomock.Any(), userID).Return(nil, errors.New("db down")),
	)

	_, _, err := suite.mgr.ResolveAPIToken(ctx, "plain")
	suite.ErrorContains(err, "get token user")
}

// ── ListAPITokens ──

func (suite *APITokenSuite) TestListAPITokens_Disabled_ReturnsErrFeatureNotSupported() {
	_, err := suite.disabledMgr().ListAPITokens(suite.T().Context(), uuid.New())
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
	aliceID := uuid.New()
	tokens := []*domain.APIToken{
		{TokenHash: "tok-1", UserID: aliceID, CreatedAt: time.Now(),
			ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
		},
		{TokenHash: "tok-2", UserID: uuid.New(), CreatedAt: time.Now(),
			ExpiresAt: nil, LastUsedAt: nil, IssuedBy: nil,
		},
	}
	users := []*domain.User{
		{ID: aliceID, Username: "alice",
			CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
			KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
		},
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
	adminID := uuid.New()
	suite.users.EXPECT().GetByID(gomock.Any(), adminID).Return(nil, errors.New("db down"))

	_, err := suite.mgr.ListAPITokens(ctx, adminID)
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
	err := suite.disabledMgr().RevokeAPIToken(suite.T().Context(), uuid.New(), "tok-1")
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
	adminID := uuid.New()
	suite.users.EXPECT().GetByID(gomock.Any(), adminID).Return(nil, errors.New("db down"))

	err := suite.mgr.RevokeAPIToken(ctx, adminID, "tok-1")
	suite.ErrorContains(err, "get admin user")
}

// ── issuance rules (M-6): step-up for bearer issuers, lifetime, issued_by ──

// stepUpMgr wires an APITokenManager whose step-up verifier is a mock, so the issuance rule is
// tested on its own (SessionManager.VerifyStepUp has its own tests in ManagerSuite).
func (suite *APITokenSuite) stepUpMgr() (*APITokenManager, *MockStepUpVerifier) {
	verifier := NewMockStepUpVerifier(suite.ctrl)

	return NewAPITokenManager(suite.apiTokens, suite.users, verifier, discardLog), verifier
}

func (suite *APITokenSuite) TestCreateAPIToken_Bearer_WithoutStepUp_ReturnsErrStepUpRequired() {
	mgr, _ := suite.stepUpMgr()
	usr := suite.adminUser()
	suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil).Times(3)
	// No verifier call and no store call: nothing may be minted.

	for _, stepUp := range []StepUp{{
		Password: "", TOTPCode: "",
	}, {Password: "pw", TOTPCode: ""}, {Password: "", TOTPCode: "123456"}} {
		_, err := mgr.CreateAPIToken(suite.T().Context(), usr.ID, Issuer{APITokenHash: "parent", StepUp: stepUp}, 0)
		suite.Require().ErrorIs(err, domain.ErrStepUpRequired)
	}
}

func (suite *APITokenSuite) TestCreateAPIToken_Bearer_StepUpFails_NothingMinted() {
	mgr, verifier := suite.stepUpMgr()
	usr := suite.adminUser()
	stepUp := StepUp{Password: "pw", TOTPCode: "123456"}

	suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	verifier.EXPECT().VerifyStepUp(gomock.Any(), usr.ID, stepUp).Return(domain.ErrStepUpFailed)

	_, err := mgr.CreateAPIToken(suite.T().Context(), usr.ID, Issuer{APITokenHash: "parent", StepUp: stepUp}, 0)
	suite.Require().ErrorIs(err, domain.ErrStepUpFailed)
}

func (suite *APITokenSuite) TestCreateAPIToken_Bearer_ValidStepUp_RecordsIssuer() {
	mgr, verifier := suite.stepUpMgr()
	usr := suite.adminUser()
	stepUp := StepUp{Password: "pw", TOTPCode: "123456"}

	var stored *domain.APIToken

	suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	verifier.EXPECT().VerifyStepUp(gomock.Any(), usr.ID, stepUp).Return(nil)
	suite.apiTokens.EXPECT().CreateIfUnderLimit(gomock.Any(), gomock.Any(), maxAPITokensPerUser).
		DoAndReturn(func(_ context.Context, tk *domain.APIToken, _ int) (bool, error) {
			stored = tk

			return true, nil
		})

	_, err := mgr.CreateAPIToken(suite.T().Context(), usr.ID, Issuer{APITokenHash: "parent", StepUp: stepUp}, 0)
	suite.Require().NoError(err)
	suite.Require().NotNil(stored.IssuedBy)
	suite.Equal("parent", *stored.IssuedBy, "the new key must point at the bearer that minted it")
}

func (suite *APITokenSuite) TestCreateAPIToken_Session_NoStepUpAndNoIssuer() {
	mgr, _ := suite.stepUpMgr()
	usr := suite.adminUser()

	var stored *domain.APIToken

	suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
	suite.apiTokens.EXPECT().CreateIfUnderLimit(gomock.Any(), gomock.Any(), maxAPITokensPerUser).
		DoAndReturn(func(_ context.Context, tk *domain.APIToken, _ int) (bool, error) {
			stored = tk

			return true, nil
		})

	_, err := mgr.CreateAPIToken(suite.T().Context(), usr.ID, Issuer{
		APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
	}, 0)
	suite.Require().NoError(err)
	suite.Nil(stored.IssuedBy, "a browser session issues without step-up and without an issuer token")
}

func (suite *APITokenSuite) TestCreateAPIToken_Lifetime() {
	mgr, _ := suite.stepUpMgr()
	usr := suite.adminUser()

	for _, ttlDays := range []int{0, 7, MaxAPITokenTTLDays} {
		var stored *domain.APIToken

		suite.users.EXPECT().GetByID(gomock.Any(), usr.ID).Return(usr, nil)
		suite.apiTokens.EXPECT().CreateIfUnderLimit(gomock.Any(), gomock.Any(), maxAPITokensPerUser).
			DoAndReturn(func(_ context.Context, tk *domain.APIToken, _ int) (bool, error) {
				stored = tk

				return true, nil
			})

		_, err := mgr.CreateAPIToken(suite.T().Context(), usr.ID, Issuer{
			APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
		}, ttlDays)
		suite.Require().NoError(err)

		if ttlDays == 0 {
			suite.Nil(stored.ExpiresAt, "ttl 0 means the key never expires")

			continue
		}

		suite.Require().NotNil(stored.ExpiresAt)
		suite.WithinDuration(stored.CreatedAt.AddDate(0, 0, ttlDays), *stored.ExpiresAt, time.Second)
	}

	for _, ttlDays := range []int{-1, MaxAPITokenTTLDays + 1} {
		_, err := mgr.CreateAPIToken(suite.T().Context(), usr.ID, Issuer{
			APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""},
		}, ttlDays)
		suite.Require().ErrorIs(err, domain.ErrInvalidAPITokenTTL)
	}
}
