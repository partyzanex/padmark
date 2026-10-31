package web_test

import (
	"io/fs"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestTemplatesHaveNoInlineStyleOrHandlers guards the Content-Security-Policy: it allows only
// nonce'd <script>/<style> blocks, so a style="…" attribute or an inline on<event>="…" handler
// in any served HTML would be silently blocked by the browser. Behaviour belongs in the nonce'd
// script (addEventListener), presentation in the stylesheet. Assigning element.style from that
// script is CSSOM, not an attribute, and is not matched here.
func TestTemplatesHaveNoInlineStyleOrHandlers(t *testing.T) {
	// An attribute is preceded by whitespace inside a tag; JS property access (btn.style,
	// window.onload) is preceded by a dot and never matches.
	inlineAttr := regexp.MustCompile(`(?i)\s(style|on[a-z]+)\s*=`)

	pkgDir := os.DirFS(".")

	var files []string

	for _, pattern := range []string{"templates/*.html", "static/*.html"} {
		matches, err := fs.Glob(pkgDir, pattern)
		require.NoError(t, err)

		files = append(files, matches...)
	}

	require.NotEmpty(t, files, "expected HTML templates under templates/")

	for _, file := range files {
		src, err := fs.ReadFile(pkgDir, file)
		require.NoError(t, err)

		for _, match := range inlineAttr.FindAllString(string(src), -1) {
			assert.Failf(t, "inline attribute blocked by CSP", "%s contains %q", file, match)
		}
	}
}

// TestEditorMatchesEditCodeErrorMessage pins the editor's save() contract: it shows "Invalid edit
// code" only for a 403 whose JSON message is exactly domain.ErrInvalidEditCode's text (other 403s,
// e.g. the cross-origin rejection, show the server's own message). Renaming the sentinel's text
// must update the template too.
func TestEditorMatchesEditCodeErrorMessage(t *testing.T) {
	src, err := os.ReadFile("templates/index.html")
	require.NoError(t, err)

	assert.Contains(t, string(src), "msg === '"+domain.ErrInvalidEditCode.Error()+"'")
}
