package sqlite

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
	migrations "github.com/partyzanex/padmark/migrations/sqlite"
)

// FileRepositorySuite uses the real migrations, so it also covers the files table and the
// slug_namespace triggers.
type FileRepositorySuite struct {
	suite.Suite

	db    *bun.DB
	repo  *FileRepository
	links *LinkRepository
	notes *NoteRepository
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
	s.notes = NewNoteRepository(db)
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

func newNoteRecord(id string) *domain.Note {
	return &domain.Note{
		ID: id, Title: "t", Content: "c", ContentType: nil, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	}
}

func fileReader(callerID uuid.UUID, authenticated bool) domain.Reader {
	return domain.Reader{CallerID: callerID, Authenticated: authenticated, AllowBurn: true, SkipPrivacy: false}
}

func newClaim(record *domain.File, token string, reader domain.Reader, now time.Time) *files.BurnClaim {
	return &files.BurnClaim{
		Now: now, Until: now.Add(time.Hour), ID: record.ID, ObjectKey: record.ObjectKey, Token: token, Reader: reader,
	}
}

func (s *FileRepositorySuite) insertOwner() uuid.UUID {
	owner := uuid.New()

	_, err := s.db.NewRaw("INSERT INTO users (id, username, totp_secret) VALUES (?, ?, ?)",
		owner, "owner-"+owner.String()[:8], "x").Exec(s.T().Context())
	s.Require().NoError(err)

	return owner
}

func (s *FileRepositorySuite) createFile(id string, change func(record *domain.File)) *domain.File {
	record := newFileRecord(id)
	if change != nil {
		change(record)
	}

	s.Require().NoError(s.repo.Create(s.T().Context(), record))

	return record
}

func (s *FileRepositorySuite) TestDelete_ReleasesSlug() {
	ctx := s.T().Context()
	record := s.createFile("a", nil)

	s.Require().ErrorIs(s.repo.Delete(ctx, record.ID, "other-object"), domain.ErrNotFound)
	s.Require().NoError(s.repo.Delete(ctx, record.ID, record.ObjectKey))
	s.Require().ErrorIs(s.repo.Delete(ctx, record.ID, record.ObjectKey), domain.ErrNotFound)

	s.Require().NoError(s.links.Create(ctx, newLinkRecord(record.ID)))
}

// TestStaleWrites_ReusedSlug covers R-24: a new file took the slug after a read. Each write with
// the object key of the old read finds no row and leaves the new file unchanged.
func (s *FileRepositorySuite) TestStaleWrites_ReusedSlug() {
	ctx := s.T().Context()
	old := s.createFile("reused", nil)
	s.Require().NoError(s.repo.Delete(ctx, old.ID, old.ObjectKey))

	current := s.createFile("reused", func(record *domain.File) {
		record.ObjectKey = "object-new"
		record.BurnAfterDownload = true
	})

	burn := false
	opts := files.UpdateOptions{ExpiresAt: nil, Privacy: nil, BurnAfterDownload: &burn, ClearExpiry: false}
	now := time.Now()

	s.Require().ErrorIs(s.repo.Update(ctx, old.ID, old.ObjectKey, opts, now), domain.ErrNotFound)
	s.Require().ErrorIs(s.repo.IncrementDownloadsVisible(ctx, old.ID, old.ObjectKey, fileReader(uuid.Nil, false)),
		domain.ErrNotFound)
	s.Require().ErrorIs(s.repo.ClaimBurn(ctx, newClaim(old, "t1", fileReader(uuid.Nil, false), now)),
		domain.ErrNotFound)
	s.Require().ErrorIs(s.repo.Delete(ctx, old.ID, old.ObjectKey), domain.ErrNotFound)

	got, err := s.repo.GetByID(ctx, current.ID)
	s.Require().NoError(err)
	s.Equal("object-new", got.ObjectKey)
	s.True(got.BurnAfterDownload)
}

func (s *FileRepositorySuite) TestClaimBurn_Lifecycle() {
	ctx := s.T().Context()
	owner := s.insertOwner()
	ownerPrivacy := domain.PrivacyOwner
	burn := s.createFile("burn", func(record *domain.File) {
		record.BurnAfterDownload = true
		record.OwnerID = &owner
		record.Privacy = &ownerPrivacy
	})
	plain := s.createFile("plain", nil)
	now := time.Now()

	s.Require().ErrorIs(s.repo.ClaimBurn(ctx, newClaim(burn, "x", fileReader(uuid.New(), true), now)),
		domain.ErrNotFound, "another user cannot claim an owner file")
	s.Require().ErrorIs(s.repo.ClaimBurn(ctx, newClaim(plain, "x", fileReader(uuid.Nil, false), now)),
		domain.ErrNotFound, "a file that is not a burn file has no claim")

	first := newClaim(burn, "t1", fileReader(owner, true), now)
	s.Require().NoError(s.repo.ClaimBurn(ctx, first))

	_, err := s.repo.GetByID(ctx, burn.ID)
	s.Require().ErrorIs(err, domain.ErrNotFound, "other readers do not see a claimed file")

	exists, err := s.repo.Exists(ctx, burn.ID)
	s.Require().NoError(err)
	s.True(exists, "a claimed file still holds its slug")

	second := newClaim(burn, "t2", fileReader(owner, true), now)
	s.Require().ErrorIs(s.repo.ClaimBurn(ctx, second), domain.ErrNotFound)
	s.Require().ErrorIs(s.repo.ReleaseBurn(ctx, second), domain.ErrNotFound, "only the claim owner releases")

	// A failed transfer releases the claim: the file stays for the next reader.
	s.Require().NoError(s.repo.ReleaseBurn(ctx, first))

	got, err := s.repo.GetByID(ctx, burn.ID)
	s.Require().NoError(err)
	s.Equal(burn.ObjectKey, got.ObjectKey)

	// A complete transfer deletes the record.
	s.Require().NoError(s.repo.ClaimBurn(ctx, second))
	s.Require().NoError(s.repo.Delete(ctx, burn.ID, burn.ObjectKey))
	s.Require().ErrorIs(s.repo.ReleaseBurn(ctx, second), domain.ErrNotFound)

	exists, err = s.repo.Exists(ctx, burn.ID)
	s.Require().NoError(err)
	s.False(exists)
}

func (s *FileRepositorySuite) TestClaimBurn_SkipPrivacy() {
	ctx := s.T().Context()
	owner := s.insertOwner()
	ownerPrivacy := domain.PrivacyOwner
	burn := s.createFile("burn", func(record *domain.File) {
		record.BurnAfterDownload = true
		record.OwnerID = &owner
		record.Privacy = &ownerPrivacy
	})

	reader := fileReader(uuid.Nil, false)
	s.Require().ErrorIs(s.repo.ClaimBurn(ctx, newClaim(burn, "t1", reader, time.Now())), domain.ErrNotFound)

	reader.SkipPrivacy = true
	s.Require().NoError(s.repo.ClaimBurn(ctx, newClaim(burn, "t1", reader, time.Now())))
}

// TestClaimBurn_StaleClaim covers a server that stopped during a transfer. Its claim becomes
// stale, the file is visible again, and the old claim cannot release the new one.
func (s *FileRepositorySuite) TestClaimBurn_StaleClaim() {
	ctx := s.T().Context()
	burn := s.createFile("burn", func(record *domain.File) { record.BurnAfterDownload = true })
	reader := fileReader(uuid.Nil, false)

	stale := newClaim(burn, "t1", reader, time.Now().Add(-2*time.Hour))
	s.Require().NoError(s.repo.ClaimBurn(ctx, stale))

	_, err := s.repo.GetByID(ctx, burn.ID)
	s.Require().NoError(err, "a stale claim does not hide the file")

	fresh := newClaim(burn, "t2", reader, time.Now())
	s.Require().NoError(s.repo.ClaimBurn(ctx, fresh))
	s.Require().ErrorIs(s.repo.ReleaseBurn(ctx, stale), domain.ErrNotFound)

	_, err = s.repo.GetByID(ctx, burn.ID)
	s.Require().ErrorIs(err, domain.ErrNotFound, "the new claim hides the file")
	s.Require().NoError(s.repo.ReleaseBurn(ctx, fresh))
}

func (s *FileRepositorySuite) TestClaimBurn_ExpiredFile() {
	past := time.Now().Add(-time.Minute)
	burn := s.createFile("burn", func(record *domain.File) {
		record.BurnAfterDownload = true
		record.ExpiresAt = &past
	})

	err := s.repo.ClaimBurn(s.T().Context(), newClaim(burn, "t1", fileReader(uuid.Nil, false), time.Now()))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// TestClaimBurn_ParallelClaims makes sure that of parallel downloads only one gets the file.
func (s *FileRepositorySuite) TestClaimBurn_ParallelClaims() {
	const readers = 16

	ctx := s.T().Context()
	burn := s.createFile("burn", func(record *domain.File) { record.BurnAfterDownload = true })

	var (
		wg      sync.WaitGroup
		mutex   sync.Mutex
		winners int
		errs    []error
	)

	for i := range readers {
		wg.Go(func() {
			claim := newClaim(burn, uuid.NewString(), fileReader(uuid.Nil, false), time.Now())
			err := s.repo.ClaimBurn(ctx, claim)

			mutex.Lock()
			defer mutex.Unlock()

			switch {
			case err == nil:
				winners++
			case !errors.Is(err, domain.ErrNotFound):
				errs = append(errs, fmt.Errorf("reader %d: %w", i, err))
			}
		})
	}

	wg.Wait()
	s.Empty(errs)
	s.Equal(1, winners)
}

func (s *FileRepositorySuite) TestIncrementDownloadsVisible() {
	ctx := s.T().Context()
	authenticated := domain.PrivacyAuthenticated
	record := s.createFile("a", func(record *domain.File) { record.Privacy = &authenticated })

	anonymous := fileReader(uuid.Nil, false)
	s.Require().ErrorIs(s.repo.IncrementDownloadsVisible(ctx, record.ID, record.ObjectKey, anonymous), domain.ErrNotFound)

	user := fileReader(uuid.New(), true)
	s.Require().NoError(s.repo.IncrementDownloadsVisible(ctx, record.ID, record.ObjectKey, user))

	skip := fileReader(uuid.Nil, false)
	skip.SkipPrivacy = true
	s.Require().NoError(s.repo.IncrementDownloadsVisible(ctx, record.ID, record.ObjectKey, skip))

	got, err := s.repo.GetByID(ctx, record.ID)
	s.Require().NoError(err)
	s.Equal(2, got.Downloads)

	burn := s.createFile("burn", func(record *domain.File) { record.BurnAfterDownload = true })
	s.Require().ErrorIs(s.repo.IncrementDownloadsVisible(ctx, burn.ID, burn.ObjectKey, fileReader(uuid.Nil, false)),
		domain.ErrNotFound, "a burn file is claimed, not counted")

	past := time.Now().Add(-time.Minute)
	expired := s.createFile("old", func(record *domain.File) { record.ExpiresAt = &past })
	s.Require().ErrorIs(
		s.repo.IncrementDownloadsVisible(ctx, expired.ID, expired.ObjectKey, fileReader(uuid.Nil, false)),
		domain.ErrNotFound, "an expired file is not counted")
}

func (s *FileRepositorySuite) TestListExpired() {
	var none files.ExpiredCursor

	ctx := s.T().Context()
	now := time.Now()

	for i, age := range []time.Duration{3 * time.Hour, time.Hour, 2 * time.Hour} {
		expires := now.Add(-age)

		s.createFile(fmt.Sprintf("old-%d", i), func(record *domain.File) { record.ExpiresAt = &expires })
	}

	future := now.Add(time.Hour)

	s.createFile("live", func(record *domain.File) { record.ExpiresAt = &future })
	s.createFile("forever", nil)

	// A file that a download claimed before it expired stays until the claim ends.
	expiresSoon := now.Add(-time.Minute)
	claimed := s.createFile("claimed", func(record *domain.File) {
		record.ExpiresAt = &expiresSoon
		record.BurnAfterDownload = true
	})
	claim := newClaim(claimed, "t1", fileReader(uuid.Nil, false), now.Add(-30*time.Minute))
	s.Require().NoError(s.repo.ClaimBurn(ctx, claim))

	batch, err := s.repo.ListExpired(ctx, now, none, 2)
	s.Require().NoError(err)
	s.Require().Len(batch, 2)
	s.Equal("object-old-0", batch[0].ObjectKey, "the oldest expiry comes first")
	s.Equal(domain.HashSlug("old-0"), batch[0].ID)
	s.Require().NotNil(batch[0].ExpiresAt, "ListExpired sets ExpiresAt for the cursor")
	s.Equal("object-old-2", batch[1].ObjectKey)

	batch, err = s.repo.ListExpired(ctx, now, none, 10)
	s.Require().NoError(err)
	s.Len(batch, 3)

	batch, err = s.repo.ListExpired(ctx, now.Add(-4*time.Hour), none, 10)
	s.Require().NoError(err)
	s.Empty(batch)
}

// TestListExpired_CursorPagesThroughTies pages through five records, three of them with the
// same expiry, and makes sure that each record comes once, in the order of expiry, then ID. A
// page ends inside the tied records, so the cursor must break the tie by ID.
func (s *FileRepositorySuite) TestListExpired_CursorPagesThroughTies() {
	ctx := s.T().Context()
	now := time.Now()
	oldest := now.Add(-3 * time.Hour)
	tied := now.Add(-2 * time.Hour)
	newest := now.Add(-time.Hour)

	for name, expires := range map[string]*time.Time{"a": &tied, "b": &tied, "c": &tied, "d": &oldest, "e": &newest} {
		s.createFile(name, func(record *domain.File) { record.ExpiresAt = expires })
	}

	tiedIDs := []string{domain.HashSlug("a"), domain.HashSlug("b"), domain.HashSlug("c")}
	slices.Sort(tiedIDs)

	want := append([]string{domain.HashSlug("d")}, tiedIDs...)
	want = append(want, domain.HashSlug("e"))

	var (
		got    []string
		cursor files.ExpiredCursor
	)

	for range want {
		page, err := s.repo.ListExpired(ctx, now, cursor, 2)
		s.Require().NoError(err)

		if len(page) == 0 {
			break
		}

		for _, record := range page {
			s.Require().NotNil(record.ExpiresAt, "ListExpired sets ExpiresAt")
			s.NotEmpty(record.ObjectKey)

			got = append(got, record.ID)
		}

		last := page[len(page)-1]
		cursor = files.ExpiredCursor{ExpiresAt: *last.ExpiresAt, ID: last.ID}
	}

	s.Equal(want, got)

	page, err := s.repo.ListExpired(ctx, now, cursor, 2)
	s.Require().NoError(err)
	s.Empty(page, "nothing is after the last record")
}

func (s *FileRepositorySuite) TestDeleteExpired() {
	ctx := s.T().Context()
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	expired := s.createFile("old", func(record *domain.File) { record.ExpiresAt = &past })
	live := s.createFile("live", func(record *domain.File) { record.ExpiresAt = &future })

	s.Require().ErrorIs(s.repo.DeleteExpired(ctx, expired.ID, "other-object", now), domain.ErrNotFound)
	s.Require().ErrorIs(s.repo.DeleteExpired(ctx, live.ID, live.ObjectKey, now), domain.ErrNotFound,
		"a live file stays")
	s.Require().NoError(s.repo.DeleteExpired(ctx, expired.ID, expired.ObjectKey, now))
	s.Require().ErrorIs(s.repo.DeleteExpired(ctx, expired.ID, expired.ObjectKey, now), domain.ErrNotFound)

	exists, err := s.repo.Exists(ctx, live.ID)
	s.Require().NoError(err)
	s.True(exists)
}

// TestExpiredFileAndNoteSlug covers R-26 in storage: an expired file holds its slug until its
// record goes. After DeleteExpired, a note can take the slug.
func (s *FileRepositorySuite) TestExpiredFileAndNoteSlug() {
	ctx := s.T().Context()
	past := time.Now().Add(-time.Minute)
	expired := s.createFile("shared", func(record *domain.File) { record.ExpiresAt = &past })

	s.Require().ErrorIs(s.notes.Create(ctx, newNoteRecord(expired.ID)), domain.ErrSlugConflict)
	s.Require().NoError(s.repo.DeleteExpired(ctx, expired.ID, expired.ObjectKey, time.Now()))
	s.Require().NoError(s.notes.Create(ctx, newNoteRecord(expired.ID)))
}

// TestUpdate_WritesOnlyChangedColumns covers R-25: two updates of different fields from old
// reads both stay.
func (s *FileRepositorySuite) TestUpdate_WritesOnlyChangedColumns() {
	ctx := s.T().Context()
	authenticated := domain.PrivacyAuthenticated
	public := domain.PrivacyPublic
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	record := s.createFile("upd", func(record *domain.File) {
		record.Privacy = &authenticated
		record.ExpiresAt = &expires
	})
	user := fileReader(uuid.New(), true)
	s.Require().NoError(s.repo.IncrementDownloadsVisible(ctx, record.ID, record.ObjectKey, user))

	privacyOnly := files.UpdateOptions{ExpiresAt: nil, Privacy: &public, BurnAfterDownload: nil, ClearExpiry: false}
	s.Require().NoError(s.repo.Update(ctx, record.ID, record.ObjectKey, privacyOnly, time.Now()))

	burn := true
	burnOnly := files.UpdateOptions{ExpiresAt: nil, Privacy: nil, BurnAfterDownload: &burn, ClearExpiry: false}
	s.Require().NoError(s.repo.Update(ctx, record.ID, record.ObjectKey, burnOnly, time.Now()))

	got, err := s.repo.GetByID(ctx, record.ID)
	s.Require().NoError(err)
	s.Equal(domain.PrivacyPublic, got.EffectivePrivacy(), "the second update keeps the first change")
	s.True(got.BurnAfterDownload)
	s.Require().NotNil(got.ExpiresAt)
	s.True(expires.Equal(*got.ExpiresAt), "an update without an expiry keeps the expiry")
	s.Equal("object-upd", got.ObjectKey)
	s.Equal("sealed", got.SealedMeta)
	s.Equal(int64(42), got.Size)
	s.Equal(1, got.Downloads)

	later := expires.Add(time.Hour)
	newExpiry := files.UpdateOptions{ExpiresAt: &later, Privacy: nil, BurnAfterDownload: nil, ClearExpiry: false}
	s.Require().NoError(s.repo.Update(ctx, record.ID, record.ObjectKey, newExpiry, time.Now()))

	got, err = s.repo.GetByID(ctx, record.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.ExpiresAt)
	s.True(later.Equal(*got.ExpiresAt))

	clearExpiry := files.UpdateOptions{ExpiresAt: &later, Privacy: nil, BurnAfterDownload: nil, ClearExpiry: true}
	s.Require().NoError(s.repo.Update(ctx, record.ID, record.ObjectKey, clearExpiry, time.Now()))

	got, err = s.repo.GetByID(ctx, record.ID)
	s.Require().NoError(err)
	s.Nil(got.ExpiresAt, "ClearExpiry has priority over ExpiresAt")

	var noChange files.UpdateOptions
	s.Require().NoError(s.repo.Update(ctx, record.ID, record.ObjectKey, noChange, time.Now()))
}

func (s *FileRepositorySuite) TestUpdate_NotFound() {
	ctx := s.T().Context()
	burn := true
	opts := files.UpdateOptions{ExpiresAt: nil, Privacy: nil, BurnAfterDownload: &burn, ClearExpiry: false}

	missing := newFileRecord("missing")
	s.Require().ErrorIs(s.repo.Update(ctx, missing.ID, missing.ObjectKey, opts, time.Now()), domain.ErrNotFound)

	past := time.Now().Add(-time.Minute)
	expired := s.createFile("old", func(record *domain.File) { record.ExpiresAt = &past })
	s.Require().ErrorIs(s.repo.Update(ctx, expired.ID, expired.ObjectKey, opts, time.Now()), domain.ErrNotFound,
		"an update must not extend the life of an expired file")
}

// TestMigrateDown_WithFileRows runs the files migration Down on a database with file rows. Down
// removes the files and their slug reservations, and keeps the notes and links.
func (s *FileRepositorySuite) TestMigrateDown_WithFileRows() {
	const versionBeforeFiles = 20261110000001

	ctx := s.T().Context()
	s.createFile("one", nil)
	s.createFile("two", func(record *domain.File) { record.BurnAfterDownload = true })
	s.Require().NoError(s.links.Create(ctx, newLinkRecord(domain.HashSlug("link"))))

	provider, err := goose.NewProvider(goose.DialectSQLite3, s.db.DB, migrations.FS)
	s.Require().NoError(err)

	_, err = provider.DownTo(ctx, versionBeforeFiles)
	s.Require().NoError(err)

	var tables int

	s.Require().NoError(s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'files'").Scan(&tables))
	s.Zero(tables, "Down must drop files")

	var reservations, linkReservations int

	s.Require().NoError(s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM slug_namespace WHERE resource = 'files'").Scan(&reservations))
	s.Zero(reservations, "Down must remove the file reservations")
	s.Require().NoError(s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM slug_namespace WHERE resource = 'links'").Scan(&linkReservations))
	s.Equal(1, linkReservations)

	// The slug of a removed file is free for a link after Down.
	s.Require().NoError(s.links.Create(ctx, newLinkRecord(domain.HashSlug("one"))))

	_, err = provider.Up(ctx)
	s.Require().NoError(err)
	s.createFile("three", nil)
}
