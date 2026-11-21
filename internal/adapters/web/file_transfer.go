package web

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// Default limits of file transfers. FileTransferLimits replaces them.
const (
	// DefaultFileIdleTimeout stops an upload or a download that makes no progress for this time.
	DefaultFileIdleTimeout = time.Minute
	// DefaultMaxUploads is the number of uploads that the server accepts at the same time. Each
	// upload holds one object-storage part buffer in memory.
	DefaultMaxUploads = 32
	// DefaultMaxUploadsPerClient is the number of uploads from one client IP address at the same
	// time.
	DefaultMaxUploadsPerClient = 4
	// uploadRetryAfterSeconds is the Retry-After value when the upload limit is full.
	uploadRetryAfterSeconds = 10
)

var (
	// errUploadMalformed is the constant answer to a body that is not valid multipart or that
	// stops before the end. The parser error is not shown or logged: it can quote form values.
	errUploadMalformed = errors.New("the multipart body is malformed or incomplete")
	// errUploadFieldAfterFile refuses a part after the file part. The server reads the fields
	// before the file, so a field after it would have no effect.
	errUploadFieldAfterFile = errors.New("the \"file\" part must be the last part of the form")
	// errUploadIdle tells that the client sent no data for the idle timeout.
	errUploadIdle    = errors.New("the upload made no progress for too long")
	errUploadsBusy   = errors.New("the server has too many uploads; try again later")
	errClientUploads = errors.New("too many uploads from this client; try again later")
)

// FileTransferLimits holds the limits of file uploads and downloads. A zero or negative field
// selects its default.
type FileTransferLimits struct {
	// IdleTimeout stops a transfer that makes no progress for this time.
	IdleTimeout time.Duration
	// MaxUploads is the number of uploads that the server accepts at the same time.
	MaxUploads int
	// MaxUploadsPerClient is the number of uploads from one client IP address at the same time.
	MaxUploadsPerClient int
}

// WithFileTransferLimits sets the limits of file transfers. Called by the composition root
// before the server starts; do not call after the server starts.
func (h *Handler) WithFileTransferLimits(limits FileTransferLimits) *Handler {
	h.files.idleTimeout = limits.IdleTimeout
	if h.files.idleTimeout <= 0 {
		h.files.idleTimeout = DefaultFileIdleTimeout
	}

	h.files.uploads = newUploadLimiter(limits.MaxUploads, limits.MaxUploadsPerClient)

	return h
}

// uploadLimiter counts the uploads in progress, in total and for each client IP address.
type uploadLimiter struct {
	perClient    map[string]int
	mu           sync.Mutex
	total        int
	maxTotal     int
	maxPerClient int
}

func newUploadLimiter(maxTotal, maxPerClient int) *uploadLimiter {
	limiter := &uploadLimiter{
		perClient: make(map[string]int), mu: sync.Mutex{}, total: 0,
		maxTotal: DefaultMaxUploads, maxPerClient: DefaultMaxUploadsPerClient,
	}

	if maxTotal > 0 {
		limiter.maxTotal = maxTotal
	}

	if maxPerClient > 0 {
		limiter.maxPerClient = maxPerClient
	}

	return limiter
}

// acquire reserves one upload for client. It returns errUploadsBusy when the server is full
// and errClientUploads when the client is at its limit. Call release after a nil error.
func (l *uploadLimiter) acquire(client string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.total >= l.maxTotal {
		return errUploadsBusy
	}

	if l.perClient[client] >= l.maxPerClient {
		return errClientUploads
	}

	l.total++
	l.perClient[client]++

	return nil
}

// release frees the upload of client that acquire reserved.
func (l *uploadLimiter) release(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.total--

	l.perClient[client]--
	if l.perClient[client] <= 0 {
		delete(l.perClient, client)
	}
}

// uploadBusyStatus returns the status of an upload that the limiter refused: 503 when the
// server is full, 429 when the client is at its limit. It sets Retry-After for both.
func uploadBusyStatus(w http.ResponseWriter, err error) (status int) {
	status = http.StatusServiceUnavailable
	if errors.Is(err, errClientUploads) {
		status = http.StatusTooManyRequests
	}

	w.Header().Set("Retry-After", strconv.Itoa(uploadRetryAfterSeconds))

	return status
}

// transferDeadline moves the connection deadlines of one transfer forward while data moves.
// Each deadline is at most idle after the last progress and never after end, the total limit.
// The server read and write timeouts (30 and 60 seconds by default) are for pages and JSON; a
// large file on a slow connection needs more time, but a stalled client must not hold the
// connection for the full total limit.
type transferDeadline struct {
	ctrl *http.ResponseController
	end  time.Time
	idle time.Duration
	// failed stops the updates after the first error: the writer does not support deadlines,
	// so the server timeouts apply.
	failed bool
}

func newTransferDeadline(w http.ResponseWriter, idle time.Duration) *transferDeadline {
	return &transferDeadline{
		ctrl: http.NewResponseController(w), end: time.Now().Add(fileTransferTimeout), idle: idle, failed: false,
	}
}

// next returns the deadline after progress now.
func (d *transferDeadline) next() time.Time {
	next := time.Now().Add(d.idle)
	if next.After(d.end) {
		return d.end
	}

	return next
}

// set applies the read and the write deadline. A zero value keeps that deadline.
func (d *transferDeadline) set(read, write time.Time) error {
	if d.failed {
		return nil
	}

	var err error
	if !read.IsZero() {
		err = d.ctrl.SetReadDeadline(read)
	}

	if !write.IsZero() {
		err = errors.Join(err, d.ctrl.SetWriteDeadline(write))
	}

	if err != nil {
		d.failed = true

		return fmt.Errorf("set transfer deadline: %w", err)
	}

	return nil
}

// uploadStarted gives the upload body the idle deadline. The response comes after the body, so
// the write deadline is the total limit.
func (d *transferDeadline) uploadStarted() error { return d.set(d.next(), d.end) }

// readProgress moves the read deadline after data arrived.
func (d *transferDeadline) readProgress() error { return d.set(d.next(), time.Time{}) }

// bodyDone sets the read deadline to the total limit. After the body, the server reads the
// connection only to see a closed connection; an idle deadline there would cancel the request
// while the object storage completes the upload.
func (d *transferDeadline) bodyDone() error { return d.set(d.end, time.Time{}) }

// responseStarted gives the response the idle write deadline.
func (d *transferDeadline) responseStarted() error { return d.set(time.Time{}, d.next()) }

// downloadStarted sets the deadlines of a download: the client sends nothing, so the read
// deadline is the total limit; the write deadline is the idle deadline.
func (d *transferDeadline) downloadStarted() error { return d.set(d.end, d.next()) }

// uploadBody is the file part as the usecase reads it. It moves the read deadline on progress,
// classifies read errors and, at the end of the file part, makes sure that no part follows.
// It keeps the first error, so the handler knows why the usecase stopped.
type uploadBody struct {
	part     *multipart.Part
	reader   *multipart.Reader
	deadline *transferDeadline
	// onDeadlineError gets a failure to move a deadline; it only logs.
	onDeadlineError func(err error)
	err             error
}

func (b *uploadBody) Read(buf []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}

	n, err := b.part.Read(buf)
	if n > 0 {
		b.deadlineDone(b.deadline.readProgress())
	}

	switch {
	case err == nil:
		return n, nil
	case errors.Is(err, io.EOF):
		err = b.finish()
	default:
		err = classifyBodyError(err)
	}

	b.err = err

	return n, err
}

// finish runs at the end of the file part. It reads the rest of the form: only the closing
// boundary may follow. The result is io.EOF itself, because readers compare it with ==.
func (b *uploadBody) finish() error {
	_, err := b.reader.NextPart()

	b.deadlineDone(b.deadline.bodyDone())

	switch {
	case errors.Is(err, io.EOF):
		return io.EOF
	case err == nil:
		return errUploadFieldAfterFile
	default:
		return classifyBodyError(err)
	}
}

func (b *uploadBody) deadlineDone(err error) {
	if err != nil {
		b.onDeadlineError(err)
	}
}

// cause returns the reason of a failed upload: the body error when the body stopped the upload,
// otherwise err from the usecase.
func (b *uploadBody) cause(err error) error {
	if b.err != nil && !errors.Is(b.err, io.EOF) {
		return b.err
	}

	return err
}

// classifyBodyError maps a read error of the request body to an upload error. The size limit
// stays as it is (413). The original error stays in the chain for errors.Is, but the handler
// never sends or logs its text: a parser error can quote form values.
func classifyBodyError(err error) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return err
	}

	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%w: %w", errUploadIdle, err)
	}

	return fmt.Errorf("%w: %w", errUploadMalformed, err)
}

// downloadWriter is the response as the usecase writes a download to it. It moves the write
// deadline on progress and records whether the response started.
type downloadWriter struct {
	http.ResponseWriter

	deadline        *transferDeadline
	onDeadlineError func(err error)
	started         bool
}

func (dw *downloadWriter) Write(buf []byte) (int, error) {
	// An empty Write also sends the status and the headers.
	dw.started = true

	n, err := dw.ResponseWriter.Write(buf)
	if n > 0 {
		deadlineErr := dw.deadline.responseStarted()
		if deadlineErr != nil {
			dw.onDeadlineError(deadlineErr)
		}
	}

	if err != nil {
		return n, fmt.Errorf("write download: %w", err)
	}

	return n, nil
}

// clearDownloadHeaders removes the headers of writeDownloadHeaders before an error response.
func clearDownloadHeaders(w http.ResponseWriter) {
	header := w.Header()
	for _, name := range []string{
		"Content-Type", "Content-Length", "Content-Disposition", "Cache-Control", "X-Content-Type-Options",
	} {
		header.Del(name)
	}
}
