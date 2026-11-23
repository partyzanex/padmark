package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

type RequestTimeoutSuite struct {
	suite.Suite
}

func TestRequestTimeoutSuite(t *testing.T) {
	suite.Run(t, new(RequestTimeoutSuite))
}

// TestRequestTimeout_SlowServer_FailsFast verifies the --timeout flag is wired into the
// command: against a server that never responds in time, the command aborts after the timeout
// instead of hanging. The server holds the reply far longer than the timeout, so a failure
// before the reply proves the bound is enforced rather than ignored. The bound is the server
// delay, not a short fixed time, so a slow machine cannot make the test fail.
func (s *RequestTimeoutSuite) TestRequestTimeout_SlowServer_FailsFast() {
	isolateEnv(s.T())

	// serverDelay >> timeout so the client's deadline fires first. The handler returns when the
	// client goes away, so srv.Close() does not wait for the full delay.
	const (
		serverDelay = 30 * time.Second
		timeout     = "50ms"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The delay is the test input (a slow server), not a wait for other goroutines.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(serverDelay):
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Now()
	err := runCLI(s.T().Context(), "--url", srv.URL, "--timeout", timeout, cmdGet, "some-id")
	elapsed := time.Since(start)

	s.Require().Error(err, "request must fail when the server is slower than the timeout")
	s.Less(elapsed, serverDelay/2,
		"command must fail fast on timeout, not wait for the slow server (got %s)", elapsed)
}

// TestRequestTimeout_SlowBody_FailsFast verifies that --timeout limits the full API call of an
// ordinary command, not only the wait for the response headers: the server sends the headers at
// once and then holds the body. Only the deadline of requestContext stops this call.
func (s *RequestTimeoutSuite) TestRequestTimeout_SlowBody_FailsFast() {
	isolateEnv(s.T())

	const (
		serverDelay = 30 * time.Second
		timeout     = "50ms"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		flusher, isFlusher := w.(http.Flusher)
		if isFlusher {
			flusher.Flush()
		}

		// The delay is the test input (a slow body), not a wait for other goroutines.
		select {
		case <-r.Context().Done():
		case <-time.After(serverDelay):
		}
	}))
	defer srv.Close()

	start := time.Now()
	err := runCLI(s.T().Context(), "--url", srv.URL, "--timeout", timeout, cmdGet, "some-id")
	elapsed := time.Since(start)

	s.Require().Error(err, "a body slower than the timeout must stop the command")
	s.Require().ErrorIs(err, context.DeadlineExceeded)
	s.Less(elapsed, serverDelay/2,
		"command must fail fast on timeout, not wait for the slow body (got %s)", elapsed)
}

// TestRequestContext_Deadline checks the deadline that requestContext adds for --timeout, and
// that --timeout 0 adds none.
func TestRequestContext_Deadline(t *testing.T) {
	isolateEnv(t)

	ctx, cancel := requestContext(t.Context(), parsedCommand(t, "--timeout", "1h"))
	defer cancel()

	deadline, hasDeadline := ctx.Deadline()
	require.True(t, hasDeadline)
	assert.WithinDuration(t, time.Now().Add(time.Hour), deadline, time.Minute)

	ctx, cancel = requestContext(t.Context(), parsedCommand(t, "--timeout", "0"))
	defer cancel()

	_, hasDeadline = ctx.Deadline()
	assert.False(t, hasDeadline, "--timeout 0 adds no deadline")
}

// TestUpload_SlowBodyOutlivesTimeout: upload sends a file body for longer than --timeout and
// completes, because only the response header timeout limits it. The same upload in the
// context of requestContext stops with the deadline.
func (s *RequestTimeoutSuite) TestUpload_SlowBodyOutlivesTimeout() {
	isolateEnv(s.T())

	const (
		timeout = 50 * time.Millisecond
		chunks  = 4
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, copyErr := io.Copy(io.Discard, r.Body)
		if copyErr != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"file123abc","url":"https://pad.example/file123abc","name":"a.bin",`+
			`"size":4,"privacy":"public","downloads":0,"burn_after_download":false,`+
			`"created_at":"2026-11-20T10:00:00Z","edit_code":"code-12345678"}`)
	}))
	defer srv.Close()

	cmd := parsedCommand(s.T(), "--url", srv.URL, "--timeout", timeout.String())

	client, _, err := clientauth.NewClient(clientSettings(cmd))
	s.Require().NoError(err)

	created, err := client.UploadFile(s.T().Context(), "a.bin", newSlowReader(chunks, 2*timeout), noUploadOptions())
	s.Require().NoError(err, "the header timeout must not stop a slow upload")
	s.Equal("file123abc", created.ID)

	ctx, cancel := requestContext(s.T().Context(), cmd)
	defer cancel()

	_, err = client.UploadFile(ctx, "a.bin", newSlowReader(chunks, 2*timeout), noUploadOptions())
	s.Require().ErrorIs(err, context.DeadlineExceeded, "the request deadline stops the slow upload")
}

// --- helpers ---

// parsedCommand parses the global flags and returns the root command, as an action sees it.
func parsedCommand(t *testing.T, args ...string) *urcli.Command {
	t.Helper()

	var parsed *urcli.Command

	app := NewApp()
	app.Action = func(_ context.Context, cmd *urcli.Command) error {
		parsed = cmd

		return nil
	}

	require.NoError(t, app.Run(t.Context(), append([]string{testBin}, args...)))
	require.NotNil(t, parsed)

	return parsed
}

// noUploadOptions returns upload options with no field set.
func noUploadOptions() padmark.UploadFileOptions {
	return padmark.UploadFileOptions{TTL: 0, Privacy: "", Slug: "", EditCode: "", BurnAfterDownload: false}
}

// slowReader gives one byte for each Read after a delay. It acts as a slow link or disk; the
// delay is the test input, not a wait for other goroutines.
type slowReader struct {
	delay time.Duration
	left  int
}

func newSlowReader(chunks int, total time.Duration) *slowReader {
	return &slowReader{delay: total / time.Duration(chunks), left: chunks}
}

func (r *slowReader) Read(buf []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}

	<-time.After(r.delay)

	r.left--
	buf[0] = 'x'

	return 1, nil
}
