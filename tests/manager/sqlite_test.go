//go:build integration

package integration

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

type SQLiteManagerSuite struct {
	ManagerSuite

	objects *s3store.Store
	db      *bun.DB
}

func (s *SQLiteManagerSuite) SetupSuite() {
	s.objects = testenv.S3Mock(s.T()).Store(s.T())
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

	noteRepo, linkRepo := sqliterepo.NewNoteRepository(s.db), sqliterepo.NewLinkRepository(s.db, slog.Default())
	expired := expiredslugs.New(noteRepo, linkRepo)

	fileRepo := sqliterepo.NewFileRepository(s.db)

	s.Manager = newManager(s.T(), noteRepo, expired)
	s.LinkManager = newLinkManager(s.T(), linkRepo, expired)
	s.FileManager = newFileManager(&s.Suite, fileRepo, expired, s.objects)
	s.NoteStorage, s.LinkStorage, s.FileStorage = noteRepo, linkRepo, fileRepo
	s.Objects = s.objects
	s.RevealStore = auth.NewRevealManager(sqliterepo.NewRevealRepository(s.db), slog.Default(), time.Now)
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
