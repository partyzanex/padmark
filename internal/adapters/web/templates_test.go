package web_test

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
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

// TestFileErrorScriptMatchesDomainErrors pins the file pages' error texts: fileErrorMessage in
// header.html selects a text by the exact server message of these domain errors. Renaming a
// sentinel's text must update the template too.
func TestFileErrorScriptMatchesDomainErrors(t *testing.T) {
	src, err := os.ReadFile("templates/header.html")
	require.NoError(t, err)

	for _, sentinel := range []error{domain.ErrInvalidEditCode, domain.ErrInvalidFileName, domain.ErrStorageUnavailable} {
		assert.Contains(t, string(src), "message === '"+sentinel.Error()+"'")
	}
}

// TestFileTemplatesUseErrorScript: the upload and edit pages show errors in #fileError through
// the shared fileErrorMessage, so both pages show the same texts.
func TestFileTemplatesUseErrorScript(t *testing.T) {
	pkgDir := os.DirFS(".")

	for _, file := range []string{"templates/file_editor.html", "templates/file_edit.html"} {
		src, err := fs.ReadFile(pkgDir, file)
		require.NoError(t, err)

		assert.Contains(t, string(src), `{{template "file-errors-js"}}`, file)
		assert.Contains(t, string(src), `id="fileError"`, file)
		assert.Contains(t, string(src), "fileErrorMessage(", file)
	}
}

// TestThemedTemplatesApplyThemeInHead: each page with a theme applies the saved theme in <head>,
// before the stylesheet. The theme-js script at the end of <body> runs after the browser can
// paint, thus without theme-head a light page flashes in the default dark "dim" theme.
func TestThemedTemplatesApplyThemeInHead(t *testing.T) {
	const (
		themeHead  = `{{template "theme-head" .Nonce}}`
		stylesheet = `<link rel="stylesheet"`
	)

	pkgDir := os.DirFS(".")

	files, err := fs.Glob(pkgDir, "templates/*.html")
	require.NoError(t, err)

	themed := 0

	for _, file := range files {
		src, err := fs.ReadFile(pkgDir, file)
		require.NoError(t, err)

		page := string(src)
		if !strings.Contains(page, `{{template "theme-js"}}`) {
			continue
		}

		themed++
		head, _, found := strings.Cut(page, "</head>")
		require.True(t, found, "%s has no </head>", file)

		headAt := strings.Index(head, themeHead)
		styleAt := strings.Index(head, stylesheet)

		require.GreaterOrEqual(t, headAt, 0, "%s: <head> must include %s", file, themeHead)
		require.GreaterOrEqual(t, styleAt, 0, "%s: <head> must include the stylesheet", file)
		assert.Less(t, headAt, styleAt, "%s: theme-head must come before the stylesheet", file)
	}

	require.NotZero(t, themed, "expected templates that use theme-js")
}
