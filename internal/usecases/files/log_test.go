package files

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// loggedObjectKey is the object key of the test record. With the bucket, it finds the content
// of one file, so it must not go to the logs.
const loggedObjectKey = "key-7f3a9c41"

// LogSuite makes sure that the log records of Manager do not contain the object key, the file
// name or the edit code.
type LogSuite struct {
	suite.Suite

	ctrl    *gomock.Controller
	storage *MockStorage
	objects *MockObjectStore
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
	s.logs = new(bytes.Buffer)

	log := slog.New(slog.NewTextHandler(s.logs, &slog.HandlerOptions{
		AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil,
	}))

	s.manager = NewManager(&Deps{
		Storage:   s.storage,
		Objects:   s.objects,
		Cipher:    passthroughCipher{},
		Encryptor: passthroughEncryptor{},
		Hasher:    identityHasher{},
		SlugLock:  noopSlugLock{},
		Notes:     NewMockSlugChecker(s.ctrl),
		Links:     NewMockSlugChecker(s.ctrl),
		Log:       log,
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

func (s *LogSuite) TestStatFailure() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(secretFile(), nil)
	s.objects.EXPECT().Stat(gomock.Any(), loggedObjectKey).Return(errors.New("timeout"))

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
	s.Contains(s.logs.String(), "check file object")
}

func (s *LogSuite) TestMissingObject() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(secretFile(), nil)
	s.objects.EXPECT().Stat(gomock.Any(), loggedObjectKey).Return(domain.ErrNotFound)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrNotFound)
	s.Contains(s.logs.String(), "file object is missing")
}

func (s *LogSuite) TestDeleteObjectFailure() {
	file := secretFile()
	s.storage.EXPECT().GetByID(gomock.Any(), domain.HashSlug("abc")).Return(file, nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), loggedObjectKey).Return(nil)
	s.objects.EXPECT().Delete(gomock.Any(), loggedObjectKey).Return(errors.New("s3 down"))

	s.Require().NoError(s.manager.Delete(s.T().Context(), "abc", "code", uuid.Nil))
	s.Contains(s.logs.String(), "delete file object")
}

func (s *LogSuite) TestUploadFailure() {
	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))

	_, err := s.manager.Create(s.T().Context(), "report.pdf", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
	s.Contains(s.logs.String(), "upload file")
}
