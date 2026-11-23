package web

import (
	"bytes"
	"context"
	"log/slog"
	net_http "net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spyResponseWriter counts how many times WriteHeader is called by the code under test.
type spyResponseWriter struct {
	net_http.ResponseWriter

	writeHeaderCalls int
	lastStatus       int
}

func (s *spyResponseWriter) Header() net_http.Header     { return net_http.Header{} }
func (s *spyResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (s *spyResponseWriter) WriteHeader(status int) {
	s.writeHeaderCalls++
	s.lastStatus = status
}

// errorWriter always fails on Write — used to simulate a broken connection.
type errorWriter struct {
	spyResponseWriter

	body []byte
}

func (e *errorWriter) Write(b []byte) (int, error) {
	e.body = append(e.body, b...)

	return 0, assert.AnError
}

// TestAPISpec_WriteError_DoesNotCorruptBody verifies that when w.Write fails,
// APISpec does not call http.Error and therefore does not append error text to the body.
func TestAPISpec_WriteError_DoesNotCorruptBody(t *testing.T) {
	wer := &errorWriter{
		ResponseWriter: nil, writeHeaderCalls: 0, lastStatus: 0, body: nil,
	}
	req, _ := net_http.NewRequest(net_http.MethodGet, "/api/openapi.yaml", nil)

	APISpec(slog.New(slog.DiscardHandler))(wer, req)

	// http.Error writes "failed to write spec\n" — assert it is absent.
	assert.NotContains(t, string(wer.body), "failed to write spec",
		"http.Error must not be called after a partial Write")
	// WriteHeader must not have been called — no status change attempted.
	assert.Equal(t, 0, wer.writeHeaderCalls,
		"WriteHeader must not be called when Write fails")
}

// TestOpenAPISpecInSync verifies that spec/openapi.yaml (the embedded copy) is identical
// to the root openapi.yaml (the source of truth). Run `go generate ./internal/adapters/web/`
// to regenerate the copy when the root file changes.
func TestOpenAPISpecInSync(t *testing.T) {
	root, err := os.ReadFile("../../../openapi.yaml")
	require.NoError(t, err, "read root openapi.yaml")

	spec, err := os.ReadFile("spec/openapi.yaml")
	require.NoError(t, err, "read spec/openapi.yaml")

	assert.True(t, bytes.Equal(root, spec),
		"spec/openapi.yaml is out of sync with root openapi.yaml; run: go generate ./internal/adapters/web/")
}

// TestAPISpec_OK_SetsContentType verifies the happy path: correct Content-Type header.
func TestAPISpec_OK_SetsContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	req, _ := net_http.NewRequest(net_http.MethodGet, "/api/openapi.yaml", nil)

	APISpec(slog.New(slog.DiscardHandler))(rec, req)

	assert.Equal(t, net_http.StatusOK, rec.Code)
	assert.Equal(t, "application/yaml", rec.Header().Get("Content-Type"))
	assert.NotEmpty(t, rec.Body.Bytes())
}

func TestNoPinger(t *testing.T) {
	var pinger Pinger = NoPinger{}

	assert.NoError(t, pinger.PingContext(context.Background()))
}

// ogenSpy stands in for the generated ogen server. It counts the requests that reach it.
type ogenSpy struct {
	calls int
}

func (o *ogenSpy) ServeHTTP(w net_http.ResponseWriter, _ *net_http.Request) {
	o.calls++

	w.WriteHeader(net_http.StatusTeapot)
}

// TestFileRoutes_ServedByFileHandler verifies that each /files path goes to FileHandler and
// never to the ogen server. The generated server buffers multipart bodies, so a /files route on
// it would break the streaming upload.
func TestFileRoutes_ServedByFileHandler(t *testing.T) {
	// The managers stay nil: the test only matches routes and never calls a handler.
	handler, err := NewHandler(&HandlerConfig{
		Notes: nil, Links: nil, Pinger: NoPinger{}, Log: slog.New(slog.DiscardHandler),
		Accounts: nil, Files: nil, Reveal: nil,
		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, CSRFSecret: nil, SessionTTL: 0,
		FileTransfer:   FileTransferLimits{IdleTimeout: 0, MaxUploads: 0, MaxUploadsPerClient: 0},
		DirectRedirect: false,
	})
	require.NoError(t, err)

	spy := &ogenSpy{calls: 0}
	mux := net_http.NewServeMux()

	registerAllRoutes(mux, handler, spy)

	cases := []struct {
		method  string
		path    string
		pattern string
	}{
		{net_http.MethodPost, "/files", "POST /files"},
		{net_http.MethodGet, "/files/abc", "GET /files/{id}"},
		{net_http.MethodGet, "/files/abc/download", "GET /files/{id}/download"},
		{net_http.MethodHead, "/files/abc/download", "GET /files/{id}/download"},
		{net_http.MethodPut, "/files/abc", "PUT /files/{id}"},
		{net_http.MethodDelete, "/files/abc", "DELETE /files/{id}"},
	}

	for _, route := range cases {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil)

			_, pattern := mux.Handler(req)
			require.Equal(t, route.pattern, pattern)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			// Without a FileManager, FileHandler answers 503 (files are not enabled).
			assert.Equal(t, net_http.StatusServiceUnavailable, rec.Code)
			assert.Zero(t, spy.calls, "the ogen server must not serve %s %s", route.method, route.path)
		})
	}
}
