//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/files"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

const (
	maxFileSize  = 8 << 20
	fileSize     = 256<<10 + 17
	fileEditCode = "file-edit-code-1"
)

// newFileManager wires files.Manager like the server: it shares expired with the note and link
// managers, and expired then also removes expired files.
func newFileManager(
	testSuite *suite.Suite, storage files.Storage, expired *expiredslugs.Releaser, objects *s3store.Store,
) *files.Manager {
	manager := files.NewManager(&files.Deps{
		Storage:      storage,
		Objects:      objects,
		Cipher:       crypto.NewFileCipher(),
		Encryptor:    testutil.ContentEncryptor(testSuite.T(), "file"),
		Hasher:       crypto.NewEditCodeHasher(testutil.Argon2Params(), slog.New(slog.DiscardHandler)),
		ExpiredSlugs: expired,
		Log:          discardLog(),
		Now:          time.Now,
	}, maxFileSize, true)
	expired.WithFiles(manager)

	return manager
}

// ── Files ──.

// TestFile_Lifecycle runs a file through create, read, download, update and delete, and checks
// that delete also removes the object.
func (s *ManagerSuite) TestFile_Lifecycle() {
	ctx := s.T().Context()
	content := s.randomContent()

	created, err := s.FileManager.Create(ctx, "report.pdf", bytes.NewReader(content), s.fileOptions("file-report"))
	s.Require().NoError(err)
	s.Equal("file-report", created.ID)
	s.Equal(fileEditCode, created.EditCode)
	s.Equal(int64(len(content)), created.Size)

	peeked, err := s.FileManager.Peek(ctx, created.ID)
	s.Require().NoError(err)
	s.Equal("report.pdf", peeked.Name)
	s.Require().NoError(s.Objects.Stat(ctx, peeked.ObjectKey))

	s.Equal(content, s.downloadFile(created.ID, false))

	peeked, err = s.FileManager.Peek(ctx, created.ID)
	s.Require().NoError(err)
	s.Equal(1, peeked.Downloads)

	s.updateFile(created.ID)

	_, err = s.FileManager.Download(ctx, created.ID, domain.Reader{
		CallerID: uuid.Nil, Authenticated: false, AllowBurn: false, SkipPrivacy: false,
	})
	s.Require().ErrorIs(err, domain.ErrBurnIntentRequired, "a preview must not consume a burn file")

	s.Require().ErrorIs(s.FileManager.Delete(ctx, created.ID, "wrong-code-123", openReader()), domain.ErrInvalidEditCode)
	s.Require().NoError(s.FileManager.Delete(ctx, created.ID, fileEditCode, openReader()))

	_, err = s.FileManager.Peek(ctx, created.ID)
	s.Require().ErrorIs(err, domain.ErrNotFound)
	s.Require().ErrorIs(s.Objects.Stat(ctx, peeked.ObjectKey), domain.ErrNotFound)
}

// TestFile_BurnAfterDownload checks that one deliberate download consumes the file and its
// object.
func (s *ManagerSuite) TestFile_BurnAfterDownload() {
	ctx := s.T().Context()
	content := s.randomContent()
	opts := s.fileOptions("")
	opts.BurnAfterDownload = true

	created, err := s.FileManager.Create(ctx, "once.bin", bytes.NewReader(content), opts)
	s.Require().NoError(err)

	peeked, err := s.FileManager.Peek(ctx, created.ID)
	s.Require().NoError(err)

	s.Equal(content, s.downloadFile(created.ID, true))

	_, err = s.FileManager.Peek(ctx, created.ID)
	s.Require().ErrorIs(err, domain.ErrNotFound)
	s.Require().ErrorIs(s.Objects.Stat(ctx, peeked.ObjectKey), domain.ErrNotFound)
}

// TestFile_SharedSlugNamespace checks that notes, links and files share one slug namespace in
// both directions. notes.Manager and links.Manager do not look at files, so the storage
// triggers must refuse their slugs.
func (s *ManagerSuite) TestFile_SharedSlugNamespace() {
	ctx := s.T().Context()

	_, err := s.Manager.Create(ctx, fileTestNote("note-first"))
	s.Require().NoError(err)

	_, err = s.FileManager.Create(ctx, "a.txt", bytes.NewReader([]byte("a")), s.fileOptions("note-first"))
	s.Require().ErrorIs(err, domain.ErrSlugConflict)

	_, err = s.FileManager.Create(ctx, "b.txt", bytes.NewReader([]byte("b")), s.fileOptions("file-first"))
	s.Require().NoError(err)

	_, err = s.Manager.Create(ctx, fileTestNote("file-first"))
	s.Require().ErrorIs(err, domain.ErrSlugConflict, "a note must not take the slug of a file")

	_, err = s.LinkManager.Create(ctx, "https://example.com/", links.CreateOptions{
		CustomHash: "file-first",
		ExpiresAt:  nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil, EditCode: "", BurnAfterClick: false,
	})
	s.Require().ErrorIs(err, domain.ErrSlugConflict, "a link must not take the slug of a file")
}

// TestFile_Expiry checks both expiry paths: a read of an expired file and the sweep. Each one
// removes the record and the object.
func (s *ManagerSuite) TestFile_Expiry() {
	ctx := s.T().Context()

	read, err := s.FileManager.Create(ctx, "read.bin", bytes.NewReader(s.randomContent()), s.fileOptions(""))
	s.Require().NoError(err)
	swept, err := s.FileManager.Create(ctx, "swept.bin", bytes.NewReader(s.randomContent()), s.fileOptions(""))
	s.Require().NoError(err)

	keys := make(map[string]string, 2)

	for _, slug := range []string{read.ID, swept.ID} {
		peeked, peekErr := s.FileManager.Peek(ctx, slug)
		s.Require().NoError(peekErr)

		keys[slug] = peeked.ObjectKey

		_, updateErr := s.DB.NewUpdate().Table("files").Set("expires_at = ?", time.Now().Add(-time.Hour)).
			Where("id = ?", domain.HashSlug(slug)).Exec(ctx)
		s.Require().NoError(updateErr)
	}

	_, err = s.FileManager.Inspect(ctx, read.ID, domain.Reader{
		CallerID: uuid.Nil, Authenticated: false, AllowBurn: false, SkipPrivacy: false,
	})
	s.Require().ErrorIs(err, domain.ErrExpired)

	removed, err := s.FileManager.SweepExpired(ctx)
	s.Require().NoError(err)
	s.Equal(1, removed, "the read already removed the other file")

	for slug, key := range keys {
		_, err = s.FileManager.Peek(ctx, slug)
		s.Require().ErrorIs(err, domain.ErrNotFound, slug)
		s.Require().ErrorIs(s.Objects.Stat(ctx, key), domain.ErrNotFound, slug)
	}
}

// updateFile changes the expiry and the burn rule. A wrong edit code changes nothing.
func (s *ManagerSuite) updateFile(slug string) {
	ctx := s.T().Context()
	expiresAt := time.Now().Add(time.Hour).UTC()
	burn := true
	opts := files.UpdateOptions{ExpiresAt: &expiresAt, Privacy: nil, BurnAfterDownload: &burn, ClearExpiry: false}

	_, err := s.FileManager.Update(ctx, slug, "wrong-code-123", openReader(), opts)
	s.Require().ErrorIs(err, domain.ErrInvalidEditCode)

	updated, err := s.FileManager.Update(ctx, slug, fileEditCode, openReader(), opts)
	s.Require().NoError(err)
	s.True(updated.BurnAfterDownload)
	s.Empty(updated.EditCode)

	stored, err := s.FileManager.Peek(ctx, slug)
	s.Require().NoError(err)
	s.True(stored.BurnAfterDownload)
	s.Require().NotNil(stored.ExpiresAt)
	s.WithinDuration(expiresAt, *stored.ExpiresAt, time.Second)
}

// downloadFile downloads the file as an anonymous reader and returns its content.
func (s *ManagerSuite) downloadFile(slug string, allowBurn bool) []byte {
	ctx := s.T().Context()

	download, err := s.FileManager.Download(ctx, slug, domain.Reader{
		CallerID: uuid.Nil, Authenticated: false, AllowBurn: allowBurn, SkipPrivacy: false,
	})
	s.Require().NoError(err)

	defer download.Close(context.WithoutCancel(ctx))

	var out bytes.Buffer

	s.Require().NoError(download.Send(ctx, &out))

	return out.Bytes()
}

// fileTestNote returns a note with the custom slug.
func fileTestNote(slug string) *domain.Note {
	return &domain.Note{
		ID: slug, Title: "", Content: "note",
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		Privacy: nil, OwnerID: nil, ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	}
}

func (s *ManagerSuite) fileOptions(slug string) files.CreateOptions {
	return files.CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, CustomSlug: slug, EditCode: fileEditCode,
		BurnAfterDownload: false,
	}
}

func (s *ManagerSuite) randomContent() []byte {
	content := make([]byte, fileSize)

	_, err := rand.Read(content)
	s.Require().NoError(err)

	return content
}
