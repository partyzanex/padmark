package http

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

// TestStatusRecorder_WriteHeader_DelegatesOnce verifies that calling WriteHeader twice
// on statusRecorder only delegates to the underlying ResponseWriter once.
// The second call must be a no-op: the underlying writer must not receive it, because
// net/http logs a "superfluous response.WriteHeader call" warning when it happens.
func TestStatusRecorder_WriteHeader_DelegatesOnce(t *testing.T) {
	spy := &spyResponseWriter{}
	sre := &statusRecorder{ResponseWriter: spy}

	sre.WriteHeader(net_http.StatusOK)
	sre.WriteHeader(net_http.StatusInternalServerError) // must be ignored

	assert.Equal(t, 1, spy.writeHeaderCalls,
		"underlying ResponseWriter.WriteHeader must be called exactly once")
	assert.Equal(t, net_http.StatusOK, sre.status,
		"sr.status must reflect the first call")
	assert.Equal(t, net_http.StatusOK, spy.lastStatus,
		"underlying writer must have received only the first status")
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
	wer := &errorWriter{}
	req, _ := net_http.NewRequest(net_http.MethodGet, "/api/openapi.yaml", nil)

	APISpec(wer, req)

	// http.Error writes "failed to write spec\n" — assert it is absent.
	assert.NotContains(t, string(wer.body), "failed to write spec",
		"http.Error must not be called after a partial Write")
	// WriteHeader must not have been called — no status change attempted.
	assert.Equal(t, 0, wer.writeHeaderCalls,
		"WriteHeader must not be called when Write fails")
}

// TestOpenAPISpecInSync verifies that spec/openapi.yaml (the embedded copy) is identical
// to the root openapi.yaml (the source of truth). Run `go generate ./internal/adapters/http/`
// to regenerate the copy when the root file changes.
func TestOpenAPISpecInSync(t *testing.T) {
	root, err := os.ReadFile("../../../openapi.yaml")
	require.NoError(t, err, "read root openapi.yaml")

	spec, err := os.ReadFile("spec/openapi.yaml")
	require.NoError(t, err, "read spec/openapi.yaml")

	assert.True(t, bytes.Equal(root, spec),
		"spec/openapi.yaml is out of sync with root openapi.yaml; run: go generate ./internal/adapters/http/")
}

// TestAPISpec_OK_SetsContentType verifies the happy path: correct Content-Type header.
func TestAPISpec_OK_SetsContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	req, _ := net_http.NewRequest(net_http.MethodGet, "/api/openapi.yaml", nil)

	APISpec(rec, req)

	assert.Equal(t, net_http.StatusOK, rec.Code)
	assert.Equal(t, "application/yaml", rec.Header().Get("Content-Type"))
	assert.NotEmpty(t, rec.Body.Bytes())
}

func TestNoPinger(t *testing.T) {
	var pinger Pinger = NoPinger{}

	assert.NoError(t, pinger.PingContext(context.Background()))
}

func TestSafeNextURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"/", "/"},
		{"/notes/abc123", "/notes/abc123"},
		{"/notes/abc?foo=bar", "/notes/abc?foo=bar"},
		{"https://evil.com", ""},
		{"//evil.com/path", ""},
		{"evil.com/path", ""},
		{"javascript:alert(1)", ""},
		{"/valid/../path", "/valid/../path"},
		// Backslash variants: browsers normalise "\" to "/", so these are open-redirect vectors.
		{`/\evil.com`, ""},
		{`/\/evil.com`, ""},
		{`\\evil.com`, ""},
		{`/path\to\file`, ""},
	}

	for _, tc := range tests {
		got := safeNextURL(tc.input)
		assert.Equal(t, tc.want, got, "safeNextURL(%q)", tc.input)
	}
}

func TestIsPublicRoute(t *testing.T) {
	named := buildNamedRoutes()

	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{net_http.MethodGet, "/notes/abc123", true},
		// Multi-segment slugs are now public note paths (non-reserved first segment).
		{net_http.MethodGet, "/notes/abc/extra", true},
		{net_http.MethodGet, "/a/b", true},
		{net_http.MethodGet, "/project/GUIDE.md", true},
		{net_http.MethodPost, "/project/GUIDE.md", true},
		{net_http.MethodPost, "/notes/abc123", true},
		{net_http.MethodPost, "/abc123", true},
		{net_http.MethodPost, "/notes", false},
		{net_http.MethodGet, "/abc123", true},
		{net_http.MethodGet, "/unknown-page", true}, // any non-reserved single segment is a candidate note slug by design
		// Reserved first segments stay non-public even when multi-segment — regression guard so
		// path-like slugs don't open up protected routes.
		{net_http.MethodGet, "/edit/abc", false},
		{net_http.MethodGet, "/edit/a/b", false},
		{net_http.MethodPost, "/edit/abc", false},
		{net_http.MethodPost, "/edit/project/GUIDE.md", false},
		{net_http.MethodGet, "/admin/users", false},
		{net_http.MethodGet, "/notes/edit/x", false},
		{net_http.MethodGet, "/success", false},
		{net_http.MethodGet, "/", false},
		{net_http.MethodGet, "/login", false},
		{net_http.MethodGet, "/api", false},
		{net_http.MethodGet, "/healthz", false},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		got := isPublicRoute(req, named)
		assert.Equal(t, tc.want, got, "%s %s", tc.method, tc.path)
	}
}

// TestWithRecovery_RecoversPanicAndLogs verifies the base case: a panic in the wrapped
// handler is turned into a 500 and a structured log entry, rather than propagating up.
func TestWithRecovery_RecoversPanicAndLogs(t *testing.T) {
	var buf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&buf, nil))
	panicking := net_http.HandlerFunc(func(net_http.ResponseWriter, *net_http.Request) {
		panic("boom")
	})

	handler := withRecovery(log, panicking)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(net_http.MethodGet, "/", nil))

	assert.Equal(t, net_http.StatusInternalServerError, rec.Code)
	assert.Contains(t, buf.String(), "panic recovered")
}

// TestWithRecovery_OutermostLayerCoversMiddlewarePanic guards the fix for a panic occurring
// in a middleware sitting between the router's two withRecovery layers (auth, CSRF,
// rate-limit, security-headers, logging, request-ID in NewRouter). Without the outermost
// recovery layer added in NewRouter, such a panic would bypass recovery entirely and fall
// through to net/http's bare per-connection recovery (no response body, no structured log).
func TestWithRecovery_OutermostLayerCoversMiddlewarePanic(t *testing.T) {
	var buf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&buf, nil))
	mux := net_http.HandlerFunc(func(w net_http.ResponseWriter, _ *net_http.Request) {
		w.WriteHeader(net_http.StatusOK)
	})

	// panicMiddleware stands in for a middleware sitting between the two recovery layers
	// in NewRouter (auth, CSRF, rate-limit, security-headers, logging, request-ID) that
	// panics before ever delegating to the inner handler.
	panicMiddleware := func(net_http.Handler) net_http.Handler {
		return net_http.HandlerFunc(func(net_http.ResponseWriter, *net_http.Request) {
			panic("middleware boom")
		})
	}

	// Mirrors NewRouter's shape: inner recovery around mux, a middleware layer that can
	// panic, then the outermost recovery layer added to fix this finding.
	stack := withRecovery(log, mux)
	stack = panicMiddleware(stack)
	stack = withRecovery(log, stack)

	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, httptest.NewRequest(net_http.MethodGet, "/", nil))

	assert.Equal(t, net_http.StatusInternalServerError, rec.Code)
	assert.Contains(t, buf.String(), "panic recovered")
}

// TestFirstPathSegment covers the helper used by withLogging to derive a leak-safe
// "route" attr from the request path (see TestWithLogging_RouteAttrExcludesSlug).
func TestFirstPathSegment(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"", ""},
		{"/", ""},
		{"/ping", "ping"},
		{"/notes/project/GUIDE.md", "notes"},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.want, firstPathSegment(tc.path), "firstPathSegment(%q)", tc.path)
	}
}

// TestWithLogging_RouteAttrExcludesSlug verifies that withLogging emits a "route" attr
// at info level containing only the first path segment (safe: identifies the endpoint) while
// the full "path" attr — which can embed a note slug, i.e. content-encryption key material —
// stays absent unless debug logging is enabled.
func TestWithLogging_RouteAttrExcludesSlug(t *testing.T) {
	var buf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&buf, nil))
	next := net_http.HandlerFunc(func(w net_http.ResponseWriter, _ *net_http.Request) {
		w.WriteHeader(net_http.StatusOK)
	})

	handler := withLogging(log, next)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(net_http.MethodGet, "/notes/project/GUIDE.md", nil)

	handler.ServeHTTP(rec, req)

	out := buf.String()
	assert.Contains(t, out, `"route":"notes"`)
	assert.NotContains(t, out, "GUIDE.md",
		"info-level log must not leak the note slug via the full path")
	assert.NotContains(t, out, `"path"`,
		"path attr must be absent at info level (debug disabled)")
}

// TestWithLogging_RouteAttrForSingleSegment covers a non-note, single-segment route to
// confirm the first segment is derived correctly regardless of path depth.
func TestWithLogging_RouteAttrForSingleSegment(t *testing.T) {
	var buf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&buf, nil))
	next := net_http.HandlerFunc(func(w net_http.ResponseWriter, _ *net_http.Request) {
		w.WriteHeader(net_http.StatusOK)
	})

	handler := withLogging(log, next)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(net_http.MethodGet, "/ping", nil)

	handler.ServeHTTP(rec, req)

	assert.Contains(t, buf.String(), `"route":"ping"`)
}
