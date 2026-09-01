package integration

import (
	"database/sql"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
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
	ctx := s.T().Context()

	for _, tbl := range []string{"notes", "links", "reveal_tokens"} {
		_, err := s.db.NewTruncateTable().TableExpr(tbl).Cascade().Exec(ctx)
		s.Require().NoError(err)
	}

	s.Manager = newManager(pgrepo.NewNoteRepository(s.db), pgrepo.NewLinkRepository(s.db, slog.Default()))
	s.LinkManager = newLinkManager(pgrepo.NewLinkRepository(s.db, slog.Default()), pgrepo.NewNoteRepository(s.db))
	s.RevealStore = pgrepo.NewRevealRepository(s.db, slog.Default())
	s.DB = s.db
	s.NewRacingManagers = func(barrier *sync.WaitGroup) (*notes.Manager, *links.Manager) {
		noteRepo := pgrepo.NewNoteRepository(s.db)
		linkRepo := pgrepo.NewLinkRepository(s.db, slog.Default())

		nm := newManager(noteRepo, &barrierChecker{inner: linkRepo, barrier: barrier})
		lm := newLinkManager(linkRepo, &barrierChecker{inner: noteRepo, barrier: barrier})

		return nm, lm
	}
}

func TestPostgresManager(t *testing.T) {
	suite.Run(t, new(PostgresManagerSuite))
}
