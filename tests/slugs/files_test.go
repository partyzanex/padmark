//go:build integration

package slugs_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/lock"
	"github.com/partyzanex/padmark/internal/infra/render"
	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/files"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

// Test values for the throwaway S3 container only; s3mock accepts any credentials.
const (
	s3AccessKey   = "padmark-test"
	s3SecretKey   = "padmark-test-secret"
	s3Bucket      = "padmark-files"
	maxFileSize   = 1 << 20
	createTimeout = 30 * time.Second
)

// sharedResources returns the three tables that share slug_namespace.
func sharedResources() []string { return []string{"notes", "links", "files"} }

// startObjects starts adobe/s3mock with the test bucket. The test cleanup stops it.
func startObjects(t *testing.T) *s3store.Store {
	t.Helper()

	container, err := testcontainers.Run(t.Context(), "adobe/s3mock:3.12.0",
		testcontainers.WithEnv(map[string]string{"initialBuckets": s3Bucket}),
		testcontainers.WithExposedPorts("9090/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("9090/tcp").WithStartupTimeout(time.Minute)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })

	hostPort, err := container.PortEndpoint(t.Context(), "9090/tcp", "")
	require.NoError(t, err)

	// A remote Docker host is not loopback; the container is a throwaway test store.
	store, err := s3store.New(&s3store.Config{
		Endpoint: "http://" + hostPort, Bucket: s3Bucket, Region: "us-east-1",
		AccessKey: s3AccessKey, SecretKey: s3SecretKey, PathStyle: true,
	}, slog.New(slog.DiscardHandler), s3store.AllowInsecureHTTP())
	require.NoError(t, err)

	return store
}

type pausedFiles struct {
	files.Storage

	gate *createGate
}

func (r *pausedFiles) Create(ctx context.Context, file *domain.File) error {
	err := r.gate.wait(ctx)
	if err != nil {
		return err
	}

	return r.Storage.Create(ctx, file)
}

// slugEnv is one server replica: a database pool, a slug lock and the three managers, wired as
// in infra/server. Each repository Create waits for gate.
type slugEnv struct {
	db    *bun.DB
	notes *notes.Manager
	links *links.Manager
	files *files.Manager
}

func newSlugEnv(t *testing.T, db *bun.DB, objects *s3store.Store, gate *createGate) *slugEnv {
	t.Helper()

	noteRepo, linkRepo := repositories(db)

	var fileRepo files.Storage = sqliterepo.NewFileRepository(db)
	if db.Dialect().Name() == dialect.PG {
		fileRepo = pgrepo.NewFileRepository(db)
	}

	log := slog.New(slog.DiscardHandler)
	slugLock := lock.NewLocker()
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())
	fileManager := files.NewManager(&files.Deps{
		Storage:   &pausedFiles{Storage: fileRepo, gate: gate},
		Objects:   objects,
		Cipher:    crypto.NewFileCipher(),
		Encryptor: testutil.ContentEncryptor(t, "file"),
		Hasher:    hasher,
		SlugLock:  slugLock,
		Notes:     noteRepo,
		Links:     linkRepo,
		Log:       log,
	}, maxFileSize, true)
	noteManager := notes.NewManager(&pausedNotes{Storage: noteRepo, gate: gate}, linkRepo, slugLock,
		render.NewRenderer(), testutil.ContentEncryptor(t, "note"), hasher, log, true).WithExpiredFiles(fileManager)
	linkManager := links.NewManager(&pausedLinks{Storage: linkRepo, gate: gate}, noteRepo, slugLock,
		testutil.ContentEncryptor(t, "link"), hasher, log, true).WithExpiredFiles(fileManager)

	return &slugEnv{db: db, notes: noteManager, links: linkManager, files: fileManager}
}

// create makes resource take slug through its manager and returns the edit code.
func (e *slugEnv) create(ctx context.Context, resource, slug string, burn bool) (string, error) {
	switch resource {
	case "notes":
		note, err := e.notes.Create(ctx, &domain.Note{
			ID: slug, Title: "", Content: "note",
			CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
			Privacy: nil, OwnerID: nil, ContentType: nil, EditCode: "",
			Views: 0, BurnTTL: 0, BurnAfterReading: burn, ClearExpiry: false, ShowContents: nil,
		})
		if err != nil {
			return "", err
		}

		return note.EditCode, nil
	case "links":
		link, err := e.links.Create(ctx, "https://example.com/target", links.CreateOptions{
			CustomHash: slug,
			ExpiresAt:  nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil, EditCode: "", BurnAfterClick: burn,
		})
		if err != nil {
			return "", err
		}

		return link.EditCode, nil
	default:
		file, err := e.files.Create(ctx, "slug.bin", bytes.NewReader([]byte("file payload")), files.CreateOptions{
			ExpiresAt: nil, Privacy: nil, OwnerID: nil, CustomSlug: slug, EditCode: "", BurnAfterDownload: burn,
		})
		if err != nil {
			return "", err
		}

		return file.EditCode, nil
	}
}

// objectKey returns the object key of the file with slug.
func (e *slugEnv) objectKey(t *testing.T, slug string) string {
	t.Helper()

	file, err := e.files.Peek(t.Context(), slug)
	require.NoError(t, err)

	return file.ObjectKey
}

// expireFile moves the expiry of the file record id into the past without a read.
func expireFile(t *testing.T, db bun.IDB, id string) {
	t.Helper()

	_, err := db.NewUpdate().Table("files").Set("expires_at = ?", time.Now().Add(-time.Hour)).
		Where("id = ?", id).Exec(t.Context())
	require.NoError(t, err)
}

func reader(allowBurn bool) files.AccessOptions {
	return files.AccessOptions{CallerID: uuid.Nil, Authenticated: false, AllowBurn: allowBurn, SkipPrivacy: false}
}

// testFileRelease checks that each way a file goes away releases its slug, and that a note, a
// link and a new file can then take it.
func testFileRelease(t *testing.T, db *bun.DB, objects *s3store.Store) {
	t.Helper()

	env := newSlugEnv(t, db, objects, openGate())

	for _, operation := range []string{"delete", "burn", "expiry", "sweep"} {
		t.Run(operation, func(t *testing.T) {
			slug := uuid.NewString()
			id := domain.HashSlug(slug)
			editCode, err := env.create(t.Context(), "files", slug, operation == "burn")
			require.NoError(t, err)
			assertReservation(t, db, id, "files")

			key := env.objectKey(t, slug)
			removeFile(t, env, slug, editCode, operation)
			assertReservation(t, db, id, "")
			require.ErrorIs(t, objects.Stat(t.Context(), key), domain.ErrNotFound)

			claimInTurn(t, env, slug)
		})
	}
}

func removeFile(t *testing.T, env *slugEnv, slug, editCode, operation string) {
	t.Helper()

	ctx := t.Context()

	switch operation {
	case "delete":
		require.NoError(t, env.files.Delete(ctx, slug, editCode, uuid.Nil))
	case "burn":
		burnFile(t, env, slug)
	case "expiry":
		expireFile(t, env.db, domain.HashSlug(slug))
		_, err := env.files.Info(ctx, slug, reader(false))
		require.ErrorIs(t, err, domain.ErrExpired)
	default:
		expireFile(t, env.db, domain.HashSlug(slug))
		removed, err := env.files.SweepExpired(ctx)
		require.NoError(t, err)
		require.NotZero(t, removed)
	}
}

// burnFile downloads a burn file twice. The first transfer fails: its claim keeps the record and
// the slug, and the release lets the next reader download. The second transfer is complete, and
// only its Close deletes the record.
func burnFile(t *testing.T, env *slugEnv, slug string) {
	t.Helper()

	ctx := t.Context()
	id := domain.HashSlug(slug)

	failed, err := env.files.Download(ctx, slug, reader(true))
	require.NoError(t, err)
	assertReservation(t, env.db, id, "files")

	_, err = env.files.Download(ctx, slug, reader(true))
	require.ErrorIs(t, err, domain.ErrNotFound, "a claimed file is not visible to a second reader")

	_, err = env.create(ctx, "notes", slug, false)
	require.ErrorIs(t, err, domain.ErrSlugConflict, "a claimed file keeps its slug")

	failed.Close(context.WithoutCancel(ctx))
	assertReservation(t, env.db, id, "files")

	download, err := env.files.Download(ctx, slug, reader(true))
	require.NoError(t, err, "a released claim lets the next reader download the file")

	var content bytes.Buffer

	require.NoError(t, download.Send(ctx, &content))
	require.Equal(t, "file payload", content.String())
	assertReservation(t, env.db, id, "files")
	download.Close(context.WithoutCancel(ctx))
}

// claimInTurn makes a note, then a link, then a file take the free slug. A raw delete removes
// each note and link before the next resource takes the slug.
func claimInTurn(t *testing.T, env *slugEnv, slug string) {
	t.Helper()

	id := domain.HashSlug(slug)

	for _, resource := range sharedResources() {
		_, err := env.create(t.Context(), resource, slug, false)
		require.NoError(t, err, resource)
		assertReservation(t, env.db, id, resource)

		if resource != "files" {
			_, err = env.db.NewDelete().Table(resource).Where("id = ?", id).Exec(t.Context())
			require.NoError(t, err)
			assertReservation(t, env.db, id, "")
		}
	}
}

// testExpiredFileSlug checks R-26: an expired file that the sweep did not remove yet does not
// block a note, a link or a new file with its slug. A file that is not expired still blocks.
func testExpiredFileSlug(t *testing.T, db *bun.DB, objects *s3store.Store) {
	t.Helper()

	env := newSlugEnv(t, db, objects, openGate())

	for _, claimant := range sharedResources() {
		t.Run(claimant, func(t *testing.T) {
			slug := uuid.NewString()
			id := domain.HashSlug(slug)
			_, err := env.create(t.Context(), "files", slug, false)
			require.NoError(t, err)

			key := env.objectKey(t, slug)
			_, err = env.create(t.Context(), claimant, slug, false)
			require.ErrorIs(t, err, domain.ErrSlugConflict, "a live file holds its slug")

			expireFile(t, db, id)
			assertReservation(t, db, id, "files")

			_, err = env.create(t.Context(), claimant, slug, false)
			require.NoError(t, err, "an expired file must not block the slug")
			assertReservation(t, db, id, claimant)
			require.ErrorIs(t, objects.Stat(t.Context(), key), domain.ErrNotFound, "the expired object goes too")

			if claimant != "files" {
				exists, existsErr := db.NewSelect().Table("files").Where("id = ?", id).Exists(t.Context())
				require.NoError(t, existsErr)
				require.False(t, exists)
			}
		})
	}
}

// testConcurrentFileCreate runs a note, a link and a file create with one slug on three
// replicas. Each replica has its own pool, lock and managers. All three pass the manager checks
// before any INSERT, so only the storage triggers can select one winner.
func testConcurrentFileCreate(
	t *testing.T, db *bun.DB, open func(*testing.T) *bun.DB, objects *s3store.Store, winner string,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), createTimeout)
	defer cancel()

	slug := uuid.NewString()
	gates := make(map[string]*createGate, len(sharedResources()))
	results := make(map[string]<-chan error, len(sharedResources()))

	for _, resource := range sharedResources() {
		gates[resource] = newGate()
		results[resource] = createAsync(ctx, newSlugEnv(t, open(t), objects, gates[resource]), resource, slug)
	}

	for _, resource := range sharedResources() {
		select {
		case <-gates[resource].ready:
		case <-ctx.Done():
			t.Fatalf("the %s create must reach INSERT after it saw the slug as free", resource)
		}
	}

	if winner == "simultaneous" {
		winner = raceAll(t, gates, results)
	} else {
		close(gates[winner].proceed)
		require.NoError(t, <-results[winner])

		for _, resource := range sharedResources() {
			if resource != winner {
				close(gates[resource].proceed)
				require.ErrorIs(t, <-results[resource], domain.ErrSlugConflict, resource)
			}
		}
	}

	id := domain.HashSlug(slug)
	assertReservation(t, db, id, winner)

	var count int

	require.NoError(t, db.NewRaw(`SELECT COUNT(*) FROM (
		SELECT id FROM notes WHERE id = ? UNION ALL SELECT id FROM links WHERE id = ?
		UNION ALL SELECT id FROM files WHERE id = ?
	) AS occupied`, id, id, id).Scan(t.Context(), &count))
	require.Equal(t, 1, count)
}

// raceAll lets all creates insert at the same time and returns the resource that won.
func raceAll(t *testing.T, gates map[string]*createGate, results map[string]<-chan error) string {
	t.Helper()

	for _, resource := range sharedResources() {
		close(gates[resource].proceed)
	}

	var winners []string

	for _, resource := range sharedResources() {
		err := <-results[resource]
		if err == nil {
			winners = append(winners, resource)
		} else {
			require.ErrorIs(t, err, domain.ErrSlugConflict, resource)
		}
	}

	require.Len(t, winners, 1)

	return winners[0]
}

func createAsync(ctx context.Context, env *slugEnv, resource, slug string) <-chan error {
	result := make(chan error, 1)

	go func() {
		_, err := env.create(ctx, resource, slug, false)
		result <- err
	}()

	return result
}
