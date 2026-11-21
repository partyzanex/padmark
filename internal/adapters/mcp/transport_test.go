package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

const testBearer = "test-bearer-token"

// authRecorder is a test server that records the Authorization header of each request.
type authRecorder struct {
	server *httptest.Server
	seen   []string
	mu     sync.Mutex
}

func newAuthRecorder(t *testing.T, next http.HandlerFunc) *authRecorder {
	t.Helper()

	rec := &authRecorder{server: nil, seen: nil, mu: sync.Mutex{}}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.seen = append(rec.seen, r.Header.Get("Authorization"))
		rec.mu.Unlock()

		next(w, r)
	}))
	t.Cleanup(rec.server.Close)

	return rec
}

func (rec *authRecorder) headers() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	return append([]string(nil), rec.seen...)
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// getWith sends a GET bound to the test context through client.
func getWith(t *testing.T, client *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	require.NoError(t, err)

	return client.Do(req)
}

// TestBearerClient_SameOriginRedirect_KeepsToken: a redirect on the same origin is followed and
// still carries the token.
func TestBearerClient_SameOriginRedirect_KeepsToken(t *testing.T) {
	t.Parallel()

	server := newAuthRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)

			return
		}

		ok(w, r)
	})

	client, err := newBearerClient(server.server.URL, testBearer, time.Minute)
	require.NoError(t, err)

	resp, err := getWith(t, client, server.server.URL+"/start")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"Bearer " + testBearer, "Bearer " + testBearer}, server.headers())
}

// TestBearerClient_CrossOriginRedirect_Refused: a redirect to another origin (here another port)
// is refused, and the other server never receives a request.
func TestBearerClient_CrossOriginRedirect_Refused(t *testing.T) {
	t.Parallel()

	other := newAuthRecorder(t, ok)
	server := newAuthRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.server.URL+"/steal", http.StatusFound)
	})

	client, err := newBearerClient(server.server.URL, testBearer, time.Minute)
	require.NoError(t, err)

	resp, err := getWith(t, client, server.server.URL+"/start")
	if resp != nil {
		require.NoError(t, resp.Body.Close())
	}

	require.ErrorIs(t, err, errCrossOriginRedirect)
	assert.Empty(t, other.headers(), "the other origin must not receive any request")
}

// TestBearerClient_OtherOrigin_NoToken: a direct request to another origin goes out without the
// token, so the transport itself never leaks it.
func TestBearerClient_OtherOrigin_NoToken(t *testing.T) {
	t.Parallel()

	server := newAuthRecorder(t, ok)
	other := newAuthRecorder(t, ok)

	client, err := newBearerClient(server.server.URL, testBearer, time.Minute)
	require.NoError(t, err)

	resp, err := getWith(t, client, other.server.URL+"/x")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, []string{""}, other.headers())
}

// TestBearerClient_TooManyRedirects: a redirect loop on the same origin stops at the limit.
func TestBearerClient_TooManyRedirects(t *testing.T) {
	t.Parallel()

	server := newAuthRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})

	client, err := newBearerClient(server.server.URL, testBearer, time.Minute)
	require.NoError(t, err)

	resp, err := getWith(t, client, server.server.URL+"/loop")
	if resp != nil {
		require.NoError(t, resp.Body.Close())
	}

	require.ErrorIs(t, err, errTooManyRedirects)
}

// TestTransferClient_HasNoTotalTimeout: the upload client limits only the wait for the response
// headers, so a large upload on a slow link is not stopped.
func TestTransferClient_HasNoTotalTimeout(t *testing.T) {
	t.Parallel()

	client, err := newTransferClient("https://pad.example.com", testBearer, 7*time.Second)
	require.NoError(t, err)

	assert.Zero(t, client.Timeout, "the transfer client has no total timeout")

	bearer, ok := client.Transport.(*bearerTransport)
	require.True(t, ok, "the transfer client keeps the origin-bound bearer transport")

	base, ok := bearer.base.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 7*time.Second, base.ResponseHeaderTimeout)
	assert.NotSame(t, http.DefaultTransport, base, "the default transport is cloned, not changed")
}

// TestTransferClient_SlowUploadOutlivesTimeout: an upload that takes longer than --timeout
// completes with the transfer client, and stops with the total timeout of the API client.
func TestTransferClient_SlowUploadOutlivesTimeout(t *testing.T) {
	t.Parallel()

	const (
		timeout = 50 * time.Millisecond
		chunks  = 4
	)

	server := newAuthRecorder(t, uploadCreated(t))

	transfer, err := newTransferClient(server.server.URL, testBearer, timeout)
	require.NoError(t, err)

	created, err := uploadThrough(t, transfer, server.server.URL, newSlowReader(chunks, 2*timeout))
	require.NoError(t, err, "the transfer client must not stop a slow upload")
	assert.Equal(t, "file123abc", created.ID)

	api, err := newBearerClient(server.server.URL, testBearer, timeout)
	require.NoError(t, err)

	_, err = uploadThrough(t, api, server.server.URL, newSlowReader(chunks, 2*timeout))
	require.Error(t, err, "the total timeout of the API client stops the slow upload")
	assert.True(t, isTimeout(err), "the error is a timeout: %v", err)
}

// TestTransferClient_HeaderTimeoutGetsTimeoutText: a server that does not answer gets the
// timeout text, not the transport text that tells the agent to check that the server runs.
func TestTransferClient_HeaderTimeoutGetsTimeoutText(t *testing.T) {
	t.Parallel()

	server := newAuthRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // the test needs no body; a read error changes nothing.

		<-r.Context().Done()
	})

	transfer, err := newTransferClient(server.server.URL, testBearer, 50*time.Millisecond)
	require.NoError(t, err)

	_, err = uploadThrough(t, transfer, server.server.URL, strings.NewReader("x"))
	require.Error(t, err)

	text := uploadError(err).Error()
	assert.Contains(t, text, ErrUploadTimeout.Error())
	assert.NotContains(t, text, "is padmark-server running")
}

// --- helpers ---

// uploadThrough sends body through the streaming upload of pkg/client with httpClient.
func uploadThrough(
	t *testing.T, httpClient *http.Client, serverURL string, body io.Reader,
) (*padmark.CreateFileResponse, error) {
	t.Helper()

	client, err := padmark.NewClient(serverURL, padmark.WithClient(httpClient))
	require.NoError(t, err)

	return client.UploadFile(t.Context(), "a.bin", body, padmark.UploadFileOptions{
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
