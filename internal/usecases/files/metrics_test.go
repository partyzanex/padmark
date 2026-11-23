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

func (s *ManagerSuite) TestMetrics_PutFailureIsStorageError() {
	metrics := s.useStrictMetrics()

	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)
	metrics.EXPECT().IncStorageErrors(StorageOpPut)

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
}

func (s *ManagerSuite) TestMetrics_TooLargeFileIsNotStorageError() {
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

func (s *ManagerSuite) TestMetrics_StatFailureIsStorageError() {
	metrics := s.useStrictMetrics()

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(errors.New("timeout"))
	metrics.EXPECT().IncStorageErrors(StorageOpStat)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
}

func (s *ManagerSuite) TestMetrics_MissingObjectIsNotStorageError() {
	s.useStrictMetrics()

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(domain.ErrNotFound)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestMetrics_GetFailureIsStorageError() {
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

func (s *ManagerSuite) TestMetrics_BurnCountsDownloadAndDeletion() {
	ctx := s.T().Context()
	metrics := s.useStrictMetrics()

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(true), nil)
	s.expectObject("hello")
	s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).Return(nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), "obj").Return(nil)
	s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(nil)
	metrics.EXPECT().IncDownloads()
	metrics.EXPECT().IncDeleted(DeleteReasonBurn)

	download, err := s.manager.Download(ctx, "abc", anonymous(true))
	s.Require().NoError(err)
	s.Require().NoError(download.Send(ctx, io.Discard))
	download.Close(ctx)
}

// TestMetrics_DeleteKeepsObjectAfterStorageError shows the order: the record goes first, so a
// failed object delete leaves only an object that nobody can read. The counter is the signal.
func (s *ManagerSuite) TestMetrics_DeleteKeepsObjectAfterStorageError() {
	metrics := s.useStrictMetrics()

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	gomock.InOrder(
		s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), "obj").Return(nil),
		s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(errors.New("s3 down")),
	)
	metrics.EXPECT().IncDeleted(DeleteReasonUser)
	metrics.EXPECT().IncStorageErrors(StorageOpDelete).Times(1)

	s.Require().NoError(s.manager.Delete(s.T().Context(), "abc", "code", skipPrivacy()))
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
