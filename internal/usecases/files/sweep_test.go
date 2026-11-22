package files

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// expiredTable is a fake list of expired records. It has the order and the cursor of
// Storage.ListExpired, so that the tests follow the position that the sweep keeps. The object
// delete fails for each object key in poison.
type expiredTable struct {
	records     []*domain.File
	poison      map[string]bool
	deleteCalls int
}

// newExpiredTable returns a table with poison failing records, then good working records.
// Each record expires one second after the previous one.
func newExpiredTable(poison, good int) *expiredTable {
	table := &expiredTable{records: nil, poison: make(map[string]bool), deleteCalls: 0}
	base := time.Now().Add(-time.Hour)

	for i := range poison + good {
		kind := "good"
		if i < poison {
			kind = "poison"
		}

		file := fileRef(fmt.Sprintf("%s-id-%03d", kind, i), fmt.Sprintf("%s-%d", kind, i))
		expires := base.Add(time.Duration(i) * time.Second)
		file.ExpiresAt = &expires

		table.records = append(table.records, file)
		table.poison[file.ObjectKey] = i < poison
	}

	return table
}

// install makes the mocks of the suite use the table.
func (t *expiredTable) install(s *ManagerSuite) {
	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(t.list).AnyTimes()
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).DoAndReturn(t.deleteObject).AnyTimes()
	s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(t.deleteRecord).AnyTimes()
}

func (t *expiredTable) list(
	_ context.Context, _ time.Time, after ExpiredCursor, limit int,
) ([]*domain.File, error) {
	var batch []*domain.File

	for _, file := range t.records {
		if len(batch) == limit {
			break
		}

		if after.IsZero() || isAfter(cursorAfter(file), after) {
			batch = append(batch, file)
		}
	}

	return batch, nil
}

// isAfter reports whether position a is after position b in the order of expiry, then ID.
func isAfter(a, b ExpiredCursor) bool {
	return a.ExpiresAt.After(b.ExpiresAt) || (a.ExpiresAt.Equal(b.ExpiresAt) && a.ID > b.ID)
}

func (t *expiredTable) deleteObject(_ context.Context, key string) error {
	t.deleteCalls++

	if t.poison[key] {
		return errors.New("access denied")
	}

	return nil
}

func (t *expiredTable) deleteRecord(_ context.Context, id, _ string, _ time.Time) error {
	for i, file := range t.records {
		if file.ID == id {
			t.records = append(t.records[:i], t.records[i+1:]...)

			return nil
		}
	}

	return domain.ErrNotFound
}

// remaining returns the number of records that still exist.
func (t *expiredTable) remaining() int { return len(t.records) }

// TestSweepExpired_FailedRecordsDoNotStopTheSweep makes sure that a record with a failed object
// delete does not hold back the records after it. The sweep passes it and removes the rest.
func (s *ManagerSuite) TestSweepExpired_FailedRecordsDoNotStopTheSweep() {
	table := newExpiredTable(sweepMaxFailStreak-1, 20)
	table.install(s)

	removed, err := s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
	s.Equal(20, removed)
	s.Equal(sweepMaxFailStreak-1, table.remaining(), "only the failed records stay")
	s.True(s.manager.backgroundPos.load().IsZero(), "the end of the list resets the position")
}

// TestSweepExpired_FailingHeadDoesNotStarveLaterFiles is the case of a head of the list that
// is larger than a batch and fails each time. Each sweep passes a part of it and keeps the
// position, so that a later sweep reaches the files after it.
func (s *ManagerSuite) TestSweepExpired_FailingHeadDoesNotStarveLaterFiles() {
	const (
		poison    = 3 * sweepBatchSize
		good      = 5
		maxSweeps = 2 * poison / sweepMaxFailStreak
	)

	table := newExpiredTable(poison, good)
	table.install(s)

	ctx := s.T().Context()
	total := 0

	for sweeps := 1; sweeps <= maxSweeps && total < good; sweeps++ {
		before := table.deleteCalls

		removed, err := s.manager.SweepExpired(ctx)
		s.Require().NoError(err)
		s.LessOrEqual(table.deleteCalls-before, sweepMaxFailStreak, "a sweep ends after a run of failures")

		total += removed
	}

	s.Equal(good, total, "the files after the failing head are removed")
	s.Equal(poison, table.remaining(), "the failed records stay for a later try")
}

// TestSweepExpired_StorageDownEndsAfterFailStreak makes sure that a sweep does not wait for one
// timeout for each file when the object storage is down.
func (s *ManagerSuite) TestSweepExpired_StorageDownEndsAfterFailStreak() {
	table := newExpiredTable(sweepBatchSize, 0)
	table.install(s)

	removed, err := s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
	s.Zero(removed)
	s.Equal(sweepMaxFailStreak, table.deleteCalls)
	s.Equal(cursorAfter(table.records[sweepMaxFailStreak-1]), s.manager.backgroundPos.load(),
		"the position is after the last failed record")
}

// TestSweepExpired_ListGetsThePositionOfTheLastPassedRecord makes sure that the next page starts
// after the last record of the previous page.
func (s *ManagerSuite) TestSweepExpired_ListGetsThePositionOfTheLastPassedRecord() {
	var start ExpiredCursor

	first := expiredBatch(sweepBatchSize, "a")
	second := expiredBatch(2, "b")

	gomock.InOrder(
		s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), start, sweepBatchSize).
			Return(first, nil),
		s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), cursorAfter(first[sweepBatchSize-1]), sweepBatchSize).
			Return(second, nil),
	)
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()

	removed, err := s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
	s.Equal(sweepBatchSize+2, removed)
}

// TestSweepExpired_RecordErrorKeepsPosition makes sure that the next sweep tries again the
// record whose delete returned a database error.
func (s *ManagerSuite) TestSweepExpired_RecordErrorKeepsPosition() {
	var start ExpiredCursor

	dbErr := errors.New("database is locked")
	batch := expiredBatch(3, "r")

	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), start, sweepBatchSize).Return(batch, nil)
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	gomock.InOrder(
		s.storage.EXPECT().DeleteExpired(gomock.Any(), "r-id-0", "r-0", gomock.Any()).Return(nil),
		s.storage.EXPECT().DeleteExpired(gomock.Any(), "r-id-1", "r-1", gomock.Any()).Return(dbErr),
	)

	removed, err := s.manager.SweepExpired(s.T().Context())
	s.Require().ErrorIs(err, dbErr)
	s.Equal(1, removed)

	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), cursorAfter(batch[0]), sweepBatchSize).
		Return(batch[1:], nil)
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	s.storage.EXPECT().DeleteExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)

	removed, err = s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
	s.Equal(2, removed)
}

// TestSweepExpired_RecordWithoutExpiryDoesNotPanic makes sure that a storage that breaks its
// contract and returns a record without ExpiresAt does not crash the sweep.
func (s *ManagerSuite) TestSweepExpired_RecordWithoutExpiryDoesNotPanic() {
	file := fileRef("n-id", "n")

	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]*domain.File{file}, nil)
	s.objects.EXPECT().Delete(gomock.Any(), "n").Return(nil)
	s.storage.EXPECT().DeleteExpired(gomock.Any(), "n-id", "n", gomock.Any()).Return(nil)

	removed, err := s.manager.SweepExpired(s.T().Context())
	s.Require().NoError(err)
	s.Equal(1, removed)
}

// TestSweepExpired_ConcurrentSweepsAreSafe lets the race detector check the position. The
// background sweep and many uploads can sweep at the same time.
func (s *ManagerSuite) TestSweepExpired_ConcurrentSweepsAreSafe() {
	ctx := s.T().Context()

	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(expiredBatch(3, "c"), nil).AnyTimes()
	s.objects.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(errors.New("s3 down")).AnyTimes()

	var group sync.WaitGroup

	for range 4 {
		group.Go(func() {
			_, err := s.manager.SweepExpired(ctx)
			s.NoError(err)
		})
		group.Go(func() { s.manager.sweepOnUpload(ctx) })
	}

	group.Wait()
}

// TestSweepOnUpload_FailingHeadDoesNotStarveLaterFiles is the same case on the upload path,
// where the background sweep can be off. A batch has only a few files, so the position
// of the upload sweep is the only way to reach the files after a failing head.
func (s *ManagerSuite) TestSweepOnUpload_FailingHeadDoesNotStarveLaterFiles() {
	const (
		poison = 2*uploadSweepBatchSize + 4
		good   = 3
	)

	table := newExpiredTable(poison, good)
	table.install(s)

	ctx := s.T().Context()

	// Two sweeps pass 16 failing records. No good file is removed yet.
	s.manager.sweepOnUpload(ctx)
	s.manager.sweepOnUpload(ctx)
	s.Equal(poison+good, table.remaining())

	s.manager.sweepOnUpload(ctx)
	s.Equal(poison, table.remaining(), "the third sweep removes the files after the failing head")
	s.True(s.manager.uploadPos.load().IsZero(), "the end of the list resets the position")
}

// TestCreate_SweepHasTotalTimeout makes sure that a slow object storage does not hold the
// response of an upload that is complete. The whole cleanup has one short limit, not 30 seconds
// for each of the eight files.
func (s *ManagerSuite) TestCreate_SweepHasTotalTimeout() {
	const limit = 50 * time.Millisecond

	s.manager.uploadSweepTimeout = limit

	s.expectUpload("hello")
	s.expectFreeSlug()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), uploadSweepBatchSize).
		Return(expiredBatch(uploadSweepBatchSize, "slow"), nil)
	// The first delete waits for the limit. The sweep then ends: the strict mocks have no other
	// object delete.
	s.objects.EXPECT().Delete(gomock.Any(), "slow-0").DoAndReturn(func(ctx context.Context, _ string) error {
		deadline, ok := ctx.Deadline()
		s.True(ok, "the delete has a deadline")
		s.LessOrEqual(time.Until(deadline), limit, "the deadline is the limit of the whole sweep")

		<-ctx.Done()

		return fmt.Errorf("delete: %w", ctx.Err())
	})

	start := time.Now()
	file, err := s.manager.Create(s.T().Context(), "a.txt", strings.NewReader("hello"), createOptions())

	s.Require().NoError(err, "a slow cleanup does not fail the upload")
	s.NotEmpty(file.ID)
	s.Less(time.Since(start), 5*time.Second)
}

// TestCreate_SweepStopsWhenTheClientLeaves makes sure that the sweep ends with the request.
func (s *ManagerSuite) TestCreate_SweepStopsWhenTheClientLeaves() {
	ctx, cancel := context.WithCancel(s.T().Context())
	defer cancel()

	s.expectUpload("hello")
	s.expectFreeSlug()
	s.storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	s.storage.EXPECT().ListExpired(gomock.Any(), gomock.Any(), gomock.Any(), uploadSweepBatchSize).
		DoAndReturn(func(context.Context, time.Time, ExpiredCursor, int) ([]*domain.File, error) {
			cancel()

			return expiredBatch(2, "gone"), nil
		})

	_, err := s.manager.Create(ctx, "a.txt", strings.NewReader("hello"), createOptions())
	s.Require().NoError(err)
}
