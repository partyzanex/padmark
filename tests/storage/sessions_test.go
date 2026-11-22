//go:build integration

package storage

import (
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// SessionSuite is the contract of the session repository.
type SessionSuite struct {
	repoSuite
}

func (s *SessionSuite) TestCreate_Get_RoundTrip() {
	ctx := s.T().Context()
	usr := s.createUser("alice", false)
	sess := newSession("hash-1", usr.ID, now().Add(time.Hour))

	s.Require().NoError(s.be.sessions.Create(ctx, sess))

	got, err := s.be.sessions.Get(ctx, "hash-1")
	s.Require().NoError(err)
	s.Equal(usr.ID, got.UserID)
	s.Equal(sess.UserAgent, got.UserAgent)
	s.Equal(sess.IP, got.IP)
	s.True(sess.ExpiresAt.Equal(got.ExpiresAt))
}

// TestGet_ExpiredAndUnknown_LookTheSame pins that a caller cannot tell an expired session from
// an unknown one.
func (s *SessionSuite) TestGet_ExpiredAndUnknown_LookTheSame() {
	ctx := s.T().Context()
	usr := s.createUser("bob", false)
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("expired", usr.ID, now().Add(-time.Minute))))

	_, err := s.be.sessions.Get(ctx, "expired")
	s.Require().ErrorIs(err, domain.ErrSessionExpired)

	_, err = s.be.sessions.Get(ctx, "unknown")
	s.Require().ErrorIs(err, domain.ErrSessionExpired)
}

func (s *SessionSuite) TestDelete() {
	ctx := s.T().Context()
	usr := s.createUser("carol", false)
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("doomed", usr.ID, now().Add(time.Hour))))

	s.Require().NoError(s.be.sessions.Delete(ctx, "doomed"))
	s.Require().NoError(s.be.sessions.Delete(ctx, "doomed"), "a second delete is not an error")

	_, err := s.be.sessions.Get(ctx, "doomed")
	s.Require().ErrorIs(err, domain.ErrSessionExpired)
}

func (s *SessionSuite) TestDeleteByUserID_KeepsOtherUsers() {
	ctx := s.T().Context()
	owner := s.createUser("dave", false)
	other := s.createUser("erin", false)
	expires := now().Add(time.Hour)

	s.Require().NoError(s.be.sessions.Create(ctx, newSession("owner-1", owner.ID, expires)))
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("owner-2", owner.ID, expires)))
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("other", other.ID, expires)))

	s.Require().NoError(s.be.sessions.DeleteByUserID(ctx, owner.ID))
	s.Require().NoError(s.be.sessions.DeleteByUserID(ctx, uuid.New()), "a user without sessions is not an error")

	_, err := s.be.sessions.Get(ctx, "owner-1")
	s.Require().ErrorIs(err, domain.ErrSessionExpired)
	_, err = s.be.sessions.Get(ctx, "owner-2")
	s.Require().ErrorIs(err, domain.ErrSessionExpired)
	_, err = s.be.sessions.Get(ctx, "other")
	s.NoError(err)
}

func (s *SessionSuite) TestDeleteByUserIDExcept_KeepsTheException() {
	ctx := s.T().Context()
	owner := s.createUser("frank", false)
	expires := now().Add(time.Hour)

	s.Require().NoError(s.be.sessions.Create(ctx, newSession("keep", owner.ID, expires)))
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("drop", owner.ID, expires)))

	s.Require().NoError(s.be.sessions.DeleteByUserIDExcept(ctx, owner.ID, "keep"))

	_, err := s.be.sessions.Get(ctx, "keep")
	s.Require().NoError(err)
	_, err = s.be.sessions.Get(ctx, "drop")
	s.Require().ErrorIs(err, domain.ErrSessionExpired)
}

func (s *SessionSuite) TestDeleteExpired_RemovesOnlyExpired() {
	ctx := s.T().Context()
	usr := s.createUser("grace", false)
	instant := now()

	s.Require().NoError(s.be.sessions.Create(ctx, newSession("old", usr.ID, instant.Add(-time.Minute))))
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("live", usr.ID, instant.Add(time.Hour))))

	s.Require().NoError(s.be.sessions.DeleteExpired(ctx, instant))

	s.Equal(int64(1), s.sessionRows())
	_, err := s.be.sessions.Get(ctx, "live")
	s.NoError(err)
}

// TestCreate_DoesNotDeleteExpired pins C5: the cleanup is an explicit call of the manager, not
// a side effect of Create.
func (s *SessionSuite) TestCreate_DoesNotDeleteExpired() {
	ctx := s.T().Context()
	usr := s.createUser("heidi", false)

	s.Require().NoError(s.be.sessions.Create(ctx, newSession("old", usr.ID, now().Add(-time.Minute))))
	s.Require().NoError(s.be.sessions.Create(ctx, newSession("new", usr.ID, now().Add(time.Hour))))

	s.Equal(int64(2), s.sessionRows())
}

func (s *SessionSuite) sessionRows() int64 {
	s.T().Helper()

	count, err := s.be.db.NewSelect().Table("sessions").Count(s.T().Context())
	s.Require().NoError(err)

	return count
}
