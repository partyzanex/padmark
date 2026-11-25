package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// deadlineRecorder is a response writer that records the deadlines that
// http.ResponseController sets.
type deadlineRecorder struct {
	*httptest.ResponseRecorder

	read  time.Time
	write time.Time
}

func (d *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	d.read = deadline

	return nil
}

func (d *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	d.write = deadline

	return nil
}

func newDeadlineRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), read: time.Time{}, write: time.Time{}}
}

// TestTransferDeadline_Upload: the body has the idle deadline, the response has the total
// limit until the body ends, and the end of the body gives the read deadline the total limit.
func TestTransferDeadline_Upload(t *testing.T) {
	t.Parallel()

	rec := newDeadlineRecorder()
	deadline := newTransferDeadline(rec, time.Minute)

	require.NoError(t, deadline.uploadStarted())
	require.WithinDuration(t, time.Now().Add(time.Minute), rec.read, time.Second)
	require.Equal(t, deadline.end, rec.write)

	require.NoError(t, deadline.readProgress())
	require.WithinDuration(t, time.Now().Add(time.Minute), rec.read, time.Second)

	require.NoError(t, deadline.bodyDone())
	require.Equal(t, deadline.end, rec.read)

	require.NoError(t, deadline.responseStarted())
	require.WithinDuration(t, time.Now().Add(time.Minute), rec.write, time.Second)
}

// TestTransferDeadline_Download: the client sends nothing, so the read deadline is the total
// limit; the write deadline is the idle deadline.
func TestTransferDeadline_Download(t *testing.T) {
	t.Parallel()

	rec := newDeadlineRecorder()
	deadline := newTransferDeadline(rec, time.Minute)

	require.NoError(t, deadline.downloadStarted())
	require.Equal(t, deadline.end, rec.read)
	require.WithinDuration(t, time.Now().Add(time.Minute), rec.write, time.Second)
}

// TestTransferDeadline_TotalLimit: the idle deadline never passes the total limit.
func TestTransferDeadline_TotalLimit(t *testing.T) {
	t.Parallel()

	rec := newDeadlineRecorder()
	deadline := newTransferDeadline(rec, 2*fileTransferTimeout)

	require.NoError(t, deadline.readProgress())
	require.Equal(t, deadline.end, rec.read)
}

// TestTransferDeadline_NotSupported: a writer without deadlines gives one error, then the
// updates stop.
func TestTransferDeadline_NotSupported(t *testing.T) {
	t.Parallel()

	deadline := newTransferDeadline(httptest.NewRecorder(), time.Minute)

	err := deadline.uploadStarted()
	require.ErrorIs(t, err, http.ErrNotSupported)
	require.NoError(t, deadline.readProgress())
}

// TestUploadLimiter: the server limit is checked first, then the client limit. A release frees
// the slot of its client.
func TestUploadLimiter(t *testing.T) {
	t.Parallel()

	limiter := newUploadLimiter(2, 1)

	require.NoError(t, limiter.acquire("a"))
	require.ErrorIs(t, limiter.acquire("a"), errClientUploads)
	require.NoError(t, limiter.acquire("b"))
	require.ErrorIs(t, limiter.acquire("c"), errUploadsBusy)

	limiter.release("a")
	require.NoError(t, limiter.acquire("c"))
	require.Empty(t, limiter.perClient["a"])
}

// TestUploadLimiter_Defaults: zero limits select the defaults.
func TestUploadLimiter_Defaults(t *testing.T) {
	t.Parallel()

	limiter := newUploadLimiter(0, -1)

	require.Equal(t, DefaultMaxUploads, limiter.maxTotal)
	require.Equal(t, DefaultMaxUploadsPerClient, limiter.maxPerClient)
}

// TestClassifyBodyError: the size limit stays, a deadline becomes the idle error, and other
// errors become the malformed error.
func TestClassifyBodyError(t *testing.T) {
	t.Parallel()

	maxBytes := &http.MaxBytesError{Limit: 1}
	require.ErrorIs(t, classifyBodyError(maxBytes), maxBytes)
	require.ErrorIs(t, classifyBodyError(errors.New("multipart: bad header")), errUploadMalformed)
	require.ErrorIs(t, classifyBodyError(os.ErrDeadlineExceeded), errUploadIdle)
}
