package integration

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

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

	sqldb, err := sql.Open(sqliteshim.DriverName(), uri)
	s.Require().NoError(err)

	s.db = bun.NewDB(sqldb, sqlitedialect.New())
	_, migrateErr := sqliterepo.Migrate(s.T().Context(), s.db)
	s.Require().NoError(migrateErr)
	s.Manager = newManager(sqliterepo.NewNoteRepository(s.db))
	s.RevealStore = sqliterepo.NewRevealRepository(s.db)
}

func (s *SQLiteManagerSuite) TearDownTest() {
	if s.db != nil {
		s.Require().NoError(s.db.Close())
	}
}

func TestSQLiteManager(t *testing.T) {
	suite.Run(t, new(SQLiteManagerSuite))
}
