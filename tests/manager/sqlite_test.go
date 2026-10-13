//go:build integration

package integration

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/infra/lock"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
)

type SQLiteManagerSuite struct {
	ManagerSuite

	db *bun.DB
}

func (s *SQLiteManagerSuite) SetupTest() {
	// Unique in-memory DB per test — no cleanup needed, DB is destroyed on Close.
	name := strings.ReplaceAll(s.T().Name(), "/", "_")
	uri := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)

	db, err := sqliterepo.Open(uri)
	s.Require().NoError(err)

	s.db = db
	_, migrateErr := sqliterepo.Migrate(s.T().Context(), s.db)
	s.Require().NoError(migrateErr)

	slugLock := lock.NewLocker()

	s.Manager = newManager(s.T(),
		sqliterepo.NewNoteRepository(s.db), sqliterepo.NewLinkRepository(s.db, slog.Default()), slugLock,
	)
	s.LinkManager = newLinkManager(s.T(),
		sqliterepo.NewLinkRepository(s.db, slog.Default()), sqliterepo.NewNoteRepository(s.db), slugLock,
	)
	s.RevealStore = sqliterepo.NewRevealRepository(s.db, slog.Default())
	s.DB = s.db
}

func (s *SQLiteManagerSuite) TearDownTest() {
	if s.db != nil {
		s.Require().NoError(s.db.Close())
	}
}

func TestSQLiteManager(t *testing.T) {
	suite.Run(t, new(SQLiteManagerSuite))
}
