//go:build integration

package postgres

import (
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
)

// FileRepositorySuite uses the real migrations, so it also covers the files table and the
// slug_namespace trigger.
type FileRepositorySuite struct {
	suite.Suite

	container *tcpostgres.PostgresContainer
	db        *bun.DB
	repo      *FileRepository
	links     *LinkRepository
}

func TestFileRepositorySuite(t *testing.T) {
	suite.Run(t, new(FileRepositorySuite))
}

func (s *FileRepositorySuite) SetupSuite() {
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

	s.db = bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())
	s.Require().NoError(s.db.PingContext(ctx))

	_, err = Migrate(ctx, s.db)
	s.Require().NoError(err)

	s.repo = NewFileRepository(s.db)
	s.links = NewLinkRepository(s.db, slog.New(slog.DiscardHandler))
}

func (s *FileRepositorySuite) TearDownSuite() {
	if s.db != nil {
		s.NoError(s.db.Close())
	}

	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *FileRepositorySuite) SetupTest() {
	_, err := s.db.ExecContext(s.T().Context(), "TRUNCATE files, links, users CASCADE")
	s.Require().NoError(err)
}

func newFileRecord(id string) *domain.File {
	return &domain.File{
		CreatedAt: time.Now().UTC(), ExpiresAt: nil, Privacy: nil, OwnerID: nil,
		ID: domain.HashSlug(id), ObjectKey: "object-" + id, SealedMeta: "sealed", Name: "", ContentKey: nil,
		EditCode: "hash", Size: 42, Downloads: 0, BurnAfterDownload: false,
	}
}

func newLinkRecord(id string) *domain.Link {
	return &domain.Link{
		CreatedAt: time.Now(), ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		ID: id, TargetURL: "x", EditCode: "", Views: 0, BurnAfterClick: false,
	}
}

func (s *FileRepositorySuite) TestCreate_SlugSharedWithLinks() {
	ctx := s.T().Context()

	s.Require().NoError(s.links.Create(ctx, newLinkRecord(domain.HashSlug("shared"))))
	s.Require().ErrorIs(s.repo.Create(ctx, newFileRecord("shared")), domain.ErrSlugConflict)

	record := newFileRecord("own")
	s.Require().NoError(s.repo.Create(ctx, record))

	got, err := s.repo.GetByID(ctx, record.ID)
	s.Require().NoError(err)
	s.Equal(int64(42), got.Size)

	deleted, err := s.repo.Delete(ctx, record.ID)
	s.Require().NoError(err)
	s.Equal("object-own", deleted.ObjectKey)
	s.Require().NoError(s.links.Create(ctx, newLinkRecord(record.ID)))
}

func (s *FileRepositorySuite) TestConsumeAndCount() {
	ctx := s.T().Context()
	owner := uuid.New()
	ownerPrivacy := domain.PrivacyOwner

	_, err := s.db.ExecContext(ctx, "INSERT INTO users (id, username, totp_secret) VALUES (?, ?, ?)",
		owner, "owner", "x")
	s.Require().NoError(err)

	burn := newFileRecord("burn")
	burn.BurnAfterDownload = true
	burn.OwnerID = &owner
	burn.Privacy = &ownerPrivacy
	s.Require().NoError(s.repo.Create(ctx, burn))

	_, err = s.repo.ConsumeVisible(ctx, burn.ID, uuid.New(), true, true)
	s.Require().ErrorIs(err, domain.ErrNotFound)

	_, err = s.repo.ConsumeVisible(ctx, burn.ID, owner, true, true)
	s.Require().NoError(err)

	plain := newFileRecord("plain")
	s.Require().NoError(s.repo.Create(ctx, plain))
	s.Require().NoError(s.repo.IncrementDownloadsVisible(ctx, plain.ID, uuid.Nil, false))

	got, err := s.repo.GetByID(ctx, plain.ID)
	s.Require().NoError(err)
	s.Equal(1, got.Downloads)
}

func (s *FileRepositorySuite) TestDeleteExpired() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Hour)

	expired := newFileRecord("old")
	expired.ExpiresAt = &past
	s.Require().NoError(s.repo.Create(ctx, expired))
	s.Require().NoError(s.repo.Create(ctx, newFileRecord("forever")))

	keys, err := s.repo.DeleteExpired(ctx, time.Now())
	s.Require().NoError(err)
	s.Equal([]string{"object-old"}, keys)
}

func (s *FileRepositorySuite) TestUpdate() {
	ctx := s.T().Context()
	record := newFileRecord("upd")
	s.Require().NoError(s.repo.Create(ctx, record))
	s.Require().NoError(s.repo.IncrementDownloadsVisible(ctx, record.ID, uuid.Nil, false))

	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	authenticated := domain.PrivacyAuthenticated
	change := newFileRecord("upd")
	change.ExpiresAt = &expires
	change.Privacy = &authenticated
	change.BurnAfterDownload = true
	change.ObjectKey = "other-object"
	change.SealedMeta = "other-meta"
	change.Size = 7
	s.Require().NoError(s.repo.Update(ctx, change))

	got, err := s.repo.GetByID(ctx, record.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.ExpiresAt)
	s.True(expires.Equal(*got.ExpiresAt))
	s.Equal(domain.PrivacyAuthenticated, got.EffectivePrivacy())
	s.True(got.BurnAfterDownload)
	s.Equal("object-upd", got.ObjectKey, "update keeps the object key")
	s.Equal("sealed", got.SealedMeta, "update keeps the sealed metadata")
	s.Equal(int64(42), got.Size, "update keeps the size")
	s.Equal(1, got.Downloads, "update keeps the download counter")

	change.ExpiresAt = nil
	s.Require().NoError(s.repo.Update(ctx, change))

	got, err = s.repo.GetByID(ctx, record.ID)
	s.Require().NoError(err)
	s.Nil(got.ExpiresAt, "a nil expiry clears the stored expiry")
}

func (s *FileRepositorySuite) TestUpdate_NotFound() {
	err := s.repo.Update(s.T().Context(), newFileRecord("missing"))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}
