//go:build integration

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

type SQLiteAuthSuite struct {
	AuthSuite

	db *bun.DB
}

func (s *SQLiteAuthSuite) SetupTest() {
	name := strings.ReplaceAll(s.T().Name(), "/", "_")
	uri := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)

	sqldb, err := sql.Open(sqliteshim.DriverName(), uri)
	s.Require().NoError(err)

	s.db = bun.NewDB(sqldb, sqlitedialect.New())

	_, migrateErr := sqliterepo.Migrate(s.T().Context(), s.db)
	s.Require().NoError(migrateErr)

	s.Users = sqliterepo.NewUserRepository(s.db)
	s.Invites = sqliterepo.NewInviteRepository(s.db)
	s.Sessions = sqliterepo.NewSessionRepository(s.db)
}

func (s *SQLiteAuthSuite) TearDownTest() {
	if s.db != nil {
		s.Require().NoError(s.db.Close())
	}
}

func TestSQLiteAuth(t *testing.T) {
	suite.Run(t, new(SQLiteAuthSuite))
}
