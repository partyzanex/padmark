package middleware

import (
	"bytes"
	"log/slog"
	net_http "net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestStatusRecorder_WriteHeader_DelegatesOnce verifies that calling WriteHeader twice
// on statusRecorder only delegates to the underlying ResponseWriter once.
// The second call must be a no-op: the underlying writer must not receive it, because
// net/http logs a "superfluous response.WriteHeader call" warning when it happens.
func TestStatusRecorder_WriteHeader_DelegatesOnce(t *testing.T) {
	spy := &spyResponseWriter{
		ResponseWriter: nil, writeHeaderCalls: 0, lastStatus: 0,
	}
	sre := &statusRecorder{ResponseWriter: spy,
		status: 0, written: false,
	}

	sre.WriteHeader(net_http.StatusOK)
	sre.WriteHeader(net_http.StatusInternalServerError) // must be ignored

	assert.Equal(t, 1, spy.writeHeaderCalls,
		"underlying ResponseWriter.WriteHeader must be called exactly once")
	assert.Equal(t, net_http.StatusOK, sre.status,
		"sr.status must reflect the first call")
	assert.Equal(t, net_http.StatusOK, spy.lastStatus,
		"underlying writer must have received only the first status")
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
		got := SafeNextURL(tc.input)
		assert.Equal(t, tc.want, got, "safeNextURL(%q)", tc.input)
	}
}

func TestIsPublicRoute(t *testing.T) {
	named := domain.ReservedSlugPrefixes()

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
		// Mutations pass the middleware too: per-note privacy (reads) and edit-code
		// ownership (writes) are enforced downstream, per resource — see isPublicRoute's doc.
		{net_http.MethodPost, "/notes", true},
		{net_http.MethodPut, "/notes/abc123", true},
		{net_http.MethodDelete, "/notes/abc123", true},
		{net_http.MethodDelete, "/project/GUIDE.md", true},
		{net_http.MethodGet, "/edit/abc", true},
		{net_http.MethodPost, "/links", true},
		{net_http.MethodPost, "/links/bulk", true},
		{net_http.MethodDelete, "/links/abc123", true},
		{net_http.MethodGet, "/r/abc123", true},
		{net_http.MethodGet, "/abc123", true},
		{net_http.MethodGet, "/unknown-page", true}, // any non-reserved single segment is a candidate note slug by design
		// Reserved first segments stay non-public even when multi-segment — regression guard so
		// path-like slugs don't open up protected routes. /edit/{id} is the named route opened
		// up (GET only — EditPage re-checks per-note privacy); non-GET methods on it are not.
		{net_http.MethodGet, "/edit/a/b", true},
		{net_http.MethodPost, "/edit/abc", false},
		{net_http.MethodPost, "/edit/project/GUIDE.md", false},
		{net_http.MethodPut, "/edit/abc", false},
		{net_http.MethodGet, "/admin/users", false},
		{net_http.MethodGet, "/notes/edit/x", false},
		{net_http.MethodGet, "/success", false},
		{net_http.MethodGet, "/", false},
		{net_http.MethodGet, "/login", false},
		{net_http.MethodGet, "/api", false},
		{net_http.MethodGet, "/healthz", false},
		{net_http.MethodGet, "/llms.txt", false},
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

	handler := WithRecovery(log, panicking)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(net_http.MethodGet, "/", nil))

	assert.Equal(t, net_http.StatusInternalServerError, rec.Code)
	assert.Contains(t, buf.String(), "panic recovered")
}

// TestWithRecovery_OutermostLayerCoversMiddlewarePanic guards the fix for a panic occurring
// in a middleware sitting between the router's two WithRecovery layers (auth, CSRF,
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
	stack := WithRecovery(log, mux)
	stack = panicMiddleware(stack)
	stack = WithRecovery(log, stack)

	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, httptest.NewRequest(net_http.MethodGet, "/", nil))

	assert.Equal(t, net_http.StatusInternalServerError, rec.Code)
	assert.Contains(t, buf.String(), "panic recovered")
}

// TestLogRoute never treats a note slug as a built-in route name.
func TestLogRoute(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"", ""},
		{"/", ""},
		{"/healthz", "healthz"},
		{"/secret-slug", "note"},
		{"/project/GUIDE.md", "note"},
		{"/notes/project/GUIDE.md", "notes"},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.want, logRoute(tc.path), "logRoute(%q)", tc.path)
	}
}

// TestWithLogging_RouteAttrExcludesSlug checks that access logging omits raw slugs.
func TestWithLogging_RouteAttrExcludesSlug(t *testing.T) {
	var buf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&buf, nil))
	next := net_http.HandlerFunc(func(w net_http.ResponseWriter, _ *net_http.Request) {
		w.WriteHeader(net_http.StatusOK)
	})

	handler := WithLogging(log, next)
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

	handler := WithLogging(log, next)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(net_http.MethodGet, "/healthz", nil)

	handler.ServeHTTP(rec, req)

	assert.Contains(t, buf.String(), `"route":"healthz"`)
}

func TestWithLoggingNeverRecordsSecretPaths(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		for _, path := range []string{"/top-secret-slug", "/notes/top-secret-slug", "/r/top-secret-slug"} {
			var buf bytes.Buffer

			log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level,
				AddSource: false, ReplaceAttr: nil,
			}))
			handler := WithLogging(log, net_http.HandlerFunc(func(w net_http.ResponseWriter, _ *net_http.Request) {
				w.WriteHeader(net_http.StatusOK)
			}))
			req := httptest.NewRequest(net_http.MethodGet, path+"?edit_code=secret-code", nil)
			handler.ServeHTTP(httptest.NewRecorder(), req)
			assert.NotContains(t, buf.String(), "top-secret-slug")
			assert.NotContains(t, buf.String(), "secret-code")

			if level == slog.LevelDebug {
				assert.Contains(t, buf.String(), `"path_hash"`)
			}
		}
	}
}

type spyResponseWriter struct {
	net_http.ResponseWriter

	writeHeaderCalls int
	lastStatus       int
}

func (s *spyResponseWriter) Header() net_http.Header     { return net_http.Header{} }
func (s *spyResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (s *spyResponseWriter) WriteHeader(status int)      { s.writeHeaderCalls++; s.lastStatus = status }
