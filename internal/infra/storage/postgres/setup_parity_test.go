//go:build integration

package postgres

import (
	"database/sql"
	"log/slog"
	"sync"
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

// parityConcurrency is how many goroutines race a single-use operation. It exceeds the default
// pool size so contention happens inside PostgreSQL, not in database/sql's connection queue.
const parityConcurrency = 16

// ParitySuite pins, on PostgreSQL, the behaviour the SQLite repositories and their tests
// establish for methods the per-repository Postgres suites did not cover — chiefly the
// security-relevant single-use and compare-and-swap operations, which on Postgres rely on
// conditional statements under READ COMMITTED rather than SQLite's single writer.
type ParitySuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
	discard   *slog.Logger
}

func (s *ParitySuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_parity_test"),
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

	s.discard = slog.New(slog.DiscardHandler)
}

func (s *ParitySuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *ParitySuite) SetupTest() {
	_, err := s.db.ExecContext(s.T().Context(), "TRUNCATE notes, links, reveal_tokens, sessions, invites, "+
		"api_tokens, users, content_encryption_keys, content_encryption_state CASCADE")
	s.Require().NoError(err)
}

func TestParitySuite(t *testing.T) {
	suite.Run(t, new(ParitySuite))
}

func (s *ParitySuite) parityUser(username string) *domain.User {
	return &domain.User{
		ID:           uuid.New(),
		Username:     username,
		TOTPSecret:   "test-secret",
		PasswordHash: "test-hash",
		KDFSalt:      []byte("test-salt"),
		CreatedAt:    time.Now().UTC().Truncate(time.Microsecond),

		LastLoginAt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
}

func (s *ParitySuite) createParityUser(username string, admin bool) *domain.User {
	s.T().Helper()

	usr := s.parityUser(username)
	usr.IsAdmin = admin
	s.Require().NoError(NewUserRepository(s.db).Create(s.T().Context(), usr))

	return usr
}

func (s *ParitySuite) count(table string) int64 {
	s.T().Helper()

	count, err := s.db.NewSelect().Table(table).Count(s.T().Context())
	s.Require().NoError(err)

	return count
}

// race runs operation concurrently parityConcurrency times, released together, and returns the results
// indexed by attempt.
func race[T any](operation func(attempt int) T) []T {
	results := make([]T, parityConcurrency)
	start := make(chan struct{})

	var group sync.WaitGroup

	for attempt := range parityConcurrency {
		group.Go(func() {
			<-start

			results[attempt] = operation(attempt)
		})
	}

	close(start)
	group.Wait()

	return results
}
