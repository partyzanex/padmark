package files

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// loggedObjectKey is the object key of the test record. With the bucket, it finds the content
// of one file, so it must not go to the logs.
const loggedObjectKey = "key-7f3a9c41"

// LogSuite makes sure that the log records of Manager do not contain the object key, the file
// name or the edit code. A test of a storage failure also checks the error and the metrics of
// that failure.
type LogSuite struct {
	suite.Suite

	ctrl    *gomock.Controller
	storage *MockStorage
	objects *MockObjectStore
	// metrics permits no call. A test that checks the metrics of its failure attaches it.
	metrics *MockMetrics
	logs    *bytes.Buffer
	manager *Manager
}

func TestLogSuite(t *testing.T) {
	suite.Run(t, new(LogSuite))
}

func (s *LogSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.storage = NewMockStorage(s.ctrl)
	s.objects = NewMockObjectStore(s.ctrl)
	s.metrics = NewMockMetrics(s.ctrl)
	s.logs = new(bytes.Buffer)

	log := slog.New(slog.NewTextHandler(s.logs, &slog.HandlerOptions{
		AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil,
	}))

	s.manager = NewManager(&Deps{
		Storage:      s.storage,
		Objects:      s.objects,
		Cipher:       passthroughCipher(s.ctrl),
		Encryptor:    passthroughEncryptor(s.ctrl),
		Hasher:       identityHasher(s.ctrl),
		ExpiredSlugs: NewMockExpiredSlugReleaser(s.ctrl),
		Log:          log,
		Now:          testNow,
	}, 10, true)
}

func (s *LogSuite) TearDownTest() {
	s.ctrl.Finish()

	logs := s.logs.String()
	s.NotEmpty(logs, "the scenario must write a log record")
	s.NotContains(logs, loggedObjectKey)
	s.NotContains(logs, "report.pdf")
	s.NotContains(logs, "code")
}

func secretFile() *domain.File {
	file := storedFile(false)
	file.ObjectKey = loggedObjectKey

	return file
}

// TestDownload_StatFailureIsUnavailableCountedAndLoggedWithoutSecrets: a failed Stat gives
// ErrStorageUnavailable, counts one storage error of the stat operation and writes a log record.
func (s *LogSuite) TestDownload_StatFailureIsUnavailableCountedAndLoggedWithoutSecrets() {
	s.manager.WithMetrics(s.metrics)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(secretFile(), nil)
	s.objects.EXPECT().Stat(gomock.Any(), loggedObjectKey).Return(errors.New("timeout"))
	s.metrics.EXPECT().IncStorageErrors(StorageOpStat)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
	s.Contains(s.logs.String(), "check file object")
}

// TestDownload_MissingObjectIsNotFoundNotCountedAndLoggedWithoutSecrets: a record without its
// object gives ErrNotFound and writes a log record. The metrics mock gets no storage error.
func (s *LogSuite) TestDownload_MissingObjectIsNotFoundNotCountedAndLoggedWithoutSecrets() {
	s.manager.WithMetrics(s.metrics)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(secretFile(), nil)
	s.objects.EXPECT().Stat(gomock.Any(), loggedObjectKey).Return(domain.ErrNotFound)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrNotFound)
	s.Contains(s.logs.String(), "file object is missing")
}

// TestDelete_ObjectFailureAfterRecordIsCountedAndLoggedWithoutSecrets shows the order: the record
// goes first, so a failed object delete leaves only an object that nobody can read. The delete
// succeeds, counts one user deletion and one storage error, and writes a log record.
func (s *LogSuite) TestDelete_ObjectFailureAfterRecordIsCountedAndLoggedWithoutSecrets() {
	s.manager.WithMetrics(s.metrics)

	file := secretFile()
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("abc"), testNow()).Return(file, nil)
	gomock.InOrder(
		s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), loggedObjectKey).Return(nil),
		s.objects.EXPECT().Delete(gomock.Any(), loggedObjectKey).Return(errors.New("s3 down")),
	)
	s.metrics.EXPECT().IncDeleted(DeleteReasonUser)
	s.metrics.EXPECT().IncStorageErrors(StorageOpDelete)

	s.Require().NoError(s.manager.Delete(s.T().Context(), "abc", "code", skipPrivacy()))
	s.Contains(s.logs.String(), "delete file object")
}

// TestCreate_FailedPutCleanupIsPutErrorAndLoggedWithoutSecrets covers R-34: during an object
// storage failure the cleanup after a failed Put fails too. The upload gives
// ErrStorageUnavailable and counts one storage error of the put operation, not a delete error.
func (s *LogSuite) TestCreate_FailedPutCleanupIsPutErrorAndLoggedWithoutSecrets() {
	s.manager.WithMetrics(s.metrics)
	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))
	s.metrics.EXPECT().IncStorageErrors(StorageOpPut)

	_, err := s.manager.Create(s.T().Context(), "report.pdf", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
	s.Contains(s.logs.String(), "upload file")
}

// TestSweepStopsAfterFailStreak makes sure that the warning about a stopped sweep does not
// contain the object key.
func (s *LogSuite) TestSweepStopsAfterFailStreak() {
	batch := make([]*domain.File, 0, sweepMaxFailStreak)

	for i := range sweepMaxFailStreak {
		file := secretFile()
		file.ID = fmt.Sprintf("id-%d", i)
		expires := testNow().Add(-time.Hour)
		file.ExpiresAt = &expires

		batch = append(batch, file)
	}

	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), sweepBatchSize).Return(batch, nil)
	s.objects.EXPECT().Delete(gomock.Any(), loggedObjectKey).Return(errors.New("s3 down")).Times(sweepMaxFailStreak)

	removed, err := s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
	s.Zero(removed)
	s.Contains(s.logs.String(), "file sweep stopped")
}
