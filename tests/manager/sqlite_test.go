package integration

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

type SQLiteManagerSuite struct {
	ManagerSuite

	db *bun.DB
}

func (s *SQLiteManagerSuite) SetupTest() {
	// Unique in-memory DB per test — no cleanup needed, DB is destroyed on Close.
	name := strings.ReplaceAll(s.T().Name(), "/", "_")
	uri := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)

	sqldb, err := sql.Open(sqliteshim.DriverName(), uri)
	s.Require().NoError(err)

	s.db = bun.NewDB(sqldb, sqlitedialect.New())
	_, migrateErr := sqliterepo.Migrate(s.T().Context(), s.db)
	s.Require().NoError(migrateErr)
	s.Manager = newManager(sqliterepo.NewNoteRepository(s.db), sqliterepo.NewLinkRepository(s.db, slog.Default()))
	s.LinkManager = newLinkManager(sqliterepo.NewLinkRepository(s.db, slog.Default()), sqliterepo.NewNoteRepository(s.db))
	s.RevealStore = sqliterepo.NewRevealRepository(s.db, slog.Default())
	s.DB = s.db
	s.NewRacingManagers = func(barrier *sync.WaitGroup) (*notes.Manager, *links.Manager) {
		noteRepo := sqliterepo.NewNoteRepository(s.db)
		linkRepo := sqliterepo.NewLinkRepository(s.db, slog.Default())

		nm := newManager(noteRepo, &barrierChecker{inner: linkRepo, barrier: barrier})
		lm := newLinkManager(linkRepo, &barrierChecker{inner: noteRepo, barrier: barrier})

		return nm, lm
	}
}

func (s *SQLiteManagerSuite) TearDownTest() {
	if s.db != nil {
		s.Require().NoError(s.db.Close())
	}
}

func TestSQLiteManager(t *testing.T) {
	suite.Run(t, new(SQLiteManagerSuite))
}
