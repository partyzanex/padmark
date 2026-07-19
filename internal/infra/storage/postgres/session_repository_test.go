package postgres

import (
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
)

type SessionRepositoryTestSuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
	repo      *SessionRepository
	users     *UserRepository
}

func (s *SessionRepositoryTestSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_session_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	s.Require().NoError(err)

	s.container = container

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	s.Require().NoError(err)

	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))
	s.db = bun.NewDB(sqldb, pgdialect.New())
	s.Require().NoError(s.db.PingContext(ctx))

	_, err = Migrate(ctx, s.db)
	s.Require().NoError(err)

	s.repo = NewSessionRepository(s.db)
	s.users = NewUserRepository(s.db)
}

func (s *SessionRepositoryTestSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *SessionRepositoryTestSuite) SetupTest() {
	ctx := s.T().Context()

	_, err := s.db.NewTruncateTable().
		TableExpr("sessions").
		Cascade().
		Exec(ctx)
	s.Require().NoError(err)

	_, err = s.db.NewTruncateTable().
		TableExpr("users").
		Cascade().
		Exec(ctx)
	s.Require().NoError(err)
}

func TestSessionRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(SessionRepositoryTestSuite))
}

// createUser inserts a test user and returns the generated UUID.
func (s *SessionRepositoryTestSuite) createUser(suffix string) uuid.UUID {
	s.T().Helper()

	id := uuid.New()
	err := s.users.Create(s.T().Context(), &domain.User{
		ID:           id,
		Username:     "user-" + suffix,
		TOTPSecret:   "secret",
		PasswordHash: "hash",
		KDFSalt:      []byte("saltsaltsalt1234"),
		CreatedAt:    time.Now().Truncate(time.Second),
	})
	s.Require().NoError(err)

	return id
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
	userID := s.createUser("u1")

	sess := s.newSession("sess-1", userID, time.Hour)
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
	userID := s.createUser("u2")

	expired := s.newSession("sess-expired", userID, -time.Minute)
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
	userID := s.createUser("u3")

	boundary := &sessionRow{
		SessionID: "sess-boundary",
		UserID:    userID,
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
	userID := s.createUser("u4")

	sess := s.newSession("sess-del", userID, time.Hour)
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
	ownerID := s.createUser("owner")
	otherID := s.createUser("other")

	for i, id := range []string{"sess-a", "sess-b", "sess-c"} {
		sess := s.newSession(id, ownerID, time.Hour)
		sess.IP = "1.2.3." + string(rune('1'+i))
		s.Require().NoError(s.repo.Create(ctx, sess))
	}

	// Session belonging to a different user — must survive.
	s.Require().NoError(s.repo.Create(ctx, s.newSession("sess-other", otherID, time.Hour)))

	s.Require().NoError(s.repo.DeleteByUserID(ctx, ownerID))

	for _, id := range []string{"sess-a", "sess-b", "sess-c"} {
		_, err := s.repo.Get(ctx, id)
		s.Require().ErrorIs(err, domain.ErrSessionExpired, "session %s must be gone after DeleteByUserID", id)
	}

	_, err := s.repo.Get(ctx, "sess-other")
	s.Require().NoError(err, "other user's session must not be affected")
}

func (s *SessionRepositoryTestSuite) TestDeleteByUserIDExcept_PreservesExceptedSession() {
	ctx := s.T().Context()
	ownerID := s.createUser("owner")
	otherID := s.createUser("other")

	keep := s.newSession("sess-keep", ownerID, time.Hour)
	old1 := s.newSession("sess-old1", ownerID, time.Hour)
	s.Require().NoError(s.repo.Create(ctx, keep))
	s.Require().NoError(s.repo.Create(ctx, old1))
	s.Require().NoError(s.repo.Create(ctx, s.newSession("sess-other", otherID, time.Hour)))

	s.Require().NoError(s.repo.DeleteByUserIDExcept(ctx, ownerID, keep.SessionID))

	_, err := s.repo.Get(ctx, keep.SessionID)
	s.Require().NoError(err, "excepted session must survive")

	_, err = s.repo.Get(ctx, old1.SessionID)
	s.Require().ErrorIs(err, domain.ErrSessionExpired, "old session must be removed")

	_, err = s.repo.Get(ctx, "sess-other")
	s.Require().NoError(err, "other user's session must not be affected")
}

func (s *SessionRepositoryTestSuite) TestDeleteByUserIDExcept_NoOtherSessions_IsIdempotent() {
	ctx := s.T().Context()
	userID := s.createUser("sole-user")
	sess := s.newSession("sess-sole", userID, time.Hour)
	s.Require().NoError(s.repo.Create(ctx, sess))

	s.Require().NoError(s.repo.DeleteByUserIDExcept(ctx, userID, sess.SessionID))

	_, err := s.repo.Get(ctx, sess.SessionID)
	s.Require().NoError(err, "sole session must survive when it is the excepted one")
}

func (s *SessionRepositoryTestSuite) TestDeleteByUserID_NoSessions_IsIdempotent() {
	userID := s.createUser("empty-user")
	err := s.repo.DeleteByUserID(s.T().Context(), userID)
	s.Require().NoError(err)
}

// ── DeleteExpired ──

func (s *SessionRepositoryTestSuite) TestDeleteExpired_RemovesExpiredKeepsActive() {
	ctx := s.T().Context()
	userID := s.createUser("u5")

	expired := s.newSession("sess-old", userID, -time.Minute)
	active := s.newSession("sess-live", userID, time.Hour)

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
