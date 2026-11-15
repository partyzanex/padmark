package ui_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

// viewStorageKey is the localStorage key of the editor view (static/editor-preview.js).
const viewStorageKey = "padmark_editor_view"

// previewPlaceholder is the text of an empty preview.
const previewPlaceholder = "Nothing to preview yet."

func TestEditorPreview(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "ViewTabs", run: testViewTabs, accounts: false, allowedStatuses: nil},
		{name: "LiveUpdate", run: testPreviewLiveUpdate, accounts: false, allowedStatuses: nil},
		{name: "ServerParity", run: testPreviewServerParity, accounts: false, allowedStatuses: nil},
		// The <img src=x> request gets the note route's 404; the image error must not run a script.
		{name: "Sanitize", run: testPreviewSanitize, accounts: false, allowedStatuses: []int{http.StatusNotFound}},
		{name: "RememberChoice", run: testPreviewRememberChoice, accounts: false, allowedStatuses: nil},
		{name: "PublishFromPreview", run: testPublishFromPreview, accounts: false, allowedStatuses: nil},
	})
}

// expectView checks the selected tab and which of the textarea and the preview are visible.
func (sess *uiSession) expectView(name string, editorVisible, previewVisible bool) {
	sess.t.Helper()
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleTab, name)).ToHaveAttribute("aria-selected", "true"))
	sess.expectShown("#editor", editorVisible)
	sess.expectShown("#preview", previewVisible)
}

func (sess *uiSession) expectShown(selector string, visible bool) {
	sess.t.Helper()

	locator := sess.page.Locator(selector)
	if visible {
		sess.must(sess.expect.Locator(locator).ToBeVisible())
	} else {
		sess.must(sess.expect.Locator(locator).ToBeHidden())
	}
}

// selectView clicks a view tab.
func (sess *uiSession) selectView(name string) {
	sess.t.Helper()
	sess.must(sess.role(playwright.AriaRoleTab, name).Click())
}

// showPreview opens the view that shows the rendered text on this screen size.
func (sess *uiSession) showPreview() {
	sess.t.Helper()

	if sess.screen.name == "desktop" {
		sess.selectView("Split")
		sess.expectView("Split", true, true)

		return
	}

	sess.selectView("Preview")
	sess.expectView("Preview", false, true)
}

// writeText opens a view with the textarea and replaces the Markdown.
func (sess *uiSession) writeText(text string) {
	sess.t.Helper()

	if sess.screen.name != "desktop" {
		sess.selectView("Write")
	}

	sess.fill("#editor", text)
}

func testViewTabs(sess *uiSession) {
	sess.gotoPage("/")
	formatting := sess.page.Locator("#mdBold")

	// Wide screens open in Split. Phone width has no room for it: Write, without a Split tab.
	if sess.screen.name == "desktop" {
		sess.expectView("Split", true, true)
		sess.must(sess.expect.Locator(sess.page.Locator(`[role="tab"]`)).ToHaveCount(3))
	} else {
		sess.expectView("Write", true, false)
		sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleTab, "Split")).ToBeHidden())
	}

	sess.selectView("Preview")
	sess.expectView("Preview", false, true)
	sess.text("#preview", previewPlaceholder)
	// The formatting buttons change the textarea, which Preview hides.
	sess.must(sess.expect.Locator(formatting).ToBeDisabled())

	// Arrow keys move the selection between the visible tabs and wrap at the ends.
	sess.must(sess.page.Keyboard().Press("ArrowLeft"))

	previous := "Write"
	if sess.screen.name == "desktop" {
		previous = "Split"
	}

	sess.expectView(previous, true, previous == "Split")
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleTab, previous)).ToBeFocused())
	sess.must(sess.expect.Locator(formatting).ToBeEnabled())
	sess.must(sess.page.Keyboard().Press("End"))
	sess.expectView("Preview", false, true)
	sess.must(sess.page.Keyboard().Press("ArrowRight"))
	sess.expectView("Write", true, false)
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleTab, "Write")).ToHaveAttribute("tabindex", "0"))
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleTab, "Preview")).ToHaveAttribute("tabindex", "-1"))

	// The current IDs and the formatting buttons operate in each view with the textarea.
	sess.fill("#editor", "text")
	sess.eval("() => document.querySelector('#editor').select()")
	sess.click("#mdBold")
	sess.value("#editor", "**text**")
	sess.text("#charCount", "8 characters")
	sess.text("#wordCount", "1 word")
	sess.expectLayout(".header", "#viewTabs", "#editor", "#submitBtn")
}

func testPreviewLiveUpdate(sess *uiSession) {
	sess.gotoPage("/")
	sess.showPreview()
	sess.text("#preview", previewPlaceholder)
	sess.writeText("Some **bold** text")
	sess.showPreview()
	sess.text("#preview strong", "bold")

	// A formatting button changes the text without a keystroke; the preview follows it too.
	sess.writeText("plain")
	sess.eval("() => document.querySelector('#editor').select()")
	sess.click("#mdItalic")
	sess.showPreview()
	sess.text("#preview em", "plain")
	sess.must(sess.expect.Locator(sess.page.Locator("#preview strong")).ToHaveCount(0))

	sess.writeText("   ")
	sess.showPreview()
	sess.text("#preview", previewPlaceholder)

	if sess.screen.name == "desktop" {
		// Split shows each keystroke without a change of view.
		sess.must(sess.page.Locator("#editor").PressSequentially("# Live"))
		sess.must(sess.expect.Locator(sess.page.Locator("#preview h1")).ToHaveText("Live"))
		sess.expectLayout("#editor", "#preview")
	}
}

// testPreviewServerParity pins the renderer options shared with internal/infra/render: hard line
// breaks, GFM tables, strikethrough and autolinks, and task lists that stay literal text.
func testPreviewServerParity(sess *uiSession) {
	sess.gotoPage("/")
	sess.writeText("line one\nline two\n\n~~gone~~ https://example.com/page\n\n" +
		"| a | b |\n| - | - |\n| 1 | 2 |\n\n- [ ] todo\n- [x] done\n")
	sess.showPreview()

	preview := sess.page.Locator("#preview")
	sess.must(sess.expect.Locator(preview.Locator("p").First().Locator("br")).ToHaveCount(1))
	sess.text("#preview del", "gone")
	sess.must(sess.expect.Locator(preview.Locator(`a[href="https://example.com/page"]`)).ToHaveCount(1))
	sess.must(sess.expect.Locator(preview.Locator("table td")).ToHaveCount(2))
	sess.must(sess.expect.Locator(preview.Locator("li").First()).ToHaveText("[ ] todo"))
	sess.must(sess.expect.Locator(preview.Locator("li").Nth(1)).ToHaveText("[x] done"))
	sess.must(sess.expect.Locator(preview.Locator("input")).ToHaveCount(0))
	// No syntax highlighting in the preview: that is a known difference from the note page.
}

func testPreviewSanitize(sess *uiSession) {
	sess.gotoPage("/")

	var dialogs safeLog

	sess.page.OnDialog(func(dialog playwright.Dialog) {
		_, _ = dialogs.Write([]byte(dialog.Message() + "\n"))
		sess.must(dialog.Dismiss())
	})

	text := "<img src=x onerror=alert(1)>\n\n[click](javascript:alert(2))\n\n" +
		`<span id="charCount">clobber</span>`
	sess.writeText(text)
	sess.showPreview()

	image := sess.page.Locator("#preview img")
	sess.must(sess.expect.Locator(image).ToHaveCount(1))
	// The image request fails, so an onerror handler would run now if the sanitizer kept it.
	sess.must(image.WaitForFunction("image => image.complete", nil))

	onerror, err := image.GetAttribute("onerror")
	sess.must(err)
	require.Empty(sess.t, onerror, "the sanitizer must remove event handler attributes")

	link := sess.page.Locator("#preview a")
	sess.must(sess.expect.Locator(link).ToHaveText("click"))
	sess.must(sess.expect.Locator(link).Not().ToHaveAttribute("href", regexpURL("javascript:")))

	// A user ID cannot shadow the page elements, so the counter keeps its own element.
	sess.must(sess.expect.Locator(sess.page.Locator("#preview #user-content-charCount")).ToHaveText("clobber"))
	sess.must(sess.expect.Locator(sess.page.Locator("#charCount")).ToHaveText(fmt.Sprintf("%d characters", len(text))))
	require.Empty(sess.t, dialogs.String(), "the preview must not run scripts from the note")
}

func testPreviewRememberChoice(sess *uiSession) {
	sess.gotoPage("/")
	sess.selectView("Preview")
	sess.reload()
	sess.expectView("Preview", false, true)
	sess.selectView("Write")
	sess.reload()
	sess.expectView("Write", true, false)

	// A Split choice from a wide screen shows Write at phone width.
	sess.eval(`() => localStorage.setItem("` + viewStorageKey + `", "split")`)
	sess.reload()

	if sess.screen.name == "desktop" {
		sess.expectView("Split", true, true)
	} else {
		sess.expectView("Write", true, false)
		sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleTab, "Split")).ToBeHidden())
	}
}

// testPublishFromPreview checks that Ctrl+Enter publishes in the view without the textarea.
func testPublishFromPreview(sess *uiSession) {
	sess.gotoPage("/")
	sess.writeText("# From preview")
	sess.selectView("Preview")
	sess.expectView("Preview", false, true)
	sess.must(sess.page.Keyboard().Press("Control+Enter"))
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
}

func (sess *uiSession) reload() {
	sess.t.Helper()
	_, err := sess.page.Reload()
	sess.must(err)
}
