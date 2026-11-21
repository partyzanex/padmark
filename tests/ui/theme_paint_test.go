package ui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

// bodyThemeScript records the data-theme of the root element when the parser makes <body>.
// The parser does a microtask checkpoint before each parser-inserted script, thus the observer
// gets the value before the first script of the body runs. The browser can paint the page at
// that point, so this value is the theme of the first paint.
const bodyThemeScript = `(() => {
	const observer = new MutationObserver(() => {
		if (!document.body) return;
		window.__themeAtBody = document.documentElement.getAttribute('data-theme');
		observer.disconnect();
	});
	observer.observe(document, {childList: true, subtree: true});
})();`

// paintTheme is the saved theme that the scenarios examine. It is not the default theme "dim",
// thus a page that paints with the server default fails.
const paintTheme = "light"

func TestThemeFirstPaint(t *testing.T) {
	runScenarios(t, []scenario{
		{
			name: "Notes", run: testThemePaintNotes, accounts: false, files: false,
			allowedStatuses: []int{http.StatusNotFound},
		},
		{name: "Accounts", run: testThemePaintAccounts, accounts: true, files: false, allowedStatuses: nil},
		{name: "Files", run: testThemePaintFiles, files: true, accounts: false, allowedStatuses: nil},
	})
}

// testThemePaintNotes: the "New" and "Edit" actions and the other note and short-link pages
// paint with the saved theme.
func testThemePaintNotes(sess *uiSession) {
	sess.saveTheme()

	_, noteURL := sess.publishNote()
	sess.expectPaintTheme("success")

	parsed, err := url.Parse(noteURL)
	sess.must(err)
	sess.gotoPage(parsed.Path)
	sess.expectPaintTheme("note")

	sess.click(".header-new")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/"))
	sess.expectPaintTheme("new note")

	sess.gotoPage(parsed.Path)
	sess.openEditor()
	sess.must(sess.expect.Locator(sess.page.Locator("#slugInput")).ToHaveAttribute("readonly", ""))
	sess.expectPaintTheme("note edit")

	sess.gotoPage("/links")
	sess.expectPaintTheme("link editor")
	sess.fill("#targetInput", "https://example.com/paint")
	sess.button("Create link")
	linkID, _ := sess.createdLink()
	sess.expectPaintTheme("published link")

	sess.link("Open link")
	sess.visible("#linkTitle")
	sess.expectPaintTheme("link")

	sess.click(".header-new")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/links"))
	sess.expectPaintTheme("new link")

	sess.gotoPage("/links/" + linkID)
	sess.openEditor()
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/edit/" + linkID))
	sess.expectPaintTheme("link edit")

	sess.gotoPage("/missing-ui-note")
	sess.text(".error-code", "404")
	sess.expectPaintTheme("not found")
}

// testThemePaintAccounts: the account pages paint with the saved theme.
func testThemePaintAccounts(sess *uiSession) {
	sess.gotoPage("/setup")
	sess.saveTheme()
	sess.gotoPage("/setup")
	sess.expectPaintTheme("setup")

	sess.admin()
	sess.expectPaintTheme("admin")

	sess.gotoPage("/change-password")
	sess.expectPaintTheme("change password")
}

// testThemePaintFiles: the file pages paint with the saved theme.
func testThemePaintFiles(sess *uiSession) {
	sess.saveTheme()

	sess.gotoPage("/files")
	sess.expectPaintTheme("file upload")

	id, _ := sess.uploadTestFile()
	sess.expectPaintTheme("uploaded file")

	sess.gotoPage("/" + id)
	sess.visible("#downloadBtn")
	sess.expectPaintTheme("file")

	sess.openEditor()
	sess.visible("#saveBtn")
	sess.expectPaintTheme("file edit")
}

// saveTheme selects paintTheme on the open page, or on the note editor when no page is open,
// and installs bodyThemeScript for the pages that the scenario opens after it.
func (sess *uiSession) saveTheme() {
	sess.t.Helper()

	if !strings.HasPrefix(sess.page.URL(), sess.baseURL) {
		sess.gotoPage("/")
	}

	sess.chooseTheme(paintTheme)
	sess.must(sess.expect.Locator(sess.page.Locator("html")).ToHaveAttribute("data-theme", paintTheme))

	script := bodyThemeScript
	sess.must(sess.page.AddInitScript(playwright.Script{Content: &script, Path: nil}))
}

// expectPaintTheme makes sure that the open page had the saved theme before its body scripts ran.
func (sess *uiSession) expectPaintTheme(page string) {
	sess.t.Helper()
	sess.must(sess.page.WaitForLoadState())

	got := sess.eval("() => window.__themeAtBody ?? null")
	require.Equal(sess.t, paintTheme, got, "%s: the theme of the first paint", page)
}
