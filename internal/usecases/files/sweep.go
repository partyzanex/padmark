package files

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	// sweepBatchSize and sweepMaxBatches limit one SweepExpired call to 1000 files. The next
	// sweep continues; thus one call cannot hold the database or the object storage for long.
	sweepBatchSize  = 100
	sweepMaxBatches = 10
	// sweepMaxFailStreak is the number of object delete failures in a row that ends a sweep.
	// A run of failures means that the object storage is down. Without this limit, a sweep
	// would wait for one timeout for each file of its batch.
	sweepMaxFailStreak = 10
	// uploadSweepBatchSize limits the sweep on the upload path. The background sweep can be off,
	// so uploads remove a few expired files; a small batch keeps the upload response fast.
	uploadSweepBatchSize = 8
	// uploadSweepTimeout limits the whole sweep on the upload path, not each delete. The upload
	// is already complete, so a slow object storage must not hold the response: a client with a
	// short timeout could send the same upload again.
	uploadSweepTimeout = 3 * time.Second
	// objectDeleteTimeout limits one object delete, so that a stopped object storage cannot
	// hold a sweep, a burn cleanup or a shutdown without end.
	objectDeleteTimeout = 30 * time.Second
)

// errObjectKept tells that an expired file keeps its record because its object delete failed.
var errObjectKept = errors.New("file object is not deleted")

// ExpiredCursor is a position in the list that Storage.ListExpired returns. The list is in the
// order of ExpiresAt, then ID. The cursor names the last record that a sweep passed, so that
// the next page starts after it. The zero value is the start of the list.
type ExpiredCursor struct {
	ExpiresAt time.Time
	ID        string
}

// IsZero reports whether the cursor is the start of the list.
func (c ExpiredCursor) IsZero() bool {
	return c.ExpiresAt.IsZero() && c.ID == ""
}

// sweepCursor keeps the position where the last sweep of one kind ended. A record whose object
// delete fails stays in storage, and it would stay at the head of the list. With the position,
// the next sweep goes on after it, so that the later expired files get their turn. At the end of
// the list the position goes back to the start, and the kept records get another try.
//
// The position lives in the memory of one process and is not a safety control: DeleteExpired
// checks each record again. A lost position only makes the sweep start at the oldest record.
// The mutex guards only the position; no I/O runs under it.
type sweepCursor struct {
	pos ExpiredCursor
	mu  sync.Mutex
}

func newSweepCursor() *sweepCursor {
	return &sweepCursor{pos: ExpiredCursor{ExpiresAt: time.Time{}, ID: ""}, mu: sync.Mutex{}}
}

func (c *sweepCursor) load() ExpiredCursor {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.pos
}

func (c *sweepCursor) store(pos ExpiredCursor) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pos = pos
}

// sweepRun is the state of one sweep.
type sweepRun struct {
	// pos is the last record that the sweep passed.
	pos        ExpiredCursor
	removed    int
	failStreak int
}

// SweepExpired removes expired files in batches. It returns the number of removed files.
// The object goes first, then the record with the same object key. Thus an object delete
// failure keeps the record, and a later sweep tries again; no object stays without a record.
// A failed record does not stop the sweep: it goes on to the next records, and the next call
// starts after the last record that this call passed. Only a run of failures, which means that
// the object storage is down, ends the sweep early.
// Give the context of the caller: its cancellation stops the sweep. A record error stops the
// sweep and is returned.
func (m *Manager) SweepExpired(ctx context.Context) (int, error) {
	return m.sweep(ctx, m.backgroundPos, sweepBatchSize, sweepMaxBatches)
}

// ReleaseExpiredSlug removes the file with the record ID id when it is expired at now, so that a
// note, a link or a new file can take the slug before the sweep removes it. It returns true when
// it removed the file. Errors are fail-soft for the caller: it keeps its slug conflict.
func (m *Manager) ReleaseExpiredSlug(ctx context.Context, id string, now time.Time) (bool, error) {
	file, err := m.deps.Storage.GetByID(ctx, id, now)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("get file %s: %w", id, err)
	}

	if !file.Expired(now) {
		return false, nil
	}

	err = m.removeExpired(ctx, id, file.ObjectKey, now)
	if err != nil {
		return false, err
	}

	return true, nil
}

// sweep runs one sweep from the position in cursor and stores the position where it ended.
func (m *Manager) sweep(ctx context.Context, cursor *sweepCursor, batchSize, maxBatches int) (int, error) {
	run := sweepRun{pos: cursor.load(), removed: 0, failStreak: 0}

	err := m.sweepPages(ctx, &run, batchSize, maxBatches)
	cursor.store(run.pos)

	if run.failStreak >= sweepMaxFailStreak {
		m.deps.Log.WarnContext(ctx, "file sweep stopped: object deletes fail", "failures", run.failStreak)
	}

	return run.removed, err
}

func (m *Manager) sweepPages(ctx context.Context, run *sweepRun, batchSize, maxBatches int) error {
	for range maxBatches {
		now := m.deps.Now()

		batch, err := m.deps.Storage.ListExpired(ctx, now, run.pos, batchSize)
		if err != nil {
			return fmt.Errorf("sweep expired files: %w", err)
		}

		err = m.sweepBatch(ctx, run, batch, now)
		if err != nil {
			return err
		}

		if run.failStreak >= sweepMaxFailStreak {
			return nil
		}

		// A short batch is the end of the list. The next sweep starts at the oldest record
		// again, so that it tries the records that this sweep kept.
		if len(batch) < batchSize {
			run.pos = ExpiredCursor{ExpiresAt: time.Time{}, ID: ""}

			return nil
		}
	}

	return nil
}

// sweepBatch removes the files of a batch. A file that it cannot remove does not stop it; run
// keeps the count of the failures in a row.
func (m *Manager) sweepBatch(ctx context.Context, run *sweepRun, batch []*domain.File, now time.Time) error {
	for _, file := range batch {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return fmt.Errorf("sweep expired files: %w", ctxErr)
		}

		err := m.removeExpired(ctx, file.ID, file.ObjectKey, now)

		switch {
		case err == nil:
			run.removed++
			run.failStreak = 0
		case errors.Is(err, domain.ErrNotFound):
			// Another process removed the record.
			run.failStreak = 0
		case errors.Is(err, errObjectKept):
			// deleteObject logged and counted the failure. The record stays for a later sweep.
			run.failStreak++
		default:
			// The position stays before this record, so that the next sweep tries it again.
			return err
		}

		run.pos = cursorAfter(file)

		if run.failStreak >= sweepMaxFailStreak {
			return nil
		}
	}

	return nil
}

// cursorAfter returns the cursor that makes ListExpired start after file.
func cursorAfter(file *domain.File) ExpiredCursor {
	cursor := ExpiredCursor{ExpiresAt: time.Time{}, ID: file.ID}
	if file.ExpiresAt != nil {
		cursor.ExpiresAt = *file.ExpiresAt
	}

	return cursor
}

// removeExpired deletes the object of an expired file, then its record while it is expired at
// now.
func (m *Manager) removeExpired(ctx context.Context, recordID, objectKey string, now time.Time) error {
	if !m.deleteObject(ctx, objectKey) {
		return errObjectKept
	}

	err := m.deps.Storage.DeleteExpired(ctx, recordID, objectKey, now)
	if err != nil {
		return fmt.Errorf("delete expired file %s: %w", recordID, err)
	}

	m.metrics.IncDeleted(DeleteReasonExpired)

	return nil
}

// expire removes the expired file that a reader asked for. It returns domain.ErrExpired, or
// domain.ErrNotFound when another process removed the file first. file.ID is the slug.
func (m *Manager) expire(ctx context.Context, file *domain.File, now time.Time) error {
	recordID := domain.HashSlug(file.ID)

	err := m.removeExpired(ctx, recordID, file.ObjectKey, now)

	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.ErrNotFound
	case err == nil, errors.Is(err, errObjectKept):
		// deleteObject logs an object failure; the sweep tries again later.
		return domain.ErrExpired
	default:
		// The sweep removes the file later. The reader gets the same answer.
		m.deps.Log.ErrorContext(ctx, "delete expired file", "id", recordID, "err", err)

		return domain.ErrExpired
	}
}

// sweepOnUpload removes a small batch of expired files on each upload. The background sweep
// can be off, so this cleanup stays. The whole sweep has the limit uploadSweepTimeout, so a slow
// object storage cannot hold the response of the upload. A failure or the limit only delays the
// cleanup to the next sweep. The request context stays the parent: when the client leaves, the
// sweep stops.
func (m *Manager) sweepOnUpload(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, m.uploadSweepTimeout)
	defer cancel()

	_, err := m.sweep(sweepCtx, m.uploadPos, uploadSweepBatchSize, 1)
	if err != nil {
		m.deps.Log.WarnContext(ctx, "file sweep failed", "err", err)
	}
}

// deleteObject removes an object within objectDeleteTimeout and reports whether it did. The
// warning and the "delete" storage error counter are the signal for the operator. The log line
// does not contain the object key: with the key and the bucket, a reader of the logs can find
// the content of one file.
func (m *Manager) deleteObject(ctx context.Context, key string) bool {
	deleteCtx, cancel := context.WithTimeout(ctx, objectDeleteTimeout)
	defer cancel()

	err := m.deps.Objects.Delete(deleteCtx, key)
	if err != nil {
		m.metrics.IncStorageErrors(StorageOpDelete)
		m.deps.Log.WarnContext(ctx, "delete file object", "err", err)

		return false
	}

	return true
}
