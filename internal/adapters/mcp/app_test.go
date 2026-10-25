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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPRejectsUnknownLogLevel(t *testing.T) {
	isolateEnv(t)

	app := NewApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	err := app.Run(t.Context(), []string{"padmark-mcp", "--" + FlagLogLevel, "debig"})
	require.ErrorContains(t, err, "invalid log level")
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

// TestMCPDebugLogLevel_LogsToStderrOnly runs the real binary entry point (NewApp → stdio
// server) with --log-level=debug and drives one check_server call over swapped stdin/stdout
// pipes. The probe hits a closed server, which check_server logs at debug level: the record
// must reach the command's stderr writer, and stdout must carry nothing but JSON-RPC frames.
func TestMCPDebugLogLevel_LogsToStderrOnly(t *testing.T) {
	isolateEnv(t)

	// A closed server: the healthz probe fails fast with connection refused.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	stdinR, stdinW, err := os.Pipe()
	require.NoError(t, err)

	stdoutR, stdoutW, err := os.Pipe()
	require.NoError(t, err)

	savedIn, savedOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdinR, stdoutW

	t.Cleanup(func() {
		os.Stdin, os.Stdout = savedIn, savedOut

		_ = stdinR.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	})

	var stderr, stdout lockedBuffer

	app := NewApp()
	app.ErrWriter = &stderr

	done := make(chan error, 1)

	go func() {
		done <- app.Run(t.Context(), []string{
			"padmark-mcp", "--" + FlagLogLevel, "debug", "--" + FlagBaseURL, deadURL,
		})
	}()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)

	session, err := client.Connect(t.Context(), &mcpsdk.IOTransport{
		Reader: io.NopCloser(io.TeeReader(stdoutR, &stdout)),
		Writer: stdinW, // closed by session.Close: the server sees EOF and Run returns
	}, nil)
	require.NoError(t, err)

	_, err = session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "check_server"})
	require.NoError(t, err)

	require.NoError(t, session.Close())
	require.NoError(t, <-done)

	logs := stderr.String()
	assert.Contains(t, logs, "level=DEBUG", "debug records must be emitted at --log-level=debug")
	assert.Contains(t, logs, "check_server: healthz failed")

	out := stdout.String()
	assert.NotContains(t, out, "level=", "log records must never reach stdout")

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		assert.True(t, json.Valid([]byte(line)), "stdout must carry only JSON-RPC frames, got %q", line)
		assert.Contains(t, line, `"jsonrpc"`)
	}
}
