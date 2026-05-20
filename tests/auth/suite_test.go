//go:build integration

package integration

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/domain"
)

type userRepo interface {
	Create(ctx context.Context, u *domain.User) error
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	GetByID(ctx context.Context, id string) (*domain.User, error)
	List(ctx context.Context) ([]*domain.User, error)
	UpdateLastLogin(ctx context.Context, id string, t time.Time) error
	UpdatePassword(ctx context.Context, id, passwordHash string, kdfSalt []byte, totpSecret string) error
	Revoke(ctx context.Context, id string) error
}

type inviteRepo interface {
	Issue(ctx context.Context, createdByID string) (string, error)
	Consume(ctx context.Context, token, username string) (*domain.Invite, error)
}

type sessionRepo interface {
	Create(ctx context.Context, s *domain.Session) error
	Get(ctx context.Context, sessionID string) (*domain.Session, error)
	Delete(ctx context.Context, sessionID string) error
	DeleteExpired(ctx context.Context) error
}

// AuthSuite is the storage-agnostic auth integration suite.
// Embed it in a storage-specific suite that sets Users, Invites, Sessions in SetupTest.
type AuthSuite struct {
	suite.Suite

	Users    userRepo
	Invites  inviteRepo
	Sessions sessionRepo
}

// ── helpers ──

// testTOTPSecret is a well-known base32 test vector; not a real credential.
//
//nolint:gosec // G101: test fixture, not a production secret
const testTOTPSecret = "JBSWY3DPEHPK3PXP"

func (s *AuthSuite) newUser(username string) *domain.User {
	return &domain.User{
		ID:         uuid.New().String(),
		Username:   username,
		TOTPSecret: testTOTPSecret,
		IsAdmin:    false,
		CreatedAt:  time.Now().Truncate(time.Second),
	}
}

func (s *AuthSuite) newAdminUser(username string) *domain.User {
	usr := s.newUser(username)
	usr.IsAdmin = true

	return usr
}

// ── UserRepository ──

func (s *AuthSuite) TestUser_Create_GetByUsername() {
	ctx := s.T().Context()
	usr := s.newUser("alice")

	s.Require().NoError(s.Users.Create(ctx, usr))

	got, err := s.Users.GetByUsername(ctx, "alice")
	s.Require().NoError(err)
	s.Equal("alice", got.Username)
	s.Equal(usr.TOTPSecret, got.TOTPSecret)
}

func (s *AuthSuite) TestUser_Create_DuplicateUsername_ReturnsErrUserExists() {
	ctx := s.T().Context()

	s.Require().NoError(s.Users.Create(ctx, s.newUser("bob")))

	err := s.Users.Create(ctx, s.newUser("bob"))
	s.ErrorIs(err, domain.ErrUserExists)
}

func (s *AuthSuite) TestUser_GetByUsername_NotFound() {
	_, err := s.Users.GetByUsername(s.T().Context(), "nobody")
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *AuthSuite) TestUser_GetByID() {
	ctx := s.T().Context()
	usr := s.newUser("carol")
	s.Require().NoError(s.Users.Create(ctx, usr))

	got, err := s.Users.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Equal(usr.ID, got.ID)
}

func (s *AuthSuite) TestUser_GetByID_NotFound() {
	_, err := s.Users.GetByID(s.T().Context(), uuid.New().String())
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *AuthSuite) TestUser_List() {
	ctx := s.T().Context()
	s.Require().NoError(s.Users.Create(ctx, s.newUser("dave")))
	s.Require().NoError(s.Users.Create(ctx, s.newUser("eve")))

	users, err := s.Users.List(ctx)
	s.Require().NoError(err)
	s.GreaterOrEqual(len(users), 2)
}

func (s *AuthSuite) TestUser_UpdateLastLogin() {
	ctx := s.T().Context()
	usr := s.newUser("frank")
	s.Require().NoError(s.Users.Create(ctx, usr))

	loginAt := time.Now().Truncate(time.Second)
	s.Require().NoError(s.Users.UpdateLastLogin(ctx, usr.ID, loginAt))

	got, err := s.Users.GetByID(ctx, usr.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.LastLoginAt)
	s.Equal(loginAt.Unix(), got.LastLoginAt.Unix())
}

func (s *AuthSuite) TestUser_Revoke() {
	ctx := s.T().Context()
	usr := s.newUser("grace")
	s.Require().NoError(s.Users.Create(ctx, usr))

	s.Require().NoError(s.Users.Revoke(ctx, usr.ID))

	_, err := s.Users.GetByID(ctx, usr.ID)
	s.ErrorIs(err, domain.ErrNotFound)
}

// ── InviteRepository ──

func (s *AuthSuite) TestInvite_Issue_ReturnsToken() {
	ctx := s.T().Context()
	admin := s.newAdminUser("invite-admin")
	s.Require().NoError(s.Users.Create(ctx, admin))

	tok, err := s.Invites.Issue(ctx, admin.ID)
	s.Require().NoError(err)
	s.NotEmpty(tok)
}

func (s *AuthSuite) TestInvite_Issue_UniqueTokens() {
	ctx := s.T().Context()
	admin := s.newAdminUser("invite-admin2")
	s.Require().NoError(s.Users.Create(ctx, admin))

	tok1, err := s.Invites.Issue(ctx, admin.ID)
	s.Require().NoError(err)

	tok2, err := s.Invites.Issue(ctx, admin.ID)
	s.Require().NoError(err)

	s.NotEqual(tok1, tok2)
}

func (s *AuthSuite) TestInvite_Consume_OK() {
	ctx := s.T().Context()
	admin := s.newAdminUser("invite-admin3")
	s.Require().NoError(s.Users.Create(ctx, admin))

	tok, err := s.Invites.Issue(ctx, admin.ID)
	s.Require().NoError(err)

	invite, err := s.Invites.Consume(ctx, tok, "newuser")
	s.Require().NoError(err)
	s.Equal(tok, invite.Token)
	s.Equal("newuser", invite.UsedBy)
	s.NotNil(invite.UsedAt)
}

func (s *AuthSuite) TestInvite_Consume_AlreadyUsed() {
	ctx := s.T().Context()
	admin := s.newAdminUser("invite-admin4")
	s.Require().NoError(s.Users.Create(ctx, admin))

	tok, err := s.Invites.Issue(ctx, admin.ID)
	s.Require().NoError(err)

	_, err = s.Invites.Consume(ctx, tok, "first")
	s.Require().NoError(err)

	_, err = s.Invites.Consume(ctx, tok, "second")
	s.ErrorIs(err, domain.ErrInviteUsed)
}

func (s *AuthSuite) TestInvite_Consume_NotFound() {
	_, err := s.Invites.Consume(s.T().Context(), "no-such-token", "user")
	s.ErrorIs(err, domain.ErrNotFound)
}

// ── SessionRepository ──

func (s *AuthSuite) TestSession_Create_Get() {
	ctx := s.T().Context()
	usr := s.newUser("session-alice")
	s.Require().NoError(s.Users.Create(ctx, usr))

	sess := &domain.Session{
		SessionID: "sess-alice-1",
		UserID:    usr.ID,
		CreatedAt: time.Now().Truncate(time.Second),
		ExpiresAt: time.Now().Add(24 * time.Hour).Truncate(time.Second),
		UserAgent: "Mozilla/5.0",
		IP:        "127.0.0.1",
	}
	s.Require().NoError(s.Sessions.Create(ctx, sess))

	got, err := s.Sessions.Get(ctx, sess.SessionID)
	s.Require().NoError(err)
	s.Equal(sess.SessionID, got.SessionID)
	s.Equal(sess.UserID, got.UserID)
}

func (s *AuthSuite) TestSession_Get_Expired_ReturnsErrSessionExpired() {
	ctx := s.T().Context()
	usr := s.newUser("session-bob")
	s.Require().NoError(s.Users.Create(ctx, usr))

	past := time.Now().Add(-time.Minute)
	sess := &domain.Session{
		SessionID: "sess-bob-expired",
		UserID:    usr.ID,
		CreatedAt: past,
		ExpiresAt: past,
	}
	s.Require().NoError(s.Sessions.Create(ctx, sess))

	_, err := s.Sessions.Get(ctx, sess.SessionID)
	s.ErrorIs(err, domain.ErrSessionExpired)
}

func (s *AuthSuite) TestSession_Get_Unknown_ReturnsErrSessionExpired() {
	_, err := s.Sessions.Get(s.T().Context(), "nonexistent-session")
	s.ErrorIs(err, domain.ErrSessionExpired)
}

func (s *AuthSuite) TestSession_Delete() {
	ctx := s.T().Context()
	usr := s.newUser("session-carol")
	s.Require().NoError(s.Users.Create(ctx, usr))

	sess := &domain.Session{
		SessionID: "sess-carol-1",
		UserID:    usr.ID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	s.Require().NoError(s.Sessions.Create(ctx, sess))
	s.Require().NoError(s.Sessions.Delete(ctx, sess.SessionID))

	_, err := s.Sessions.Get(ctx, sess.SessionID)
	s.ErrorIs(err, domain.ErrSessionExpired)
}

func (s *AuthSuite) TestSession_DeleteExpired() {
	ctx := s.T().Context()
	usr := s.newUser("session-dave")
	s.Require().NoError(s.Users.Create(ctx, usr))

	past := time.Now().Add(-time.Minute)
	expired := &domain.Session{
		SessionID: "sess-dave-old",
		UserID:    usr.ID,
		CreatedAt: past,
		ExpiresAt: past,
	}
	active := &domain.Session{
		SessionID: "sess-dave-active",
		UserID:    usr.ID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	s.Require().NoError(s.Sessions.Create(ctx, expired))
	s.Require().NoError(s.Sessions.Create(ctx, active))

	s.Require().NoError(s.Sessions.DeleteExpired(ctx))

	_, err := s.Sessions.Get(ctx, active.SessionID)
	s.Require().NoError(err, "active session must survive DeleteExpired")
}
