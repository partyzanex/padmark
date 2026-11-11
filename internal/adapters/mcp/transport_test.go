package mcp

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
