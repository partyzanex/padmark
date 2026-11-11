package web_test

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// editorPreviewScripts are the same-origin scripts that render the editor preview in the browser.
// The order is significant: editor-preview.js uses the globals of the two vendored libraries.
func editorPreviewScripts() []string {
	return []string{"/static/marked.umd.js", "/static/purify.min.js", "/static/editor-preview.js"}
}

// requireEditorPreviewScripts makes sure that the editor page loads each preview script from
// /static/ with a content-hash version and the CSP nonce, in the necessary order.
func requireEditorPreviewScripts(t *testing.T, router http.Handler, path string) {
	t.Helper()

	page := httptest.NewRecorder()
	router.ServeHTTP(page, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, page.Code, "the editor page must be served")

	scriptSrc := scriptSrcDirectiveRe.FindStringSubmatch(page.Header().Get("Content-Security-Policy"))
	require.Len(t, scriptSrc, 2, "the editor CSP must have a script-src directive")

	nonce := strings.TrimSuffix(strings.TrimPrefix(cspNonceRe.FindString(scriptSrc[1]), "'nonce-"), "'")
	require.NotEmpty(t, nonce, "script-src must carry a nonce")

	// html/template entity-encodes '+' in attributes (&#43;); the browser decodes it back.
	body := html.UnescapeString(page.Body.String())
	last := -1

	for _, script := range editorPreviewScripts() {
		asset := httptest.NewRecorder()
		router.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, script, nil))
		require.Equal(t, http.StatusOK, asset.Code, "%s must be embedded", script)
		assert.Contains(t, asset.Header().Get("Content-Type"), "javascript", script)

		sum := sha256.Sum256(asset.Body.Bytes())
		tag := `<script src="` + script + `?p=` + hex.EncodeToString(sum[:])[:12] + `" nonce="` + nonce + `">`

		index := strings.Index(body, tag)
		require.GreaterOrEqual(t, index, 0, "the editor must load %s with a version and the nonce", script)
		assert.Greater(t, index, last, "%s must load after the scripts it uses", script)

		last = index
	}

	assert.Contains(t, body, `id="preview"`, "the editor must have the preview pane")
	assert.Contains(t, body, `role="tablist" aria-label="Editor view"`, "the editor must have the view tabs")
}

// TestIndexPage_PreviewScripts covers the create editor: the preview libraries are vendored and
// load as same-origin scripts under the global CSP.
func (s *HandlerSuite) TestIndexPage_PreviewScripts() {
	requireEditorPreviewScripts(s.T(), s.router, "/")
}

// TestWebUI_PreviewWithoutAPI covers --disable-api: the editor is read-only, but its preview
// needs only static files, so it continues to operate.
func (s *DisableAPISuite) TestWebUI_PreviewWithoutAPI() {
	requireEditorPreviewScripts(s.T(), s.newRouter(true), "/")
}
