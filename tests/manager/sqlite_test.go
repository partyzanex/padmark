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

	objects *objectStore
	db      *bun.DB
}

func (s *SQLiteManagerSuite) SetupSuite() {
	s.objects = startObjectStore(&s.Suite)
}

func (s *SQLiteManagerSuite) TearDownSuite() {
	s.objects.stop(&s.Suite)
}

func (s *SQLiteManagerSuite) SetupTest() {
	// Unique in-memory DB per test — no cleanup needed, DB is destroyed on Close.
	name := strings.ReplaceAll(s.T().Name(), "/", "_")
	uri := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)

	db, err := sqliterepo.Open(uri, slog.New(slog.DiscardHandler))
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
	s.FileManager = newFileManager(&s.Suite, sqliterepo.NewFileRepository(s.db), sqliterepo.NewNoteRepository(s.db),
		sqliterepo.NewLinkRepository(s.db, slog.Default()), slugLock, s.objects.store)
	s.Objects = s.objects.store
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
