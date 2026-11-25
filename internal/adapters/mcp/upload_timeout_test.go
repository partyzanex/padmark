package mcp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/clientauth"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

const testBearer = "test-bearer-token"

// newUploadServer starts a test server with next as its handler.
func newUploadServer(t *testing.T, next http.HandlerFunc) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(next)
	t.Cleanup(server.Close)

	return server
}

// TestHTTPClient_SlowUploadOutlivesTimeout: an upload that takes longer than --timeout
// completes without a call deadline, as upload_file sends it. The same request in a tool with
// the call deadline (limitCall) stops with a timeout.
func TestHTTPClient_SlowUploadOutlivesTimeout(t *testing.T) {
	t.Parallel()

	const (
		timeout = 50 * time.Millisecond
		chunks  = 4
	)

	server := newUploadServer(t, uploadCreated(t))

	httpClient, err := clientauth.NewHTTPClient(server.URL, testBearer, timeout)
	require.NoError(t, err)

	created, err := uploadThrough(t.Context(), t, httpClient, server.URL, newSlowReader(chunks, 2*timeout))
	require.NoError(t, err, "the header timeout must not stop a slow upload")
	assert.Equal(t, "file123abc", created.ID)

	proxy := NewServer(nil, slog.New(slog.DiscardHandler), WithCallTimeout(timeout))
	limited := limitCall(proxy, func(
		ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{},
	) (*mcpsdk.CallToolResult, struct{}, error) {
		_, uploadErr := uploadThrough(ctx, t, httpClient, server.URL, newSlowReader(chunks, 2*timeout))

		return nil, struct{}{}, uploadErr
	})

	_, _, err = limited(t.Context(), nil, struct{}{})
	require.Error(t, err, "the call deadline stops the slow request")
	assert.True(t, isTimeout(err), "the error is a timeout: %v", err)
}

// TestHTTPClient_HeaderTimeoutGetsTimeoutText: a server that does not answer gets the
// timeout text, not the transport text that tells the agent to check that the server runs.
func TestHTTPClient_HeaderTimeoutGetsTimeoutText(t *testing.T) {
	t.Parallel()

	server := newUploadServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // the test needs no body; a read error changes nothing.

		<-r.Context().Done()
	})

	httpClient, err := clientauth.NewHTTPClient(server.URL, testBearer, 50*time.Millisecond)
	require.NoError(t, err)

	_, err = uploadThrough(t.Context(), t, httpClient, server.URL, strings.NewReader("x"))
	require.Error(t, err)

	text := uploadError(err).Error()
	assert.Contains(t, text, errUploadTimeout.Error())
	assert.NotContains(t, text, "is padmark-server running")
}

// --- helpers ---

// uploadThrough sends body through the streaming upload of pkg/client with httpClient in ctx.
func uploadThrough(
	ctx context.Context, t *testing.T, httpClient *http.Client, serverURL string, body io.Reader,
) (*padmark.CreateFileResponse, error) {
	t.Helper()

	client, err := padmark.NewClient(serverURL, padmark.WithClient(httpClient))
	require.NoError(t, err)

	return client.UploadFile(ctx, "a.bin", body, padmark.UploadFileOptions{
		TTL: 0, Privacy: "", Slug: "", EditCode: "", BurnAfterDownload: false,
	})
}

// uploadCreated reads the full upload body and answers 201 with a small file.
func uploadCreated(t *testing.T) http.HandlerFunc {
	t.Helper()

	created := padmark.CreateFileResponse{
		ID: "file123abc", URL: testURL(t, "https://pad.example.com/file123abc"), Name: "a.bin", Size: 4,
		Privacy: padmark.CreateFileResponsePrivacyPublic, Downloads: 0, BurnAfterDownload: false,
		ExpiresAt: padmark.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
		CreatedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), EditCode: "code-12345678",
	}

	data, err := created.MarshalJSON()
	require.NoError(t, err)

	return func(w http.ResponseWriter, r *http.Request) {
		_, copyErr := io.Copy(io.Discard, r.Body)
		if copyErr != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data) // the client reports a short body; the test checks the result.
	}
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
