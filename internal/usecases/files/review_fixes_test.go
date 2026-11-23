package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// --- burn after download: two-phase claim (R-06) ---

func (s *ManagerSuite) TestDownload_BurnClaimDeletesAfterCompleteSend() {
	ctx := s.T().Context()

	var claimed *BurnClaim

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(true), nil)
	s.expectObject("burn content")
	s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, claim *BurnClaim) error {
		claimed = claim

		return nil
	})

	before := testNow()
	download, err := s.manager.Download(ctx, "abc", anonymous(true))
	s.Require().NoError(err)
	s.Require().NotNil(claimed)
	s.Equal(domain.HashSlug("abc"), claimed.ID)
	s.Equal("obj", claimed.ObjectKey)
	s.NotEmpty(claimed.Token)
	s.Equal(anonymous(true), claimed.Reader)
	s.WithinDuration(before.Add(burnClaimTTL), claimed.Until, time.Minute)

	var out strings.Builder

	s.Require().NoError(download.Send(ctx, &out))
	s.Equal("burn content", out.String())

	gomock.InOrder(
		s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), "obj").Return(nil),
		s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(nil),
	)
	download.Close(ctx)
	s.Nil(download.File.ContentKey, "Close clears the content key")

	// A second Close does nothing: the strict mocks have no more calls.
	download.Close(ctx)
}

func (s *ManagerSuite) TestDownload_BurnFailedSendKeepsFile() {
	ctx := s.T().Context()

	var claimed *BurnClaim

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(true), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, claim *BurnClaim) error {
		claimed = claim

		return nil
	})
	s.objects.EXPECT().WriteTo(gomock.Any(), "obj", gomock.Any()).Return(errors.New("reset by peer"))

	download, err := s.manager.Download(ctx, "abc", anonymous(true))
	s.Require().NoError(err)
	s.Require().Error(download.Send(ctx, io.Discard))

	// No record delete and no object delete: the next reader can download the file.
	s.storage.EXPECT().ReleaseBurn(gomock.Any(), claimed).Return(nil)
	download.Close(ctx)
}

func (s *ManagerSuite) TestDownload_BurnCloseWithoutSendReleasesClaim() {
	ctx := s.T().Context()

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(true), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).Return(nil)
	s.storage.EXPECT().ReleaseBurn(gomock.Any(), gomock.Any()).Return(errors.New("db down"))

	download, err := s.manager.Download(ctx, "abc", anonymous(true))
	s.Require().NoError(err)
	download.Close(ctx)
}

// TestDownload_BurnFinishFailureStillDeletesObject makes sure that a reader who got the content
// is the only one: the object goes also when the record delete fails.
func (s *ManagerSuite) TestDownload_BurnFinishFailureStillDeletesObject() {
	ctx := s.T().Context()
	metrics := s.useStrictMetrics()

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(true), nil)
	s.expectObject("hello")
	s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).Return(nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), "obj").Return(errors.New("db down"))
	s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(nil)
	metrics.EXPECT().IncDownloads()

	download, err := s.manager.Download(ctx, "abc", anonymous(true))
	s.Require().NoError(err)
	s.Require().NoError(download.Send(ctx, io.Discard))
	download.Close(ctx)
}

// TestDownload_BurnSendStopsBeforeClaimIsStale makes sure that Send has a deadline before the
// claim becomes stale. Thus a second reader cannot claim the file during the transfer.
func (s *ManagerSuite) TestDownload_BurnSendStopsBeforeClaimIsStale() {
	ctx := s.T().Context()

	var claimed *BurnClaim

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(true), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, claim *BurnClaim) error {
		claimed = claim

		return nil
	})
	s.objects.EXPECT().WriteTo(gomock.Any(), "obj", gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ string, _ io.Writer) error {
			deadline, ok := ctx.Deadline()
			s.Require().True(ok, "Send must have a deadline")
			s.True(deadline.Before(claimed.Until))

			return nil
		})
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), "obj").Return(nil)
	s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(nil)

	download, err := s.manager.Download(ctx, "abc", anonymous(true))
	s.Require().NoError(err)
	s.Require().NoError(download.Send(ctx, io.Discard))
	download.Close(ctx)
}

// --- a record that changed after the read (R-24) ---

func (s *ManagerSuite) TestDownload_CounterFindsBurnFile() {
	ctx := s.T().Context()
	changed := storedFile(true)

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().IncrementDownloadsVisible(gomock.Any(), gomock.Any(), "obj", gomock.Any(), testNow()).
		Return(domain.ErrNotFound)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(changed, nil)

	s.Run("without burn intent", func() {
		_, err := s.manager.Download(ctx, "abc", anonymous(false))
		s.Require().ErrorIs(err, domain.ErrBurnIntentRequired)
	})

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().IncrementDownloadsVisible(gomock.Any(), gomock.Any(), "obj", gomock.Any(), testNow()).
		Return(domain.ErrNotFound)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(changed, nil)
	s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).Return(nil)

	s.Run("with burn intent", func() {
		download, err := s.manager.Download(ctx, "abc", anonymous(true))
		s.Require().NoError(err)
		s.True(download.File.BurnAfterDownload)

		s.storage.EXPECT().ReleaseBurn(gomock.Any(), gomock.Any()).Return(nil)
		download.Close(ctx)
	})
}

func (s *ManagerSuite) TestDownload_SlugTakenByNewFile() {
	replaced := storedFile(false)
	replaced.ObjectKey = "new-obj"

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().IncrementDownloadsVisible(gomock.Any(), gomock.Any(), "obj", gomock.Any(), testNow()).
		Return(domain.ErrNotFound)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(replaced, nil)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestDownload_CounterFindsPrivateFile() {
	private := storedFile(false)
	authenticated := domain.PrivacyAuthenticated
	private.Privacy = &authenticated

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().IncrementDownloadsVisible(gomock.Any(), gomock.Any(), "obj", gomock.Any(), testNow()).
		Return(domain.ErrNotFound)
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(private, nil)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrForbidden)
}

func (s *ManagerSuite) TestDownload_CounterErrorDoesNotStopDownload() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
	s.storage.EXPECT().IncrementDownloadsVisible(gomock.Any(), gomock.Any(), "obj", gomock.Any(), testNow()).
		Return(errors.New("database is locked"))

	download, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().NoError(err)
	s.Zero(download.File.Downloads)
}

func (s *ManagerSuite) TestDelete_SlugTakenByNewFile() {
	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
	s.storage.EXPECT().Delete(gomock.Any(), domain.HashSlug("abc"), "obj").Return(domain.ErrNotFound)
	// No object delete: the object of the new file must stay.

	err := s.manager.Delete(s.T().Context(), "abc", "code", skipPrivacy())
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

// --- SkipPrivacy (R-23) ---

func skipPrivacy() domain.Reader {
	return domain.Reader{CallerID: uuid.Nil, Authenticated: false, AllowBurn: true, SkipPrivacy: true}
}

// privateFile returns a file that only its owner can see.
func privateFile(burn bool) *domain.File {
	file := storedFile(burn)
	owner := uuid.New()
	privacy := domain.PrivacyOwner
	file.OwnerID = &owner
	file.Privacy = &privacy

	return file
}

func (s *ManagerSuite) TestSkipPrivacy_Inspect() {
	ctx := s.T().Context()
	skip := skipPrivacy()

	s.Run("inspect", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(privateFile(false), nil)

		file, err := s.manager.Inspect(ctx, "abc", skip)
		s.Require().NoError(err)
		s.Equal("report.pdf", file.Name)
		s.Nil(file.ContentKey, "Inspect does not return the content key")
	})

	s.Run("inspect without skip", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(privateFile(false), nil)

		_, err := s.manager.Inspect(ctx, "abc", anonymous(true))
		s.Require().ErrorIs(err, domain.ErrForbidden)
	})
}

func (s *ManagerSuite) TestSkipPrivacy_Download() {
	ctx := s.T().Context()
	skip := skipPrivacy()

	s.Run("download counter", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(privateFile(false), nil)
		s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
		s.storage.EXPECT().IncrementDownloadsVisible(gomock.Any(), gomock.Any(), "obj", skip, testNow()).Return(nil)

		_, err := s.manager.Download(ctx, "abc", skip)
		s.Require().NoError(err)
	})

	s.Run("burn claim", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(privateFile(true), nil)
		s.objects.EXPECT().Stat(gomock.Any(), "obj").Return(nil)
		s.storage.EXPECT().ClaimBurn(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, claim *BurnClaim) error {
				s.True(claim.Reader.SkipPrivacy)

				return nil
			})
		s.storage.EXPECT().ReleaseBurn(gomock.Any(), gomock.Any()).Return(nil)

		download, err := s.manager.Download(ctx, "abc", skip)
		s.Require().NoError(err)
		download.Close(ctx)
	})
}

func (s *ManagerSuite) TestSkipPrivacy_ExpiryAndDelete() {
	ctx := s.T().Context()
	skip := skipPrivacy()

	s.Run("expired file", func() {
		file := privateFile(false)
		past := testNow().Add(-time.Minute)
		file.ExpiresAt = &past

		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(file, nil)
		s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(nil)
		s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), "obj", gomock.Any()).Return(nil)

		_, err := s.manager.Inspect(ctx, "abc", skip)
		s.Require().ErrorIs(err, domain.ErrExpired)
	})

	s.Run("delete still needs the edit code", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(privateFile(false), nil)

		s.Require().ErrorIs(s.manager.Delete(ctx, "abc", "wrong", skipPrivacy()), domain.ErrInvalidEditCode)
	})
}

// --- expired files: object first, bounded batches (R-07) ---

// expiredBatch returns count expired records in the order of ListExpired: each expires one
// second after the previous one.
func expiredBatch(count int, prefix string) []*domain.File {
	base := testNow().Add(-time.Hour)
	batch := make([]*domain.File, 0, count)

	for i := range count {
		file := fileRef(fmt.Sprintf("%s-id-%d", prefix, i), fmt.Sprintf("%s-%d", prefix, i))
		expires := base.Add(time.Duration(i) * time.Second)
		file.ExpiresAt = &expires

		batch = append(batch, file)
	}

	return batch
}

func (s *ManagerSuite) TestSweepExpired_ObjectFirstThenRecord() {
	metrics := s.useStrictMetrics()
	batch := expiredBatch(2, "obj")

	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), sweepBatchSize).Return(batch, nil)
	gomock.InOrder(
		s.objects.EXPECT().Delete(gomock.Any(), "obj-0").Return(nil),
		s.storage.EXPECT().DeleteExpired(gomock.Any(), "obj-id-0", "obj-0", gomock.Any()).Return(nil),
		// A failed object delete keeps the record, so the next sweep tries again.
		s.objects.EXPECT().Delete(gomock.Any(), "obj-1").Return(errors.New("s3 down")),
	)
	metrics.EXPECT().IncDeleted(DeleteReasonExpired)
	metrics.EXPECT().IncStorageErrors(StorageOpDelete)

	removed, err := s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
	s.Equal(1, removed)
}

func (s *ManagerSuite) TestSweepExpired_ContinuesWithFullBatches() {
	first := expiredBatch(sweepBatchSize, "a")
	second := expiredBatch(3, "b")

	gomock.InOrder(
		s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), sweepBatchSize).Return(first, nil),
		s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), sweepBatchSize).Return(second, nil),
	)
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil).Times(sweepBatchSize + 3)
	s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).Times(sweepBatchSize + 3)

	removed, err := s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
	s.Equal(sweepBatchSize+3, removed)
}

func (s *ManagerSuite) TestSweepExpired_StopsAfterMaxBatches() {
	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), sweepBatchSize).
		Return(expiredBatch(sweepBatchSize, "x"), nil).Times(sweepMaxBatches)
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()

	removed, err := s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
	s.Equal(sweepBatchSize*sweepMaxBatches, removed)
}

func (s *ManagerSuite) TestSweepExpired_Errors() {
	dbErr := errors.New("database is locked")

	s.Run("list", func() {
		s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, dbErr)

		_, err := s.manager.SweepExpired(s.T().Context())
		s.Require().ErrorIs(err, dbErr)
	})

	s.Run("record", func() {
		s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(expiredBatch(2, "r"), nil)
		s.objects.EXPECT().Delete(gomock.Any(), "r-0").Return(nil)
		s.storage.EXPECT().DeleteExpired(gomock.Any(), "r-id-0", "r-0", gomock.Any()).Return(dbErr)

		removed, err := s.manager.SweepExpired(s.T().Context())
		s.Require().ErrorIs(err, dbErr)
		s.Zero(removed)
	})

	s.Run("record removed by another process", func() {
		s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(expiredBatch(1, "g"), nil)
		s.objects.EXPECT().Delete(gomock.Any(), "g-0").Return(nil)
		s.storage.EXPECT().DeleteExpired(gomock.Any(), "g-id-0", "g-0", gomock.Any()).Return(domain.ErrNotFound)

		removed, err := s.manager.SweepExpired(s.T().Context())
		s.Require().NoError(err)
		s.Zero(removed)
	})

	s.Run("canceled context", func() {
		ctx, cancel := context.WithCancel(s.T().Context())
		cancel()

		s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(expiredBatch(1, "c"), nil)

		_, err := s.manager.SweepExpired(ctx)
		s.Require().ErrorIs(err, context.Canceled)
	})
}

// TestSweepExpired_ObjectDeleteHasTimeout makes sure that each object delete has a deadline, so
// that a stopped object storage cannot hold the sweep or the shutdown without end.
func (s *ManagerSuite) TestSweepExpired_ObjectDeleteHasTimeout() {
	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(expiredBatch(1, "t"), nil)
	s.objects.EXPECT().Delete(gomock.Any(), "t-0").DoAndReturn(func(ctx context.Context, _ string) error {
		_, ok := ctx.Deadline()
		s.True(ok, "an object delete must have a deadline")

		return nil
	})
	s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
}

// --- expired file that holds a slug (R-26) ---

func (s *ManagerSuite) TestReleaseExpiredSlug() {
	ctx := s.T().Context()
	id := domain.HashSlug("abc")

	s.Run("missing", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), id, testNow()).Return(nil, domain.ErrNotFound)

		released, err := s.manager.ReleaseExpiredSlug(ctx, id, testNow())
		s.Require().NoError(err)
		s.False(released)
	})

	s.Run("live file", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), id, testNow()).Return(storedFile(false), nil)

		released, err := s.manager.ReleaseExpiredSlug(ctx, id, testNow())
		s.Require().NoError(err)
		s.False(released)
	})

	s.Run("expired file", func() {
		file := storedFile(false)
		past := testNow().Add(-time.Minute)
		file.ExpiresAt = &past

		s.storage.EXPECT().GetByID(gomock.Any(), id, testNow()).Return(file, nil)
		gomock.InOrder(
			s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(nil),
			s.storage.EXPECT().DeleteExpired(gomock.Any(), id, "obj", gomock.Any()).Return(nil),
		)

		released, err := s.manager.ReleaseExpiredSlug(ctx, id, testNow())
		s.Require().NoError(err)
		s.True(released)
	})

	s.Run("object storage failure", func() {
		file := storedFile(false)
		past := testNow().Add(-time.Minute)
		file.ExpiresAt = &past

		s.storage.EXPECT().GetByID(gomock.Any(), id, testNow()).Return(file, nil)
		s.objects.EXPECT().Delete(gomock.Any(), "obj").Return(errors.New("s3 down"))

		released, err := s.manager.ReleaseExpiredSlug(ctx, id, testNow())
		s.Require().Error(err)
		s.False(released)
	})
}

// TestCreate_ExpiredRecordDoesNotHoldCustomSlug: an expired note, link or file still holds the
// custom slug in storage. Create asks the release port to remove it and tries once more.
func (s *ManagerSuite) TestCreate_ExpiredRecordDoesNotHoldCustomSlug() {
	s.expectUpload("x")
	gomock.InOrder(
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict),
		s.expired.EXPECT().ReleaseExpiredSlug(gomock.Any(), domain.HashSlug("report"), testNow()).Return(true, nil),
		s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil),
	)
	s.expectUploadSweep()

	opts := createOptions()
	opts.CustomSlug = "report"

	file, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), opts)
	s.Require().NoError(err)
	s.Equal("report", file.ID)
}

// --- static slug checks before the upload (R-21) ---

func (s *ManagerSuite) TestCreate_RejectsBadCustomSlugBeforeUpload() {
	ctx := s.T().Context()

	// No Put expectation: the strict mock fails the test if the upload starts.
	opts := createOptions()
	opts.CustomSlug = "login"
	_, err := s.manager.Create(ctx, "a", strings.NewReader("x"), opts)
	s.Require().ErrorIs(err, domain.ErrInvalidSlug)

	opts.CustomSlug = "bad slug!"
	_, err = s.manager.Create(ctx, "a", strings.NewReader("x"), opts)
	s.Require().ErrorIs(err, domain.ErrInvalidSlug)

	disabled := NewManager(s.manager.deps, 10, false)
	opts.CustomSlug = "report"
	_, err = disabled.Create(ctx, "a", strings.NewReader("x"), opts)
	s.Require().ErrorIs(err, domain.ErrCustomSlugDisabled)
}

// --- upload errors (R-34, R-39) ---

// TestCreate_PutFailureWithBrokenBodyIsStorageFailure covers R-39: Put fails for its own cause,
// and the body read also fails after that. The upload is a storage failure.
func (s *ManagerSuite) TestCreate_PutFailureWithBrokenBodyIsStorageFailure() {
	metrics := s.useStrictMetrics()

	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)
	metrics.EXPECT().IncStorageErrors(StorageOpPut)

	_, err := s.manager.Create(s.T().Context(), "a", iotestErrReader{}, createOptions())
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
}

// TestMetrics_FailedPutCleanupIsNotDeleteError covers R-34: the cleanup after a failed Put
// fails too during an object storage failure, but it is not a "delete" storage error.
func (s *ManagerSuite) TestMetrics_FailedPutCleanupIsNotDeleteError() {
	metrics := s.useStrictMetrics()

	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(errors.New("connection refused"))
	metrics.EXPECT().IncStorageErrors(StorageOpPut)

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, domain.ErrStorageUnavailable)
}

func (s *ManagerSuite) TestCreate_CanceledRequest() {
	ctx, cancel := context.WithCancel(s.T().Context())

	s.objects.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, string, io.Reader) error {
			cancel()

			return context.Canceled
		})
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ string) error {
		s.NoError(ctx.Err(), "the cleanup must run after the request is canceled")

		return nil
	})

	_, err := s.manager.Create(ctx, "a", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, context.Canceled)
	s.Require().NotErrorIs(err, domain.ErrStorageUnavailable)
}

func (s *ManagerSuite) TestCreate_RandomSlugCollisions() {
	s.expectUpload("x")
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrSlugConflict).Times(maxSlugRetries)
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)

	_, err := s.manager.Create(s.T().Context(), "a", strings.NewReader("x"), createOptions())
	s.Require().ErrorIs(err, domain.ErrHashCollision)
}

func (s *ManagerSuite) TestDownload_CorruptMetaIsNotFound() {
	file := storedFile(false)
	file.SealedMeta = "{"

	s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(file, nil)

	_, err := s.manager.Download(s.T().Context(), "abc", anonymous(false))
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *ManagerSuite) TestDelete_StorageErrors() {
	dbErr := errors.New("db down")

	s.Run("read", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(nil, dbErr)

		s.Require().ErrorIs(s.manager.Delete(s.T().Context(), "abc", "code", skipPrivacy()), dbErr)
	})

	s.Run("write", func() {
		s.storage.EXPECT().GetByID(gomock.Any(), gomock.Any(), testNow()).Return(storedFile(false), nil)
		s.storage.EXPECT().Delete(gomock.Any(), gomock.Any(), "obj").Return(dbErr)

		s.Require().ErrorIs(s.manager.Delete(s.T().Context(), "abc", "code", skipPrivacy()), dbErr)
	})
}
