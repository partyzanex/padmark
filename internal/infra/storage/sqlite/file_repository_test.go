package sqlite

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

// FileRepositorySuite uses the real migrations, so it also covers the files table and the
// slug_namespace triggers.
type FileRepositorySuite struct {
	suite.Suite

	db    *bun.DB
	repo  *FileRepository
	links *LinkRepository
}

func TestFileRepositorySuite(t *testing.T) {
	suite.Run(t, new(FileRepositorySuite))
}

func (s *FileRepositorySuite) SetupTest() {
	db, err := Open(filepath.Join(s.T().TempDir(), "files.db"), slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	_, err = Migrate(s.T().Context(), db)
	s.Require().NoError(err)

	s.db = db
	s.repo = NewFileRepository(db)
	s.links = NewLinkRepository(db, slog.New(slog.DiscardHandler))
}

func (s *FileRepositorySuite) TearDownTest() {
	s.Require().NoError(s.db.Close())
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

func (s *FileRepositorySuite) TestCreateAndGet() {
	ctx := s.T().Context()
	record := newFileRecord("a")

	s.Require().NoError(s.repo.Create(ctx, record))

	got, err := s.repo.GetByID(ctx, record.ID)
	s.Require().NoError(err)
	s.Equal("object-a", got.ObjectKey)
	s.Equal("sealed", got.SealedMeta)
	s.Equal(int64(42), got.Size)
	s.Equal(domain.PrivacyPublic, got.EffectivePrivacy())

	exists, err := s.repo.Exists(ctx, record.ID)
	s.Require().NoError(err)
	s.True(exists)

	_, err = s.repo.GetByID(ctx, domain.HashSlug("missing"))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *FileRepositorySuite) TestCreate_SlugSharedWithLinks() {
	ctx := s.T().Context()

	s.Require().NoError(s.links.Create(ctx, newLinkRecord(domain.HashSlug("shared"))))
	s.Require().ErrorIs(s.repo.Create(ctx, newFileRecord("shared")), domain.ErrSlugConflict)

	s.Require().NoError(s.repo.Create(ctx, newFileRecord("own")))
	s.Require().ErrorIs(s.links.Create(ctx, newLinkRecord(domain.HashSlug("own"))), domain.ErrSlugConflict)
}

func (s *FileRepositorySuite) TestDelete_ReleasesSlug() {
	ctx := s.T().Context()
	record := newFileRecord("a")
	s.Require().NoError(s.repo.Create(ctx, record))

	deleted, err := s.repo.Delete(ctx, record.ID)
	s.Require().NoError(err)
	s.Equal("object-a", deleted.ObjectKey)

	_, err = s.repo.Delete(ctx, record.ID)
	s.Require().ErrorIs(err, domain.ErrNotFound)

	s.Require().NoError(s.links.Create(ctx, newLinkRecord(record.ID)))
}

func (s *FileRepositorySuite) TestConsumeVisible() {
	ctx := s.T().Context()
	owner := uuid.New()
	ownerPrivacy := domain.PrivacyOwner

	_, err := s.db.NewRaw("INSERT INTO users (id, username, totp_secret) VALUES (?, ?, ?)",
		owner, "owner", "x").Exec(ctx)
	s.Require().NoError(err)

	burn := newFileRecord("burn")
	burn.BurnAfterDownload = true
	burn.OwnerID = &owner
	burn.Privacy = &ownerPrivacy
	s.Require().NoError(s.repo.Create(ctx, burn))

	_, err = s.repo.ConsumeVisible(ctx, burn.ID, uuid.New(), true, true)
	s.Require().ErrorIs(err, domain.ErrNotFound, "another user cannot consume an owner file")

	_, err = s.repo.ConsumeVisible(ctx, burn.ID, owner, true, false)
	s.Require().ErrorIs(err, domain.ErrNotFound, "without allowBurn only an expired file qualifies")

	consumed, err := s.repo.ConsumeVisible(ctx, burn.ID, owner, true, true)
	s.Require().NoError(err)
	s.Equal("object-burn", consumed.ObjectKey)

	plain := newFileRecord("plain")
	s.Require().NoError(s.repo.Create(ctx, plain))

	_, err = s.repo.ConsumeVisible(ctx, plain.ID, uuid.Nil, false, true)
	s.Require().ErrorIs(err, domain.ErrNotFound, "a file that is not burn and not expired stays")
}

func (s *FileRepositorySuite) TestIncrementDownloadsVisible() {
	ctx := s.T().Context()
	authenticated := domain.PrivacyAuthenticated
	record := newFileRecord("a")
	record.Privacy = &authenticated
	s.Require().NoError(s.repo.Create(ctx, record))

	s.Require().ErrorIs(s.repo.IncrementDownloadsVisible(ctx, record.ID, uuid.Nil, false), domain.ErrNotFound)
	s.Require().NoError(s.repo.IncrementDownloadsVisible(ctx, record.ID, uuid.New(), true))

	got, err := s.repo.GetByID(ctx, record.ID)
	s.Require().NoError(err)
	s.Equal(1, got.Downloads)
}

func (s *FileRepositorySuite) TestDeleteExpired() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	expired := newFileRecord("old")
	expired.ExpiresAt = &past
	live := newFileRecord("live")
	live.ExpiresAt = &future

	s.Require().NoError(s.repo.Create(ctx, expired))
	s.Require().NoError(s.repo.Create(ctx, live))
	s.Require().NoError(s.repo.Create(ctx, newFileRecord("forever")))

	keys, err := s.repo.DeleteExpired(ctx, time.Now())
	s.Require().NoError(err)
	s.Equal([]string{"object-old"}, keys)

	keys, err = s.repo.DeleteExpired(ctx, time.Now())
	s.Require().NoError(err)
	s.Empty(keys)

	exists, err := s.repo.Exists(ctx, live.ID)
	s.Require().NoError(err)
	s.True(exists)
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
