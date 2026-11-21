package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/partyzanex/padmark/internal/domain"
)

// errStreamStopped closes a pipe when its reader or writer side is done. The goroutine on the
// other side then ends with this error, which is not a failure.
var errStreamStopped = errors.New("upload stopped")

// upload encrypts body into the object storage through a pipe. The encryption goroutine ends
// before upload returns, so the size counter is safe to read.
func (m *Manager) upload(ctx context.Context, file *domain.File, body io.Reader) error {
	key, err := m.deps.Cipher.NewKey()
	if err != nil {
		return fmt.Errorf("new file key: %w", err)
	}

	counter := &sizeLimitReader{src: body, limit: m.maxSize, total: 0}
	pipeReader, pipeWriter := io.Pipe()
	source := &failureReader{src: pipeReader, failed: atomic.Bool{}}
	encrypted := make(chan error, 1)

	go func() {
		encErr := m.deps.Cipher.Encrypt(key, pipeWriter, counter)
		encrypted <- errors.Join(encErr, pipeWriter.CloseWithError(encErr))
	}()

	putErr := m.deps.Objects.Put(ctx, file.ObjectKey, source)
	putErr = errors.Join(putErr, pipeReader.CloseWithError(errStreamStopped))

	encErr := <-encrypted

	err = m.uploadError(ctx, &uploadResult{
		putErr: putErr, encErr: encErr, total: counter.total, bodyFailed: source.failed.Load(),
	})
	if err != nil {
		clear(key)
		m.discardUpload(ctx, file.ObjectKey)

		return err
	}

	file.ContentKey = key
	file.Size = counter.total

	return nil
}

// uploadResult holds the outcome of the two sides of an upload.
type uploadResult struct {
	putErr error
	encErr error
	total  int64
	// bodyFailed is true when Put stopped because the encrypted stream gave an error. The
	// encryption side gives an error only when the request body fails.
	bodyFailed bool
}

// uploadError selects the error of an upload. The size limit, a canceled request and a broken
// request body are the caller's errors. Put is examined first: when Put failed for a different
// cause, the upload is a storage failure, also if the body read failed after that.
func (m *Manager) uploadError(ctx context.Context, result *uploadResult) error {
	switch {
	case result.total > m.maxSize:
		return domain.ErrFileTooLarge
	case result.putErr == nil && result.encErr == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("upload file: %w", ctx.Err())
	case result.putErr != nil && !result.bodyFailed:
		return m.storageFailure(ctx, result.putErr)
	case result.encErr != nil && !errors.Is(result.encErr, errStreamStopped):
		// The client stopped sending, or the body was not readable.
		return fmt.Errorf("read upload: %w", result.encErr)
	default:
		return m.storageFailure(ctx, result.putErr)
	}
}

func (m *Manager) storageFailure(ctx context.Context, putErr error) error {
	m.metrics.IncStorageErrors(StorageOpPut)
	m.deps.Log.ErrorContext(ctx, "upload file", "err", putErr)

	return fmt.Errorf("upload file: %w", errors.Join(domain.ErrStorageUnavailable, putErr))
}

// discardUpload removes what a failed upload can leave. minio-go aborts a failed multipart
// upload itself, so no part stays. But Put can fail after the object is complete, for example
// when the response is lost; Delete removes that object and is safe for a missing key.
//
// A failure here is not a "delete" storage error. That counter means an object without a
// record, and during an object storage failure this cleanup fails for each upload.
func (m *Manager) discardUpload(ctx context.Context, key string) {
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), objectDeleteTimeout)
	defer cancel()

	err := m.deps.Objects.Delete(deleteCtx, key)
	if err != nil {
		m.deps.Log.DebugContext(ctx, "remove failed upload", "err", err)
	}
}

// failureReader records whether its source returned an error other than io.EOF. Put can read
// in a different goroutine, so the flag is atomic.
type failureReader struct {
	src    io.Reader
	failed atomic.Bool
}

func (r *failureReader) Read(dst []byte) (int, error) {
	n, err := r.src.Read(dst)

	switch {
	case err == nil:
		return n, nil
	case err == io.EOF:
		// The io.Reader contract needs io.EOF itself, not a wrapped value.
		return n, io.EOF
	case errors.Is(err, errStreamStopped):
		// upload closed the pipe itself. This is not a failure of the request body.
	default:
		r.failed.Store(true)
	}

	return n, fmt.Errorf("read encrypted upload: %w", err)
}

// sizeLimitReader counts bytes and fails with domain.ErrFileTooLarge after limit bytes.
type sizeLimitReader struct {
	src   io.Reader
	limit int64
	total int64
}

func (r *sizeLimitReader) Read(dst []byte) (int, error) {
	if r.total > r.limit {
		return 0, domain.ErrFileTooLarge
	}

	// One byte more than the limit is enough to know that the file is too large.
	chunk := dst
	if remaining := r.limit - r.total + 1; int64(len(chunk)) > remaining {
		chunk = chunk[:remaining]
	}

	n, err := r.src.Read(chunk)
	r.total += int64(n)

	switch {
	case r.total > r.limit:
		return n, domain.ErrFileTooLarge
	case err == io.EOF:
		// The io.Reader contract needs io.EOF itself. A wrapped io.EOF is not a clean end, and
		// io.ErrUnexpectedEOF is a cut body, so both go to the error case below.
		return n, io.EOF
	case err != nil:
		return n, fmt.Errorf("read upload: %w", err)
	default:
		return n, nil
	}
}
