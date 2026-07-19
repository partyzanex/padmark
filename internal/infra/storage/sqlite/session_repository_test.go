package sqlite

import (
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	"github.com/partyzanex/padmark/internal/domain"
)

type SessionRepositoryTestSuite struct {
	suite.Suite

	db    *bun.DB
	repo  *SessionRepository
	users *UserRepository
}

func (s *SessionRepositoryTestSuite) SetupTest() {
	sqldb, err := sql.Open(sqliteshim.DriverName(), "file::memory:?cache=shared&_busy_timeout=5000")
	s.Require().NoError(err)

	s.db = bun.NewDB(sqldb, sqlitedialect.New())
	s.Require().NoError(s.db.Ping())

	_, err = Migrate(s.T().Context(), s.db)
	s.Require().NoError(err)

	s.repo = NewSessionRepository(s.db)
	s.users = NewUserRepository(s.db)
}

func (s *SessionRepositoryTestSuite) TearDownTest() {
	s.Require().NoError(s.db.Close())
}

func TestSessionRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(SessionRepositoryTestSuite))
}

func (s *SessionRepositoryTestSuite) createUser(id uuid.UUID) {
	s.T().Helper()

	err := s.users.Create(s.T().Context(), &domain.User{
		ID:           id,
		Username:     "user-" + id.String(),
		TOTPSecret:   "secret",
		PasswordHash: "hash",
		KDFSalt:      []byte("saltsaltsalt1234"),
		CreatedAt:    time.Now().Truncate(time.Second),
	})
	s.Require().NoError(err)
}

func (s *SessionRepositoryTestSuite) newSession(id string, userID uuid.UUID, ttl time.Duration) *domain.Session {
	return &domain.Session{
		SessionID: id,
		UserID:    userID,
		CreatedAt: time.Now().Truncate(time.Second),
		ExpiresAt: time.Now().Add(ttl).Truncate(time.Second),
		UserAgent: "Mozilla/5.0",
		IP:        "127.0.0.1",
	}
}

// ── Create / Get ──

func (s *SessionRepositoryTestSuite) TestCreate_Get_RoundTrip() {
	ctx := s.T().Context()
	u1 := uuid.New()
	s.createUser(u1)

	sess := s.newSession("sess-1", u1, time.Hour)
	s.Require().NoError(s.repo.Create(ctx, sess))

	got, err := s.repo.Get(ctx, sess.SessionID)
	s.Require().NoError(err)
	s.Equal(sess.SessionID, got.SessionID)
	s.Equal(sess.UserID, got.UserID)
	s.Equal(sess.UserAgent, got.UserAgent)
	s.Equal(sess.IP, got.IP)
}

func (s *SessionRepositoryTestSuite) TestGet_Expired_ReturnsErrSessionExpired() {
	ctx := s.T().Context()
	u2 := uuid.New()
	s.createUser(u2)

	expired := s.newSession("sess-expired", u2, -time.Minute)
	s.Require().NoError(s.repo.Create(ctx, expired))

	_, err := s.repo.Get(ctx, expired.SessionID)
	s.ErrorIs(err, domain.ErrSessionExpired)
}

func (s *SessionRepositoryTestSuite) TestGet_Unknown_ReturnsErrSessionExpired() {
	_, err := s.repo.Get(s.T().Context(), "no-such-session")
	s.ErrorIs(err, domain.ErrSessionExpired)
}

// TestGet_ExactExpiryBoundary_ReturnsErrSessionExpired verifies the query uses
// strict greater-than (expires_at > now), so a session at the exact boundary is rejected.
func (s *SessionRepositoryTestSuite) TestGet_ExactExpiryBoundary_ReturnsErrSessionExpired() {
	ctx := s.T().Context()
	u3 := uuid.New()
	s.createUser(u3)

	boundary := &sessionRow{
		SessionID: "sess-boundary",
		UserID:    u3,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now(), // exactly now — must be rejected
		UserAgent: "",
		IP:        "",
	}
	_, err := s.db.NewInsert().Model(boundary).Exec(ctx)
	s.Require().NoError(err)

	_, err = s.repo.Get(ctx, "sess-boundary")
	s.ErrorIs(err, domain.ErrSessionExpired, "session at exact expiry boundary must be rejected")
}

// ── Delete ──

func (s *SessionRepositoryTestSuite) TestDelete_RemovesSession() {
	ctx := s.T().Context()
	u4 := uuid.New()
	s.createUser(u4)

	sess := s.newSession("sess-del", u4, time.Hour)
	s.Require().NoError(s.repo.Create(ctx, sess))
	s.Require().NoError(s.repo.Delete(ctx, sess.SessionID))

	_, err := s.repo.Get(ctx, sess.SessionID)
	s.ErrorIs(err, domain.ErrSessionExpired)
}

func (s *SessionRepositoryTestSuite) TestDelete_NonExistentSession_NoError() {
	err := s.repo.Delete(s.T().Context(), "ghost-session")
	s.Require().NoError(err)
}

// ── DeleteByUserID ──

func (s *SessionRepositoryTestSuite) TestDeleteByUserID_RemovesAllUserSessions() {
	ctx := s.T().Context()
	owner := uuid.New()
	other := uuid.New()

	s.createUser(owner)
	s.createUser(other)

	for i, id := range []string{"sess-a", "sess-b", "sess-c"} {
		sess := s.newSession(id, owner, time.Hour)
		sess.IP = "1.2.3." + string(rune('1'+i))
		s.Require().NoError(s.repo.Create(ctx, sess))
	}

	// Session belonging to a different user — must survive.
	s.Require().NoError(s.repo.Create(ctx, s.newSession("sess-other", other, time.Hour)))

	s.Require().NoError(s.repo.DeleteByUserID(ctx, owner))

	for _, id := range []string{"sess-a", "sess-b", "sess-c"} {
		_, err := s.repo.Get(ctx, id)
		s.Require().ErrorIs(err, domain.ErrSessionExpired, "session %s must be gone after DeleteByUserID", id)
	}

	_, err := s.repo.Get(ctx, "sess-other")
	s.Require().NoError(err, "other user's session must not be affected")
}

func (s *SessionRepositoryTestSuite) TestDeleteByUserIDExcept_PreservesExceptedSession() {
	ctx := s.T().Context()
	owner := uuid.New()
	other := uuid.New()

	s.createUser(owner)
	s.createUser(other)

	keep := s.newSession("sess-keep", owner, time.Hour)
	old1 := s.newSession("sess-old1", owner, time.Hour)
	s.Require().NoError(s.repo.Create(ctx, keep))
	s.Require().NoError(s.repo.Create(ctx, old1))
	// Another user's session must not be touched.
	s.Require().NoError(s.repo.Create(ctx, s.newSession("sess-other", other, time.Hour)))

	s.Require().NoError(s.repo.DeleteByUserIDExcept(ctx, owner, keep.SessionID))

	_, err := s.repo.Get(ctx, keep.SessionID)
	s.Require().NoError(err, "excepted session must survive")

	_, err = s.repo.Get(ctx, old1.SessionID)
	s.Require().ErrorIs(err, domain.ErrSessionExpired, "old session must be removed")

	_, err = s.repo.Get(ctx, "sess-other")
	s.Require().NoError(err, "other user's session must not be affected")
}

func (s *SessionRepositoryTestSuite) TestDeleteByUserIDExcept_NoOtherSessions_IsIdempotent() {
	ctx := s.T().Context()
	soleUser := uuid.New()
	s.createUser(soleUser)
	sess := s.newSession("sole-sess", soleUser, time.Hour)
	s.Require().NoError(s.repo.Create(ctx, sess))

	s.Require().NoError(s.repo.DeleteByUserIDExcept(ctx, soleUser, sess.SessionID))

	_, err := s.repo.Get(ctx, sess.SessionID)
	s.Require().NoError(err, "sole session must survive when it is the excepted one")
}

func (s *SessionRepositoryTestSuite) TestDeleteByUserID_NoSessions_IsIdempotent() {
	emptyUser := uuid.New()
	s.createUser(emptyUser)
	err := s.repo.DeleteByUserID(s.T().Context(), emptyUser)
	s.Require().NoError(err)
}

// ── DeleteExpired ──

func (s *SessionRepositoryTestSuite) TestDeleteExpired_RemovesExpiredKeepsActive() {
	ctx := s.T().Context()
	u5 := uuid.New()
	s.createUser(u5)

	expired := s.newSession("sess-old", u5, -time.Minute)
	active := s.newSession("sess-live", u5, time.Hour)

	s.Require().NoError(s.repo.Create(ctx, expired))
	s.Require().NoError(s.repo.Create(ctx, active))

	s.Require().NoError(s.repo.DeleteExpired(ctx))

	_, err := s.repo.Get(ctx, active.SessionID)
	s.Require().NoError(err, "active session must survive DeleteExpired")

	var count int

	err = s.db.NewSelect().
		TableExpr("sessions").
		ColumnExpr("count(*)").
		Where("session_id = ?", expired.SessionID).
		Scan(ctx, &count)
	s.Require().NoError(err)
	s.Zero(count, "expired session must be removed by DeleteExpired")
}
