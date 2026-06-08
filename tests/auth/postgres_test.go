//go:build integration

package integration

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
)

type PostgresAuthSuite struct {
	AuthSuite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
}

func (s *PostgresAuthSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_auth_test"),
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

	_, migrateErr := pgrepo.Migrate(ctx, s.db)
	s.Require().NoError(migrateErr)
}

func (s *PostgresAuthSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *PostgresAuthSuite) SetupTest() {
	ctx := s.T().Context()

	for _, tbl := range []string{"sessions", "invites", "users"} {
		_, err := s.db.NewTruncateTable().TableExpr(tbl).Cascade().Exec(ctx)
		s.Require().NoError(err)
	}

	s.Users = pgrepo.NewUserRepository(s.db)
	s.Invites = pgrepo.NewInviteRepository(s.db)
	s.Sessions = pgrepo.NewSessionRepository(s.db)
}

func TestPostgresAuth(t *testing.T) {
	suite.Run(t, new(PostgresAuthSuite))
}
