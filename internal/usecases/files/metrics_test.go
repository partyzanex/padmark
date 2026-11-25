package files

import (
	"context"
	"errors"
	"io"
	"strings"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// The tests in this file replace the permissive metrics mock with a strict one. Thus an
// unexpected or a missing metric call fails the test.

// useStrictMetrics attaches the metrics mock without permitted calls and returns it.
func (s *ManagerSuite) useStrictMetrics() *MockMetrics {
	s.manager.WithMetrics(s.strictMetrics)

	return s.strictMetrics
}

func (s *ManagerSuite) TestMetrics_CreateCountsUploadAndSize() {
	metrics := s.useStrictMetrics()

	s.expectUpload("hello")
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.expectUploadSweep()
	metrics.EXPECT().IncUploads()
	metrics.EXPECT().ObserveUploadBytes(int64(5))

	_, err := s.manager.Create(s.T().Context(), "a.txt", strings.NewReader("hello"), createOptions())
	s.Require().NoError(err)
}

// TestCreate_PutFailureIsUnavailableAndCountsStorageError: a failed Put gives
// ErrStorageUnavailable and counts one storage error of the put operation.
func (s *ManagerSuite) TestCreate_PutFailureIsUnavailableAndCountsStorageError() {
	metrics := s.useStrictMetrics()

	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)
	metrics.EXPECT().IncStorageErrors(StorageOpPut)

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
}

// TestCreate_RejectsTooLargeFileWithoutStorageError: a file above the limit gives ErrFileTooLarge,
// and the strict metrics mock gets no storage error.
func (s *ManagerSuite) TestCreate_RejectsTooLargeFileWithoutStorageError() {
	s.useStrictMetrics()

	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, body io.Reader) error {
			_, err := io.ReadAll(body)

			return err
		})
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("12345678901"), createOptions())
	s.Require().ErrorIs(err, domain.ErrFileTooLarge)
}

// TestDownload_StorageReadFailureCountsStorageError: a failed object read stops Send with an
// error, after one counted download, and counts one storage error of the get operation.
func (s *ManagerSuite) TestDownload_StorageReadFailureCountsStorageError() {
	ctx := s.T().Context()
	metrics := s.useStrictMetrics()

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().IncrementDownloadsVisible(gomock.Any(), gomock.Any(), "obj", gomock.Any(), testNow()).Return(nil)
	s.objects.EXPECT().WriteTo(gomock.Any(), "obj", gomock.Any()).Return(errors.New("reset by peer"))
	metrics.EXPECT().IncDownloads()
	metrics.EXPECT().IncStorageErrors(StorageOpGet)

	download, err := s.manager.Download(ctx, "abc", anonymous(false))
	s.Require().NoError(err)
	s.Require().Error(download.Send(ctx, io.Discard))
}

func (s *ManagerSuite) TestMetrics_RecordFailureRemovesObject() {
	metrics := s.useStrictMetrics()

	s.expectUpload("x")
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(errors.New("disk full"))
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(nil, domain.ErrNotFound)
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(errors.New("s3 down"))
	metrics.EXPECT().IncStorageErrors(StorageOpDelete)

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), createOptions())
	s.Require().Error(err)
}
