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

type PostgresManagerSuite struct {
	ManagerSuite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
}

func (s *PostgresManagerSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark_test"),
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

func (s *PostgresManagerSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *PostgresManagerSuite) SetupTest() {
	_, err := s.db.NewTruncateTable().TableExpr("notes").Cascade().Exec(s.T().Context())
	s.Require().NoError(err)

	s.Manager = newManager(pgrepo.NewRepository(s.db))
}

func TestPostgresManager(t *testing.T) {
	suite.Run(t, new(PostgresManagerSuite))
}
