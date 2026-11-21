package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsFilePage checks that --disable-api keeps only the browser file pages and the download
// form, and no other mutation of a file.
func TestIsFilePage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/files", true},
		{http.MethodHead, "/files", true},
		{http.MethodGet, "/files/abc/edit", true},
		{http.MethodGet, "/files/abc/download", true},
		{http.MethodHead, "/files/abc/download", true},
		{http.MethodPost, "/files/abc/download", true},
		{http.MethodGet, "/files/abc", false},
		{http.MethodGet, "/files//download", false},
		{http.MethodGet, "/files/abc/download/extra", false},
		{http.MethodPost, "/files", false},
		{http.MethodPost, "/files/abc/edit", false},
		{http.MethodPost, "/files//download", false},
		{http.MethodPost, "/files/abc", false},
		{http.MethodPut, "/files/abc", false},
		{http.MethodPut, "/files/abc/download", false},
		{http.MethodDelete, "/files/abc", false},
		{http.MethodDelete, "/files/abc/download", false},
		{http.MethodPatch, "/files/abc/download", false},
		{http.MethodPost, "/notes/abc/download", false},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		assert.Equal(t, tc.want, isFilePage(req), "%s %s", tc.method, tc.path)
	}
}

// TestWithAPIDisabled_FileDownloadForm checks that the burn-after-download form reaches the
// handler while the upload and the other file mutations get 503.
func TestWithAPIDisabled_FileDownloadForm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/files/abc/download", http.StatusNoContent},
		{http.MethodPost, "/files", http.StatusServiceUnavailable},
		{http.MethodPut, "/files/abc", http.StatusServiceUnavailable},
		{http.MethodDelete, "/files/abc", http.StatusServiceUnavailable},
	}

	log := slog.New(slog.DiscardHandler)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := WithAPIDisabled(log, noPage, next)

	for _, tc := range tests {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		assert.Equal(t, tc.want, rec.Code, "%s %s", tc.method, tc.path)
	}
}

// noPage is the page matcher of a server without pages on the API paths.
func noPage(*http.Request) bool { return false }

// TestIsLinkPage checks that --disable-api keeps the short-link pages with a fixed path, and no
// mutation of a short link.
func TestIsLinkPage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/links", true},
		{http.MethodHead, "/links", true},
		{http.MethodGet, "/links/abc/edit", true},
		{http.MethodHead, "/links/abc/edit", true},
		{http.MethodGet, "/links/abc", false},
		{http.MethodGet, "/links//edit", false},
		{http.MethodGet, "/links/abc/other", false},
		{http.MethodPost, "/links", false},
		{http.MethodPost, "/links/bulk", false},
		{http.MethodPut, "/links/abc", false},
		{http.MethodDelete, "/links/abc", false},
		{http.MethodGet, "/notes/abc/edit", false},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		assert.Equal(t, tc.want, isLinkPage(req), "%s %s", tc.method, tc.path)
	}
}

// TestWithAPIDisabled_PageMatcher checks that a request on an API path passes only when the page
// matcher of the caller selects it; the JSON read of the same path gets 503.
func TestWithAPIDisabled_PageMatcher(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	htmlOnly := func(r *http.Request) bool { return r.Header.Get("Accept") == "text/html" }
	handler := WithAPIDisabled(log, htmlOnly, next)

	page := httptest.NewRequest(http.MethodGet, "/links/abc", nil)
	page.Header.Set("Accept", "text/html")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, page)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	read := httptest.NewRequest(http.MethodGet, "/links/abc", nil)
	read.Header.Set("Accept", "application/json")

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, read)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
