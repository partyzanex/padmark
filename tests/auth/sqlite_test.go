//go:build integration

package integration

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/uptrace/bun"

	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
)

type SQLiteAuthSuite struct {
	AuthSuite

	db *bun.DB
}

func (s *SQLiteAuthSuite) SetupTest() {
	name := strings.ReplaceAll(s.T().Name(), "/", "_")
	uri := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)

	db, err := sqliterepo.Open(uri, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	s.db = db

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
