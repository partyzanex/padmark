package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

type RevealSuite struct {
	suite.Suite

	ctrl  *gomock.Controller
	store *MockRevealTokenStore
	mgr   *RevealManager
}

func TestRevealSuite(t *testing.T) {
	suite.Run(t, new(RevealSuite))
}

func (s *RevealSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.store = NewMockRevealTokenStore(s.ctrl)
	s.mgr = NewRevealManager(s.store, discardLog())
}

func (s *RevealSuite) TearDownTest() {
	s.ctrl.Finish()
}

// TestIssue_StoresHashAndTTL pins the rules that moved out of the repositories: the store gets
// only the token hash, the token expires after revealTokenTTL, and stale tokens are deleted after
// the insert.
func (s *RevealSuite) TestIssue_StoresHashAndTTL() {
	ctx := s.T().Context()
	before := time.Now()

	var (
		storedHash string
		expiresAt  time.Time
	)

	gomock.InOrder(
		s.store.EXPECT().Create(ctx, gomock.Any(), "note-hash", gomock.Any()).
			DoAndReturn(func(_ context.Context, tokenHash, _ string, exp time.Time) error {
				storedHash, expiresAt = tokenHash, exp

				return nil
			}),
		s.store.EXPECT().DeleteStale(ctx, gomock.Any()).Return(nil),
	)

	tok, err := s.mgr.Issue(ctx, "note-hash")
	s.Require().NoError(err)
	s.NotEmpty(tok)
	s.Equal(domain.HashRevealToken(tok), storedHash)
	s.NotEqual(tok, storedHash)
	s.WithinDuration(before.Add(revealTokenTTL), expiresAt, time.Minute)
}

func (s *RevealSuite) TestIssue_CreateFails_ReturnsError() {
	ctx := s.T().Context()
	boom := errors.New("boom")
	s.store.EXPECT().Create(ctx, gomock.Any(), "note-hash", gomock.Any()).Return(boom)

	_, err := s.mgr.Issue(ctx, "note-hash")
	s.ErrorIs(err, boom)
}

func (s *RevealSuite) TestIssue_DeleteStaleFails_ReturnsToken() {
	ctx := s.T().Context()
	s.store.EXPECT().Create(ctx, gomock.Any(), "note-hash", gomock.Any()).Return(nil)
	s.store.EXPECT().DeleteStale(ctx, gomock.Any()).Return(errors.New("db down"))

	tok, err := s.mgr.Issue(ctx, "note-hash")
	s.Require().NoError(err)
	s.NotEmpty(tok)
}

func (s *RevealSuite) TestConsume_PassesHash() {
	ctx := s.T().Context()
	s.store.EXPECT().Consume(ctx, domain.HashRevealToken("tok"), "note-hash", gomock.Any()).Return(true, nil)

	s.True(s.mgr.Consume(ctx, "tok", "note-hash"))
}

func (s *RevealSuite) TestConsume_ConditionNotMet_ReturnsFalse() {
	ctx := s.T().Context()
	s.store.EXPECT().Consume(ctx, domain.HashRevealToken("tok"), "note-hash", gomock.Any()).Return(false, nil)

	s.False(s.mgr.Consume(ctx, "tok", "note-hash"))
}

// TestConsume_StoreFails_ReturnsFalse pins C4 on the manager side: a store failure never
// reveals the note.
func (s *RevealSuite) TestConsume_StoreFails_ReturnsFalse() {
	ctx := s.T().Context()
	s.store.EXPECT().Consume(ctx, domain.HashRevealToken("tok"), "note-hash", gomock.Any()).
		Return(false, errors.New("db down"))

	s.False(s.mgr.Consume(ctx, "tok", "note-hash"))
}
