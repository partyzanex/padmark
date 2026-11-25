package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

type UserAdminSuite struct {
	suite.Suite

	ctrl  *gomock.Controller
	users *MockUserAdminStore
	mgr   *UserAdminManager

	adminID  uuid.UUID
	targetID uuid.UUID
}

func TestUserAdminSuite(t *testing.T) {
	suite.Run(t, new(UserAdminSuite))
}

func (s *UserAdminSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.users = NewMockUserAdminStore(s.ctrl)
	s.mgr = NewUserAdminManager(s.users)
	s.adminID = uuid.New()
	s.targetID = uuid.New()
}

func (s *UserAdminSuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *UserAdminSuite) TestRevokeUser_SelfRevoke_ReturnsErrForbidden() {
	// The strict mock has no expectation: self-revoke is rejected before any store call.
	s.ErrorIs(s.mgr.RevokeUser(s.T().Context(), s.adminID, s.adminID), domain.ErrForbidden)
}

func (s *UserAdminSuite) TestRevokeUser_NonAdminCaller_ReturnsErrForbidden() {
	ctx := s.T().Context()
	s.users.EXPECT().GetByID(ctx, s.adminID).Return(newTestUser(s.adminID, false), nil)

	s.ErrorIs(s.mgr.RevokeUser(ctx, s.adminID, s.targetID), domain.ErrForbidden)
}

func (s *UserAdminSuite) TestRevokeUser_AdminLookupFails_WrapsError() {
	ctx := s.T().Context()
	boom := errors.New("boom")

	s.users.EXPECT().GetByID(ctx, s.adminID).Return(nil, boom)

	s.ErrorIs(s.mgr.RevokeUser(ctx, s.adminID, s.targetID), boom)
}

func (s *UserAdminSuite) TestRevokeUser_TargetLookupFails_WrapsError() {
	ctx := s.T().Context()
	boom := errors.New("boom")

	s.users.EXPECT().GetByID(ctx, s.adminID).Return(newTestUser(s.adminID, true), nil)
	s.users.EXPECT().GetByID(ctx, s.targetID).Return(nil, boom)

	s.ErrorIs(s.mgr.RevokeUser(ctx, s.adminID, s.targetID), boom)
}

// TestRevokeUser_AdminTarget_ReturnsErrForbidden pins Y5: the schema allows one admin, so an
// admin target is the last admin. The manager refuses it without a user list and without
// a Revoke call.
func (s *UserAdminSuite) TestRevokeUser_AdminTarget_ReturnsErrForbidden() {
	ctx := s.T().Context()
	s.users.EXPECT().GetByID(ctx, s.adminID).Return(newTestUser(s.adminID, true), nil)
	s.users.EXPECT().GetByID(ctx, s.targetID).Return(newTestUser(s.targetID, true), nil)

	s.ErrorIs(s.mgr.RevokeUser(ctx, s.adminID, s.targetID), domain.ErrForbidden)
}

func (s *UserAdminSuite) TestRevokeUser_NonAdminTarget_Revokes() {
	ctx := s.T().Context()
	s.users.EXPECT().GetByID(ctx, s.adminID).Return(newTestUser(s.adminID, true), nil)
	s.users.EXPECT().GetByID(ctx, s.targetID).Return(newTestUser(s.targetID, false), nil)
	s.users.EXPECT().Revoke(ctx, s.targetID).Return(nil)

	s.Require().NoError(s.mgr.RevokeUser(ctx, s.adminID, s.targetID))
}

func (s *UserAdminSuite) TestRevokeUser_RevokeFails_WrapsError() {
	ctx := s.T().Context()
	boom := errors.New("boom")

	s.users.EXPECT().GetByID(ctx, s.adminID).Return(newTestUser(s.adminID, true), nil)
	s.users.EXPECT().GetByID(ctx, s.targetID).Return(newTestUser(s.targetID, false), nil)
	s.users.EXPECT().Revoke(ctx, s.targetID).Return(boom)

	s.ErrorIs(s.mgr.RevokeUser(ctx, s.adminID, s.targetID), boom)
}

// --- helpers ---

func newTestUser(id uuid.UUID, isAdmin bool) *domain.User {
	return &domain.User{
		ID: id, IsAdmin: isAdmin, CreatedAt: time.Time{},
		LastLoginAt: nil, Username: "", TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0,
	}
}
