package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	// burnClaimTTL is the life of a burn claim. A claim of a server that stopped during the
	// transfer becomes stale after this time, and the next reader can download the file. It must
	// be longer than the longest transfer: the HTTP adapter permits one hour.
	burnClaimTTL = 70 * time.Minute
	// burnClaimMargin stops Send before its claim becomes stale. Thus two readers never get
	// the content of one burn file.
	burnClaimMargin = 5 * time.Minute
)

// Download is a file download that is ready to send. Call Send once, then Close.
type Download struct {
	File *domain.File

	send func(ctx context.Context, dst io.Writer) error
	// complete runs at Close after a complete Send, release runs at Close in each other case.
	complete func(ctx context.Context)
	release  func(ctx context.Context)
	sent     bool
}

// noFinish is the Close action of a file that is not a burn file.
func noFinish(context.Context) {}

// NewDownload returns a Download of file whose Send calls send. Manager uses it; adapter tests
// use it for a download without object storage.
func NewDownload(file *domain.File, send func(ctx context.Context, dst io.Writer) error) *Download {
	return &Download{File: file, send: send, complete: noFinish, release: noFinish, sent: false}
}

// Send writes the plaintext of the file to dst.
func (d *Download) Send(ctx context.Context, dst io.Writer) error {
	err := d.send(ctx, dst)
	d.sent = err == nil

	return err
}

// Close ends the download. For a burn-after-download file, it deletes the file when Send
// completed, and it releases the claim when Send failed or did not run; the next reader can
// then download the file. Give a context that is not canceled when the client disconnects.
func (d *Download) Close(ctx context.Context) {
	if d.sent {
		d.complete(ctx)
	} else {
		d.release(ctx)
	}

	// A second Close does nothing.
	d.complete, d.release = noFinish, noFinish

	dropKey(d.File)
}

// record claims a burn file or counts the download of another file.
func (m *Manager) record(ctx context.Context, file *domain.File, opts AccessOptions, download *Download) error {
	if file.BurnAfterDownload {
		return m.claimBurn(ctx, file, opts, download)
	}

	recordID := domain.HashSlug(file.ID)

	err := m.deps.Storage.IncrementDownloadsVisible(ctx, recordID, file.ObjectKey, opts)

	switch {
	case errors.Is(err, domain.ErrNotFound):
		return m.recordChanged(ctx, file, opts, download)
	case err != nil:
		// The counter is statistics only: a failure does not stop the download.
		m.deps.Log.ErrorContext(ctx, "count file download", "id", recordID, "err", err)
	default:
		file.Downloads++
	}

	return nil
}

// recordChanged handles a counter that found no row. The record changed after the first read:
// it was deleted, replaced by a new file with the same slug, made private, expired, or made a
// burn file. A second read selects the correct result.
func (m *Manager) recordChanged(
	ctx context.Context, file *domain.File, opts AccessOptions, download *Download,
) error {
	fresh, err := m.deps.Storage.GetByID(ctx, domain.HashSlug(file.ID))
	if err != nil {
		return fmt.Errorf("count download: %w", err)
	}

	switch {
	case fresh.ObjectKey != file.ObjectKey:
		return fmt.Errorf("count download: slug has a new file: %w", domain.ErrNotFound)
	case !opts.visible(fresh):
		return domain.ErrForbidden
	case fresh.Expired(time.Now()):
		return m.expire(ctx, file)
	case !fresh.BurnAfterDownload:
		return fmt.Errorf("count download: %w", domain.ErrNotFound)
	case !opts.AllowBurn:
		return domain.ErrBurnIntentRequired
	default:
		file.BurnAfterDownload = true

		return m.claimBurn(ctx, file, opts, download)
	}
}

// claimBurn claims a burn file for this download. The record stays, but other readers do not
// see it. Download.Close deletes it after a complete Send, or releases the claim.
func (m *Manager) claimBurn(ctx context.Context, file *domain.File, opts AccessOptions, download *Download) error {
	now := time.Now()
	claim := &BurnClaim{
		Now:       now,
		Until:     now.Add(burnClaimTTL),
		ID:        domain.HashSlug(file.ID),
		ObjectKey: file.ObjectKey,
		Token:     uuid.NewString(),
		Reader:    opts,
	}

	err := m.deps.Storage.ClaimBurn(ctx, claim)
	if err != nil {
		return fmt.Errorf("claim burn file: %w", err)
	}

	send := download.send
	download.send = func(ctx context.Context, dst io.Writer) error {
		sendCtx, cancel := context.WithDeadline(ctx, claim.Until.Add(-burnClaimMargin))
		defer cancel()

		return send(sendCtx, dst)
	}
	download.complete = func(ctx context.Context) { m.completeBurn(ctx, claim) }
	download.release = func(ctx context.Context) { m.releaseBurn(ctx, claim) }

	return nil
}

// releaseBurn releases the claim after a failed transfer, so that the next reader can download
// the file.
func (m *Manager) releaseBurn(ctx context.Context, claim *BurnClaim) {
	err := m.deps.Storage.ReleaseBurn(ctx, claim)
	if err != nil {
		// The claim becomes stale after burnClaimTTL, so the file is not lost.
		m.deps.Log.WarnContext(ctx, "release burn claim", "id", claim.ID, "err", err)
	}
}

// completeBurn deletes a burn file after a complete transfer. Send ends before the claim becomes
// stale, so no other reader holds the record now, and Delete with the object key is enough.
func (m *Manager) completeBurn(ctx context.Context, claim *BurnClaim) {
	err := m.deps.Storage.Delete(ctx, claim.ID, claim.ObjectKey)
	if err != nil {
		m.deps.Log.ErrorContext(ctx, "delete burn file record", "id", claim.ID, "err", err)
	} else {
		m.metrics.IncDeleted(DeleteReasonBurn)
	}

	// The reader has the content. The object goes also when the record stays, so that no
	// second reader gets the file after a stale claim.
	m.deleteObject(ctx, claim.ObjectKey)
}
