//go:build integration

package s3store_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// Test values for the throwaway container only; s3mock accepts any credentials.
const (
	testAccessKey = "padmark-test"
	testSecretKey = "padmark-test-secret"
	testBucket    = "padmark-files"
)

// StoreSuite runs Store and files.Manager against an S3-compatible container.
type StoreSuite struct {
	suite.Suite

	container testcontainers.Container
	endpoint  string
	store     *s3store.Store
}

func TestStoreSuite(t *testing.T) {
	suite.Run(t, new(StoreSuite))
}

func (s *StoreSuite) SetupSuite() {
	ctx := s.T().Context()

	// adobe/s3mock creates the bucket at startup and does not check signatures. The MinIO
	// images need a registry login, so they cannot run in every environment.
	container, err := testcontainers.Run(ctx, "adobe/s3mock:3.12.0",
		testcontainers.WithEnv(map[string]string{"initialBuckets": testBucket}),
		testcontainers.WithExposedPorts("9090/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("9090/tcp").WithStartupTimeout(time.Minute)),
	)
	s.Require().NoError(err)

	s.container = container

	hostPort, err := container.PortEndpoint(ctx, "9090/tcp", "")
	s.Require().NoError(err)

	s.endpoint = "http://" + hostPort
	s.store = s.newStore(testBucket)
}

func (s *StoreSuite) TearDownSuite() {
	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *StoreSuite) newStore(bucket string) *s3store.Store {
	store, err := s3store.New(&s3store.Config{
		Endpoint: s.endpoint, Bucket: bucket, Region: "us-east-1",
		AccessKey: testAccessKey, SecretKey: testSecretKey, PathStyle: true,
	}, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	return store
}

// TestEnsureBucket: an existing bucket stays as it is, and a missing bucket is created once
// and then accepts objects.
func (s *StoreSuite) TestEnsureBucket() {
	ctx := s.T().Context()

	created, err := s.store.EnsureBucket(ctx)
	s.Require().NoError(err)
	s.False(created, "the bucket exists")

	store := s.newStore("created-bucket")

	created, err = store.EnsureBucket(ctx)
	s.Require().NoError(err)
	s.True(created, "a missing bucket must be created")

	created, err = store.EnsureBucket(ctx)
	s.Require().NoError(err)
	s.False(created, "the second call finds the bucket")

	s.Require().NoError(store.Put(ctx, "a", bytes.NewReader([]byte("hello"))))
	s.Require().NoError(store.Stat(ctx, "a"))
}

func (s *StoreSuite) TestObjectLifecycle() {
	ctx := s.T().Context()

	s.Require().NoError(s.store.Put(ctx, "a", bytes.NewReader([]byte("hello"))))
	s.Require().NoError(s.store.Stat(ctx, "a"))

	var out bytes.Buffer

	s.Require().NoError(s.store.WriteTo(ctx, "a", &out))
	s.Equal("hello", out.String())

	s.Require().NoError(s.store.Delete(ctx, "a"))
	s.Require().ErrorIs(s.store.Stat(ctx, "a"), domain.ErrNotFound)
	s.Require().ErrorIs(s.store.WriteTo(ctx, "a", &out), domain.ErrNotFound)
	s.Require().NoError(s.store.Delete(ctx, "a"), "delete of a missing key is not an error")
}

// TestManagerEndToEnd uploads a file larger than one multipart part through the real cipher,
// downloads it, and checks that a burn file and its object are gone after one download.
func (s *StoreSuite) TestManagerEndToEnd() {
	ctx := s.T().Context()
	manager := s.newManager()

	content := make([]byte, 17<<20+123)
	_, err := rand.Read(content)
	s.Require().NoError(err)

	opts := files.CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, CustomSlug: "", EditCode: "", BurnAfterDownload: true,
	}

	file, err := manager.Create(ctx, "big.bin", bytes.NewReader(content), opts)
	s.Require().NoError(err)
	s.Equal(int64(len(content)), file.Size)

	reader := domain.Reader{CallerID: [16]byte{}, Authenticated: false, AllowBurn: true, SkipPrivacy: false}

	download, err := manager.Download(ctx, file.ID, reader)
	s.Require().NoError(err)

	var out bytes.Buffer

	s.Require().NoError(download.Send(ctx, &out))
	download.Close(context.WithoutCancel(ctx))
	s.True(bytes.Equal(content, out.Bytes()), "downloaded content differs")

	_, err = manager.Download(ctx, file.ID, reader)
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *StoreSuite) newManager() *files.Manager {
	db, err := sqlite.Open(filepath.Join(s.T().TempDir(), "files.db"), slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.NoError(db.Close()) })

	_, err = sqlite.Migrate(s.T().Context(), db)
	s.Require().NoError(err)

	ring := &crypto.ContentKeyring{Active: "k1", Keys: map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}}

	encryptor, err := ring.ForResource("file")
	s.Require().NoError(err)

	expired := expiredslugs.New(sqlite.NewNoteRepository(db), sqlite.NewLinkRepository(db, slog.New(slog.DiscardHandler)))
	manager := files.NewManager(&files.Deps{
		Storage:      sqlite.NewFileRepository(db),
		Objects:      s.store,
		Cipher:       crypto.NewFileCipher(),
		Encryptor:    encryptor,
		Hasher:       crypto.NewEditCodeHasher(testutil.Argon2Params()),
		ExpiredSlugs: expired,
		Log:          slog.New(slog.DiscardHandler),
	}, 64<<20, true)
	expired.WithFiles(manager)

	return manager
}
