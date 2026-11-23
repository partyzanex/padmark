//go:build integration

package integration

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

type PostgresManagerSuite struct {
	ManagerSuite

	objects *s3store.Store
	db      *bun.DB
}

func (s *PostgresManagerSuite) SetupSuite() {
	s.db = testenv.Postgres(s.T()).Open(s.T())
	s.Require().NoError(s.db.PingContext(s.T().Context()))
	_, migrateErr := pgrepo.Migrate(s.T().Context(), s.db)
	s.Require().NoError(migrateErr)

	s.objects = testenv.S3Mock(s.T()).Store(s.T())
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
	s.FileManager = newFileManager(&s.Suite, pgrepo.NewFileRepository(s.db), expired, s.objects)
	s.Objects = s.objects
	s.RevealStore = auth.NewRevealManager(pgrepo.NewRevealRepository(s.db), slog.Default(), time.Now)
	s.DB = s.db
}

func TestPostgresManager(t *testing.T) {
	suite.Run(t, new(PostgresManagerSuite))
}
