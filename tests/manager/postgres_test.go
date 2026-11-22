//go:build integration

package integration

import (
	"database/sql"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

type PostgresManagerSuite struct {
	ManagerSuite

	container *tcpostgres.PostgresContainer
	objects   *objectStore
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

	s.objects = startObjectStore(&s.Suite)
}

func (s *PostgresManagerSuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}

	s.objects.stop(&s.Suite)
}

func (s *PostgresManagerSuite) SetupTest() {
	ctx := s.T().Context()

	for _, tbl := range []string{"notes", "links", "files", "reveal_tokens"} {
		_, err := s.db.NewTruncateTable().TableExpr(tbl).Cascade().Exec(ctx)
		s.Require().NoError(err)
	}

	noteRepo, linkRepo := pgrepo.NewNoteRepository(s.db), pgrepo.NewLinkRepository(s.db, slog.Default())
	expired := expiredslugs.New(noteRepo, linkRepo)

	s.Manager = newManager(s.T(), noteRepo, expired)
	s.LinkManager = newLinkManager(s.T(), linkRepo, expired)
	s.FileManager = newFileManager(&s.Suite, pgrepo.NewFileRepository(s.db), expired, s.objects.store)
	s.Objects = s.objects.store
	s.RevealStore = auth.NewRevealManager(pgrepo.NewRevealRepository(s.db), slog.Default())
	s.DB = s.db
}

func TestPostgresManager(t *testing.T) {
	suite.Run(t, new(PostgresManagerSuite))
}
