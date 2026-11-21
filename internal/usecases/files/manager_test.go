package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// passthroughEncryptor returns the metadata unchanged.
type passthroughEncryptor struct{}

func (passthroughEncryptor) Encrypt(pt, _ string) (string, error)    { return pt, nil }
func (passthroughEncryptor) Decrypt(ct, _, _ string) (string, error) { return ct, nil }

// identityHasher keeps edit codes as plaintext; a user code gets the "user:" prefix, so tests
// can see which hash the manager selected.
type identityHasher struct{}

func (identityHasher) Hash(code string) (string, error)             { return code, nil }
func (identityHasher) HashUserProvided(code string) (string, error) { return "user:" + code, nil }
func (identityHasher) Verify(hash, code string) bool                { return hash == code || hash == "user:"+code }

// passthroughCipher copies the content without a change and uses a fixed key.
type passthroughCipher struct{}

func (passthroughCipher) NewKey() ([]byte, error) { return []byte("key"), nil }

func (passthroughCipher) Encrypt(_ []byte, dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)

	return err
}

func (passthroughCipher) Decrypt(_ []byte, dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)

	return err
}

type noopSlugLock struct{}

func (noopSlugLock) Lock(context.Context, string) (func(), error) { return func() {}, nil }

const testMeta = `{"name":"report.pdf","key":"a2V5"}`

type ManagerSuite struct {
	suite.Suite

	ctrl    *gomock.Controller
	storage *MockStorage
	objects *MockObjectStore
	notes   *MockSlugChecker
	links   *MockSlugChecker
	metrics *MockMetrics
	manager *Manager
}

func TestManagerSuite(t *testing.T) {
	suite.Run(t, new(ManagerSuite))
}

func (s *ManagerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.storage = NewMockStorage(s.ctrl)
	s.objects = NewMockObjectStore(s.ctrl)
	s.notes = NewMockSlugChecker(s.ctrl)
	s.links = NewMockSlugChecker(s.ctrl)
	s.metrics = NewMockMetrics(s.ctrl)
	s.allowMetrics()
	s.manager = NewManager(&Deps{
		Storage:   s.storage,
		Objects:   s.objects,
		Cipher:    passthroughCipher{},
		Encryptor: passthroughEncryptor{},
		Hasher:    identityHasher{},
		SlugLock:  noopSlugLock{},
		Notes:     s.notes,
		Links:     s.links,
		Log:       slog.New(slog.DiscardHandler),
	}, 10, true).WithMetrics(s.metrics)
}

// allowMetrics permits all metric calls. The tests in metrics_test.go use a strict mock.
func (s *ManagerSuite) allowMetrics() {
	s.metrics.EXPECT().IncUploads().AnyTimes()
	s.metrics.EXPECT().ObserveUploadBytes(gomock.Any()).AnyTimes()
	s.metrics.EXPECT().IncDownloads().AnyTimes()
	s.metrics.EXPECT().IncDeleted(gomock.Any()).AnyTimes()
	s.metrics.EXPECT().IncStorageErrors(gomock.Any()).AnyTimes()
}

func (s *ManagerSuite) TearDownTest() {
	s.ctrl.Finish()
}

func createOptions() CreateOptions {
	return CreateOptions{
		ExpiresAt: nil, Privacy: nil, OwnerID: nil, CustomSlug: "", EditCode: "", BurnAfterDownload: false,
	}
}

func anonymous(allowBurn bool) AccessOptions {
	return AccessOptions{CallerID: uuid.Nil, Authenticated: false, AllowBurn: allowBurn, SkipPrivacy: false}
}

func (s *ManagerSuite) expectFreeSlug() {
	s.notes.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.links.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
	s.storage.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, nil)
}

func (s *ManagerSuite) expectUpload(want string) {
	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, body io.Reader) error {
			got, err := io.ReadAll(body)
			s.Require().NoError(err)
			s.Equal(want, string(got))

			return nil
		})
}

// expectUploadSweep expects the small sweep after an upload. It finds no expired file.
func (s *ManagerSuite) expectUploadSweep() {
	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), uploadSweepBatchSize).Return(nil, nil)
}

func (s *ManagerSuite) TestCreate_StoresFileAndReturnsSlug() {
	ctx := s.T().Context()
	old := fileRef("old-id", "old")

	s.expectUpload("hello")
	s.expectFreeSlug()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, file *domain.File) error {
			s.Len(file.ID, 64, "storage gets the hashed slug")
			s.Contains(file.SealedMeta, `"name":"a.txt"`)
			s.Equal(int64(5), file.Size)

			return nil
		})
	gomock.InOrder(
		s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), uploadSweepBatchSize).
			Return([]*domain.File{old}, nil),
		s.objects.EXPECT().Delete(gomock.Any(), "old").Return(nil),
		s.storage.EXPECT().DeleteExpired(gomock.Any(), "old-id", "old", gomock.Any()).Return(nil),
	)

	file, err := s.manager.Create(ctx, "dir/a.txt", strings.NewReader("hello"), createOptions())
	s.Require().NoError(err)
	s.NotEmpty(file.ID)
	s.NotEqual(domain.HashSlug(file.ID), file.ID)
	s.Equal("a.txt", file.Name)
	s.NotEmpty(file.EditCode)
	s.Equal(int64(5), file.Size)
	s.Nil(file.ContentKey, "the caller does not get the content key")
}

func (s *ManagerSuite) TestCreate_UserEditCodeUsesMemoryHardHash() {
	s.expectUpload("x")
	s.expectUploadSweep()
	s.expectFreeSlug()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, file *domain.File) error {
			s.Equal("user:my-secret-code", file.EditCode)

			return nil
		})

	opts := createOptions()
	opts.EditCode = "my-secret-code"

	file, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), opts)
	s.Require().NoError(err)
	s.Equal("my-secret-code", file.EditCode)
}

func (s *ManagerSuite) TestCreate_RejectsTooLargeFile() {
	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, body io.Reader) error {
			_, err := io.ReadAll(body)

			return err
		})
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("01234567890"), createOptions())
	s.Require().ErrorIs(err, domain.ErrFileTooLarge)
}

func (s *ManagerSuite) TestCreate_AcceptsFileAtLimit() {
	s.expectUpload("0123456789")
	s.expectUploadSweep()
	s.expectFreeSlug()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	file, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("0123456789"), createOptions())
	s.Require().NoError(err)
	s.Equal(int64(10), file.Size)
}

func (s *ManagerSuite) TestCreate_StorageFailureIsUnavailable() {
	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
}

func (s *ManagerSuite) TestCreate_BrokenUploadIsNotAStorageFailure() {
	broken := io.MultiReader(strings.NewReader("ab"), iotestErrReader{})

	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, body io.Reader) error {
			_, err := io.ReadAll(body)

			return err
		})
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Create(s.T().Context(), "a", broken, createOptions())
	s.Require().Error(err)
	s.NotErrorIs(err, domain.ErrStorageUnavailable)
}

func (s *ManagerSuite) TestCreate_CutBodyIsNotStored() {
	// A multipart part without its closing boundary ends with io.ErrUnexpectedEOF.
	cut := io.MultiReader(strings.NewReader("ab"), iotest.ErrReader(io.ErrUnexpectedEOF))

	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, body io.Reader) error {
			_, err := io.ReadAll(body)

			return err
		})
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)
	// No Storage.Create expectation: the strict mock fails the test if a record is written.

	file, err := s.manager.Create(s.T().Context(), "a", cut, createOptions())
	s.Require().ErrorIs(err, io.ErrUnexpectedEOF)
	s.Require().NotErrorIs(err, domain.ErrStorageUnavailable)
	s.Nil(file)
}

func (s *ManagerSuite) TestCreate_RemovesObjectWhenRecordFails() {
	s.expectUpload("x")
	s.expectFreeSlug()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict)
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, domain.ErrSlugConflict)
}

func (s *ManagerSuite) TestCreate_CustomSlugTakenByNote() {
	s.expectUpload("x")
	s.notes.EXPECT().Exists(gomock.Any(), domain.HashSlug("report")).Return(true, nil)
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	opts := createOptions()
	opts.CustomSlug = "report"

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), opts)
	s.Require().ErrorIs(err, domain.ErrSlugConflict)
}

func (s *ManagerSuite) TestCreate_RejectsInvalidInputBeforeUpload() {
	ctx := s.T().Context()

	_, err := s.manager.Create(ctx, "  ", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, domain.ErrInvalidFileName)

	opts := createOptions()
	opts.CustomSlug = "a/b"
	_, err = s.manager.Create(ctx, "a", strings.NewReader("x"), opts)
	s.Require().ErrorIs(err, domain.ErrInvalidSlug)

	owner := domain.PrivacyOwner
	opts = createOptions()
	opts.Privacy = &owner
	_, err = s.manager.Create(ctx, "a", strings.NewReader("x"), opts)
	s.Require().ErrorIs(err, domain.ErrOwnerPrivacyRequiresOwner)

	opts = createOptions()
	opts.EditCode = "short"
	_, err = s.manager.Create(ctx, "a", strings.NewReader("x"), opts)
	s.Require().ErrorIs(err, domain.ErrEditCodeTooShort)
}

func storedFile(burn bool) *domain.File {
	return &domain.File{
		CreatedAt:         time.Now(),
		ExpiresAt:         nil,
		Privacy:           nil,
		OwnerID:           nil,
		ID:                domain.HashSlug("abc"),
		ObjectKey:         "obj",
		SealedMeta:        testMeta,
		Name:              "",
		ContentKey:        nil,
		EditCode:          "code",
		Size:              5,
		Downloads:         0,
		BurnAfterDownload: burn,
	}
}

// fileRef returns a stored file with the record ID id and the object key key.
func fileRef(id, key string) *domain.File {
	file := storedFile(false)
	file.ID = id
	file.ObjectKey = key

	return file
}

func (s *ManagerSuite) expectObject(content string) {
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.objects.EXPECT().WriteTo(gomock.Any(), "obj", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, dst io.Writer) error {
			_, err := io.WriteString(dst, content)

			return err
		})
}

func (s *ManagerSuite) TestDownload_CountsAndDecrypts() {
	ctx := s.T().Context()

	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("abc")).Return(storedFile(false), nil)
	s.expectObject("hello")
	s.storage.EXPECT().IncrementDownloadsVisible(gomock.Any(), domain.HashSlug("abc"), "obj", anonymous(false)).
		Return(nil)

	download, err := s.manager.Download(ctx, "abc", anonymous(false))
	s.Require().NoError(err)
	s.Equal("report.pdf", download.File.Name)
	s.Equal([]byte("key"), download.File.ContentKey)
	s.Equal(1, download.File.Downloads)

	var out bytes.Buffer

	s.Require().NoError(download.Send(ctx, &out))
	s.Equal("hello", out.String())
	download.Close(ctx)
}

func (s *ManagerSuite) TestDownload_BurnNeedsDeliberateAccess() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(true), nil)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrBurnIntentRequired)
}

func (s *ManagerSuite) TestDownload_BurnConsumesAndDeletesObjectOnClose() {
	ctx := s.T().Context()

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(true), nil)
	s.expectObject("hello")
	s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).Return(nil)

	download, err := s.manager.Download(ctx, "abc", anonymous(true))
	s.Require().NoError(err)
	s.Require().NoError(download.Send(ctx, io.Discard))

	gomock.InOrder(
		s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), "obj").Return(nil),
		s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(nil),
	)
	download.Close(ctx)
}

func (s *ManagerSuite) TestDownload_LostBurnRace() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(true), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).Return(domain.ErrNotFound)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(true))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestDownload_PrivateFileIsForbidden() {
	file := storedFile(false)
	privacy := domain.PrivacyAuthenticated
	file.Privacy = &privacy

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(file, nil)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrForbidden)
}

func (s *ManagerSuite) TestDownload_ExpiredFileIsRemoved() {
	file := storedFile(false)
	past := time.Now().Add(-time.Minute)
	file.ExpiresAt = &past

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(file, nil)
	gomock.InOrder(
		s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(nil),
		s.storage.EXPECT().DeleteExpired(gomock.Any(), domain.HashSlug("abc"), "obj", gomock.Any()).Return(nil),
	)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrExpired)
}

func (s *ManagerSuite) TestDownload_MissingObjectIsNotFound() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(domain.ErrNotFound)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestDownload_StorageReadFailure() {
	ctx := s.T().Context()

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().IncrementDownloadsVisible(gomock.Any(), gomock.Any(), "obj", gomock.Any()).Return(nil)
	s.objects.EXPECT().WriteTo(gomock.Any(), "obj", gomock.Any()).Return(errors.New("reset by peer"))

	download, err := s.manager.Download(ctx, "abc", anonymous(false))
	s.Require().NoError(err)
	s.Require().Error(download.Send(ctx, io.Discard))
}

func (s *ManagerSuite) TestDownload_UnknownSlug() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestDelete() {
	ctx := s.T().Context()
	owner := uuid.New()

	s.Run("with edit code", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)
		s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), "obj").Return(nil)
		s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(nil)

		s.Require().NoError(s.manager.Delete(ctx, "abc", "code", uuid.Nil))
	})

	s.Run("owner without edit code", func() {
		file := storedFile(false)
		file.OwnerID = &owner

		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(file, nil)
		s.storage.EXPECT().Delete(gomock.Any(), gomock.Any(), "obj").Return(nil)
		s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(errors.New("s3 down"))

		s.Require().NoError(s.manager.Delete(ctx, "abc", "", owner))
	})

	s.Run("wrong edit code", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(storedFile(false), nil)

		s.Require().ErrorIs(s.manager.Delete(ctx, "abc", "wrong", uuid.Nil), domain.ErrInvalidEditCode)
	})
}

func TestSizeLimitReader_EndOfStream(t *testing.T) {
	t.Parallel()

	read := func(src io.Reader) error {
		_, err := (&sizeLimitReader{src: src, limit: 10, total: 0}).Read(make([]byte, 4))

		return err
	}

	// require.Same compares the error values by identity: a wrapped error is a different value.
	t.Run("clean EOF passes unchanged", func(t *testing.T) {
		t.Parallel()

		require.Same(t, io.EOF, read(strings.NewReader("")))
	})

	t.Run("unexpected EOF is a wrapped error", func(t *testing.T) {
		t.Parallel()

		err := read(iotest.ErrReader(io.ErrUnexpectedEOF))
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		require.NotSame(t, io.ErrUnexpectedEOF, err)
	})

	t.Run("wrapped EOF is not a clean end", func(t *testing.T) {
		t.Parallel()

		err := read(iotest.ErrReader(fmt.Errorf("proxy: %w", io.EOF)))
		require.Error(t, err)
		require.NotSame(t, io.EOF, err)
	})
}

// --- helpers ---

// iotestErrReader fails every read, as a client connection that breaks.
type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
