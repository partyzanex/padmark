package files

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	// sweepBatchSize and sweepMaxBatches limit one SweepExpired call to 1000 files. The next
	// sweep continues; thus one call cannot hold the database or the object storage for long.
	sweepBatchSize  = 100
	sweepMaxBatches = 10
	// uploadSweepBatchSize limits the sweep on the upload path. The background sweep can be off,
	// so uploads remove a few expired files; a small batch keeps the upload response fast.
	uploadSweepBatchSize = 8
	// objectDeleteTimeout limits one object delete, so that a stopped object storage cannot
	// hold a sweep, a burn cleanup or a shutdown without end.
	objectDeleteTimeout = 30 * time.Second
)

// errObjectKept tells that an expired file keeps its record because its object delete failed.
var errObjectKept = errors.New("file object is not deleted")

// SweepExpired removes expired files in batches. It returns the number of removed files.
// The object goes first, then the record with the same object key. Thus an object delete
// failure keeps the record, and the next sweep tries again; no object stays without a record.
// Give the context of the caller: its cancellation stops the sweep. A record error stops the
// sweep and is returned.
func (m *Manager) SweepExpired(ctx context.Context) (int, error) {
	return m.sweep(ctx, sweepBatchSize, sweepMaxBatches)
}

// ReleaseExpiredSlug removes the file with the record ID id when it is expired, so that a note,
// a link or a new file can take the slug before the sweep removes it. It returns true when it
// removed the file. Errors are fail-soft for the caller: it keeps its slug conflict.
func (m *Manager) ReleaseExpiredSlug(ctx context.Context, id string) (bool, error) {
	file, err := m.deps.Storage.GetByID(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("release expired slug: %w", err)
	}

	if !file.Expired(time.Now()) {
		return false, nil
	}

	err = m.removeExpired(ctx, id, file.ObjectKey)
	if err != nil {
		return false, fmt.Errorf("release expired slug: %w", err)
	}

	return true, nil
}

func (m *Manager) sweep(ctx context.Context, batchSize, maxBatches int) (int, error) {
	removed := 0

	for range maxBatches {
		batch, err := m.deps.Storage.ListExpired(ctx, time.Now(), batchSize)
		if err != nil {
			return removed, fmt.Errorf("sweep expired files: %w", err)
		}

		count, err := m.sweepBatch(ctx, batch)
		removed += count

		if err != nil {
			return removed, err
		}

		// A short batch is the last one. A batch without progress means that object storage
		// fails; the same records would come again, so the next sweep tries again.
		if len(batch) < batchSize || count == 0 {
			break
		}
	}

	return removed, nil
}

func (m *Manager) sweepBatch(ctx context.Context, batch []*domain.File) (int, error) {
	removed := 0

	for _, file := range batch {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return removed, fmt.Errorf("sweep expired files: %w", ctxErr)
		}

		err := m.removeExpired(ctx, file.ID, file.ObjectKey)

		switch {
		case err == nil:
			removed++
		case errors.Is(err, errObjectKept), errors.Is(err, domain.ErrNotFound):
			// The object delete is logged; a missing record was removed by another process.
		default:
			return removed, fmt.Errorf("sweep expired files: %w", err)
		}
	}

	return removed, nil
}

// removeExpired deletes the object of an expired file, then its record.
func (m *Manager) removeExpired(ctx context.Context, recordID, objectKey string) error {
	if !m.deleteObject(ctx, objectKey) {
		return errObjectKept
	}

	err := m.deps.Storage.DeleteExpired(ctx, recordID, objectKey, time.Now())
	if err != nil {
		return fmt.Errorf("delete expired file: %w", err)
	}

	m.metrics.IncDeleted(DeleteReasonExpired)

	return nil
}

// expire removes the expired file that a reader asked for. It returns domain.ErrExpired, or
// domain.ErrNotFound when another process removed the file first. file.ID is the slug.
func (m *Manager) expire(ctx context.Context, file *domain.File) error {
	recordID := domain.HashSlug(file.ID)

	err := m.removeExpired(ctx, recordID, file.ObjectKey)

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
// can be off, so this cleanup stays. A failure only delays the cleanup to the next sweep.
func (m *Manager) sweepOnUpload(ctx context.Context) {
	_, err := m.sweep(ctx, uploadSweepBatchSize, 1)
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
