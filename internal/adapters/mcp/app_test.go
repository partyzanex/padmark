package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func TestMCPRejectsUnknownLogLevel(t *testing.T) {
	isolateEnv(t)

	app := NewApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	err := app.Run(t.Context(), []string{"padmark-mcp", "--" + FlagLogLevel, "debig"})
	require.ErrorContains(t, err, "invalid log level")
}

// TestMCPRejectsRemovedHiddenFlag checks that no flag turns off the refusal of names that start
// with a dot. The unreleased v0.5.0 had such a flag; a configuration with it must stop the proxy.
func TestMCPRejectsRemovedHiddenFlag(t *testing.T) {
	isolateEnv(t)

	app := NewApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	err := app.Run(t.Context(), []string{"padmark-mcp", "--upload-allow-hidden"})
	require.ErrorContains(t, err, "flag provided but not defined")
}

// TestMCPRejectsRemovedSlugFlag checks that the proxy has no slug limit of its own. The unreleased
// v0.5.0 had such a flag; the server flag --custom-slugs now controls slugs for all records.
func TestMCPRejectsRemovedSlugFlag(t *testing.T) {
	isolateEnv(t)

	app := NewApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	err := app.Run(t.Context(), []string{"padmark-mcp", "--upload-allow-slug"})
	require.ErrorContains(t, err, "flag provided but not defined")
}

// lockedBuffer is a bytes.Buffer safe to write from the server/client goroutines while the
// test reads it.
type lockedBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

type DebugLogSuite struct {
	suite.Suite
}

func TestDebugLogSuite(t *testing.T) {
	suite.Run(t, new(DebugLogSuite))
}

// swapStdio replaces os.Stdin and os.Stdout with pipes until the end of the test. It returns the
// write end of the new stdin and the read end of the new stdout.
func (s *DebugLogSuite) swapStdio() (stdinW, stdoutR *os.File) {
	stdinR, stdinW, err := os.Pipe()
	s.Require().NoError(err)

	stdoutR, stdoutW, err := os.Pipe()
	s.Require().NoError(err)

	savedIn, savedOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdinR, stdoutW

	s.T().Cleanup(func() {
		os.Stdin, os.Stdout = savedIn, savedOut

		_ = stdinR.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	})

	return stdinW, stdoutR
}

// TestMCPDebugLogLevel_LogsToStderrOnly runs the real binary entry point (NewApp → stdio
// server) with --log-level=debug and drives one check_server call over swapped stdin/stdout
// pipes. The probe hits a closed server, which check_server logs at debug level: the record
// must reach the command's stderr writer, and stdout must carry nothing but JSON-RPC frames.
func (s *DebugLogSuite) TestMCPDebugLogLevel_LogsToStderrOnly() {
	isolateEnv(s.T())

	// A closed server: the healthz probe fails fast with connection refused.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	stdinW, stdoutR := s.swapStdio()

	var stderr, stdout lockedBuffer

	app := NewApp()
	app.ErrWriter = &stderr

	done := make(chan error, 1)

	go func() {
		done <- app.Run(s.T().Context(), []string{
			"padmark-mcp", "--" + FlagLogLevel, "debug", "--" + FlagBaseURL, deadURL,
		})
	}()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name: "test-client", Version: "0",
		Title: "", Description: "", WebsiteURL: "", Icons: nil,
	}, nil)

	session, err := client.Connect(s.T().Context(), &mcpsdk.IOTransport{
		Reader: io.NopCloser(io.TeeReader(stdoutR, &stdout)),
		Writer: stdinW, // closed by session.Close: the server sees EOF and Run returns.

		MaxLineLength: 0,
	}, nil)
	s.Require().NoError(err)

	_, err = session.CallTool(s.T().Context(), &mcpsdk.CallToolParams{
		Name: "check_server",
		Meta: nil, Arguments: nil, InputResponses: nil, RequestState: "",
	})
	s.Require().NoError(err)

	s.Require().NoError(session.Close())
	s.Require().NoError(<-done)

	logs := stderr.String()
	s.Contains(logs, "level=DEBUG", "debug records must be emitted at --log-level=debug")
	s.Contains(logs, "check_server: healthz failed")

	out := stdout.String()
	s.NotContains(out, "level=", "log records must never reach stdout")

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		s.True(json.Valid([]byte(line)), "stdout must carry only JSON-RPC frames, got %q", line)
		s.Contains(line, `"jsonrpc"`)
	}
}
