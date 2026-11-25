package ui_test

import (
	"fmt"
	"net/http"
	"strings"
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
		{name: "ViewTabs", run: testViewTabs, accounts: false, files: false, allowedStatuses: nil},
		{name: "LiveUpdate", run: testPreviewLiveUpdate, accounts: false, files: false, allowedStatuses: nil},
		{name: "ServerParity", run: testPreviewServerParity, accounts: false, files: false, allowedStatuses: nil},
		{name: "Math", run: testPreviewMath, accounts: false, files: false, allowedStatuses: nil},
		{name: "MathNotePage", run: testMathNotePage, accounts: false, files: false, allowedStatuses: nil},
		{name: "MathParity", run: testMathParity, accounts: false, files: false, allowedStatuses: nil},
		{name: "MathLoadsOnce", run: testMathLoadsOnce, accounts: false, files: false, allowedStatuses: nil},
		{name: "NoMathNoKaTeX", run: testNoMathNoKaTeX, accounts: false, files: false, allowedStatuses: nil},
		{name: "MathContents", run: testMathContents, accounts: false, files: false, allowedStatuses: nil},
		{name: "Tables", run: testTables, accounts: false, files: false, allowedStatuses: nil},
		// The <img src=x> request gets the note route's 404; the image error must not run a script.
		{
			name: "Sanitize", run: testPreviewSanitize, accounts: false, files: false,
			allowedStatuses: []int{http.StatusNotFound},
		},
		{name: "RememberChoice", run: testPreviewRememberChoice, accounts: false, files: false, allowedStatuses: nil},
		{name: "PublishFromPreview", run: testPublishFromPreview, accounts: false, files: false, allowedStatuses: nil},
		{name: "PlainText", run: testPreviewPlainText, accounts: false, files: false, allowedStatuses: nil},
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

// tableNote has a wide table with long words, a narrow table and a user element that tries to be a
// scroll box. Only the two tables get a box (internal/infra/render/table_scroll.go).
func tableNote() string {
	header := "| Service | Environment | Responsible team | Deployment window | Rollback procedure " +
		"| Monitoring dashboard | Escalation contact | Owner | Region | Notes |\n"
	rule := "|---|---|---|---|---|---|---|---|---|---|\n"
	row := "| authentication | production | platform engineering | weekdays before 16:00 " +
		"| revert the release tag | latency and error rate | on-call engineer | identity | europe-west " +
		"| the longest cell text of this table wraps in its column |\n"

	return "Text before the tables.\n\n" + header + rule + row + row + "\n" +
		"| Key | Value |\n|---|---|\n| a | 1 |\n\n" +
		`<div class="table-scroll">not a box</div>` + "\n\nText after the tables.\n"
}

// tableMetrics measures each scroll box in a root: its overflow, the space between the box and
// its table, the widths of the box and of the text column, the smallest difference between a cell
// width and the minimum cell width, and the number of words that the browser breaks across two
// lines.
const tableMetrics = `(root) => Array.from(document.querySelectorAll(root + " .table-scroll")).map((box) => {
	const table = box.firstElementChild;
	const cells = Array.from(table.querySelectorAll("th, td"));
	const minWidth = parseFloat(getComputedStyle(cells[0]).minWidth);
	const parent = getComputedStyle(box.parentElement);
	const column = box.parentElement.clientWidth - parseFloat(parent.paddingLeft) - parseFloat(parent.paddingRight);
	const brokenWords = (cell) => {
		const walker = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
		let broken = 0;
		for (let node = walker.nextNode(); node; node = walker.nextNode()) {
			for (const word of node.data.matchAll(/\S+/g)) {
				const range = document.createRange();
				range.setStart(node, word.index);
				range.setEnd(node, word.index + word[0].length);
				broken += range.getClientRects().length > 1 ? 1 : 0;
			}
		}
		return broken;
	};
	return {
		tag: table.tagName,
		overflowX: getComputedStyle(box).overflowX,
		overflow: box.scrollWidth - box.clientWidth,
		gap: box.clientWidth - table.getBoundingClientRect().width,
		width: box.getBoundingClientRect().width,
		column: column,
		minWidth: minWidth,
		slack: Math.min(...cells.map((cell) => cell.getBoundingClientRect().width - minWidth)),
		brokenWords: cells.reduce((sum, cell) => sum + brokenWords(cell), 0),
	};
})`

// expectTables checks the tables of tableNote in root: the wide table scrolls in a box as wide as
// the text column, the narrow table takes the width of its content and its box wraps it without a
// scroll, no cell is narrower than the minimum and the page does not scroll horizontally.
func (sess *uiSession) expectTables(root string) {
	sess.t.Helper()

	sess.must(sess.expect.Locator(sess.page.Locator(root + " .table-scroll")).ToHaveCount(2))
	sess.must(sess.expect.Locator(sess.page.Locator(root + " .table-scroll > table")).ToHaveCount(2))
	sess.must(sess.expect.Locator(sess.page.Locator(root+" div").Filter(playwright.LocatorFilterOptions{
		HasText: "not a box", Has: nil, HasNot: nil, HasNotText: nil, Visible: nil,
	})).Not().ToHaveAttribute("class", "table-scroll"))
	sess.expectLayout(root+" .table-scroll >> nth=0", root+" .table-scroll >> nth=1")

	boxes, ok := sess.evalWith(tableMetrics, root).([]any)
	require.True(sess.t, ok, "the table metrics are a list")
	require.Len(sess.t, boxes, 2)

	for idx, value := range boxes {
		box, isMap := value.(map[string]any)
		require.True(sess.t, isMap, "the metrics of box %d are an object", idx)
		require.Equal(sess.t, "TABLE", box["tag"], "box %d holds the table", idx)
		require.Equal(sess.t, "auto", box["overflowX"], "box %d scrolls horizontally", idx)
		require.GreaterOrEqual(sess.t, sess.number(box["minWidth"]), float64(100), "cells have a minimum width")
		require.GreaterOrEqual(sess.t, sess.number(box["slack"]), -0.5, "no cell of box %d is narrower", idx)
		require.Zero(sess.t, sess.number(box["brokenWords"]), "box %d breaks no word across two lines", idx)

		if idx == 0 {
			require.Greater(sess.t, sess.number(box["overflow"]), float64(0), "the wide table scrolls in its box")
			require.InDelta(sess.t, sess.number(box["column"]), sess.number(box["width"]), 1,
				"the box of the wide table is as wide as the text column")

			continue
		}

		require.LessOrEqual(sess.t, sess.number(box["overflow"]), float64(0), "the narrow table does not scroll")
		require.InDelta(sess.t, 0, sess.number(box["gap"]), 1, "the box of the narrow table wraps the table")
		require.Less(sess.t, sess.number(box["width"]), sess.number(box["column"])-1,
			"the narrow table takes the width of its content, not the text column")
	}
}

// testTables checks the scroll boxes of tables in the preview and on the note page.
func testTables(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#titleInput", "Tables")
	sess.writeText(tableNote())
	sess.showPreview()
	sess.expectTables("#preview")

	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.link("Open note")
	sess.expectTables(".prose.markdown-body")
}

// mathNote has the formula forms of LLM output and text that must stay text.
func mathNote() string {
	return "**Mean:**\n\n$$\n\\bar x=\\frac{1}{n}\\sum_{i=1}^{n}x_i\n$$\n\n" +
		"Error $e_i=y_i-\\hat{y}_i$ and \\(\\nabla J(w)\\).\n\n" +
		"\\[\n\\mathrm{MSE}=\\frac{1}{n}\\sum_{i=1}^{n}(y_i-\\hat{y}_i)^2\n\\]\n\n" +
		"Prices $5 and $10 stay text.\n\n$\\frac{1}{$\n"
}

// expectFormulas checks that KaTeX drew the formulas of mathNote in root, that the prices are
// text and that a formula KaTeX refuses keeps its source.
func (sess *uiSession) expectFormulas(root string) {
	sess.t.Helper()

	sess.must(sess.expect.Locator(sess.page.Locator(root + " div.math-display .katex-display")).ToHaveCount(2))
	sess.must(sess.expect.Locator(sess.page.Locator(root + " span.math-inline .katex")).ToHaveCount(2))
	sess.must(sess.expect.Locator(sess.page.Locator(root + " .math-error")).ToHaveText(`\frac{1}{`))
	sess.must(sess.expect.Locator(sess.page.Locator(root + " p").Filter(playwright.LocatorFilterOptions{
		HasText: "Prices", Has: nil, HasNot: nil, HasNotText: nil, Visible: nil,
	})).ToHaveText("Prices $5 and $10 stay text."))
	sess.expectLayout(root + " div.math-display >> nth=0")
}

func testPreviewMath(sess *uiSession) {
	sess.gotoPage("/")
	sess.writeText(mathNote())
	sess.showPreview()
	sess.expectFormulas("#preview")
}

func testMathNotePage(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#titleInput", "Formulas")
	sess.fill("#editor", mathNote())
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.link("Open note")
	sess.expectFormulas(".prose.markdown-body")
}

// mathCorpus mixes formulas with text that only looks like one. mathCorpusFormulas lists what
// the server (internal/infra/render/math.go) finds in it, in page order.
func mathCorpus() string {
	return strings.Join([]string{
		"A $a_1$ b.",
		"$x$$y$",
		"Cost $5 and $10.",
		"a $ b$ c",
		"a $b $ c",
		"$x$5",
		`\$x\$`,
		`\\(x\\)`,
		`$$ $$ and \( \)`,
		"$10 and `$x$`",
		"$a<b$",
		`\(p \$ q\)`,
		"In $$k^2$$ line.",
		"$a\nb$",
		`\[x\] text`,
		"$$\n\\sum\n$$",
		"## Title $t$",
		"| h |\n| - |\n| $c_1$ |",
		"> \\[\n> q\n> \\]",
		"- item\n\n  $$\n  a_2\n  $$",
		"`\\(x\\)` and `$$y$$`",
		"$a`b$",
	}, "\n\n")
}

func mathCorpusFormulas() []any {
	return []any{
		"inline:a_1", "inline:x", "inline:y", "inline:a<b", `inline:p \$ q`, "display:k^2",
		`display:\sum`, "inline:t", "inline:c_1", "display:q", "display:a_2",
	}
}

// formulas waits until math.js drew every formula in root and returns each one as
// "inline:<TeX>" or "display:<TeX>", in page order.
func (sess *uiSession) formulas(root string, count int) []any {
	sess.t.Helper()
	sess.must(sess.expect.Locator(sess.page.Locator(root + " [data-tex]")).ToHaveCount(count))

	list, ok := sess.eval(`() => Array.from(document.querySelectorAll(` +
		`'` + root + ` .math-inline, ` + root + ` .math-display'), (node) =>` +
		` (node.classList.contains('math-display') ? 'display:' : 'inline:') + node.dataset.tex.trim())`).([]any)
	require.True(sess.t, ok, "the formulas must be a list")

	return list
}

// testMathParity checks that the preview finds the same formulas as the server, and leaves the
// same text alone.
func testMathParity(sess *uiSession) {
	want := mathCorpusFormulas()

	sess.gotoPage("/")
	sess.writeText(mathCorpus())
	sess.showPreview()
	require.Equal(sess.t, want, sess.formulas("#preview", len(want)), "preview")

	sess.fill("#titleInput", "Corpus")
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.link("Open note")
	require.Equal(sess.t, want, sess.formulas(".prose.markdown-body", len(want)), "note page")
}

// testMathLoadsOnce checks that the editor loads KaTeX only for the first formula, and only once.
func testMathLoadsOnce(sess *uiSession) {
	sess.gotoPage("/")
	sess.writeText("No formula, only $5 and $10.")
	sess.showPreview()
	sess.text("#preview p", "No formula, only $5 and $10.")
	require.Equal(sess.t, "undefined", sess.eval("() => typeof window.katex"), "no formula, no KaTeX")
	sess.must(sess.expect.Locator(sess.page.Locator(`link[href*="katex.min.css"]`)).ToHaveCount(0))

	for _, text := range []string{"$a$", "$a$ and $b$", "$$c$$"} {
		sess.writeText(text)
		sess.showPreview()
		sess.must(sess.expect.Locator(sess.page.Locator("#preview .katex")).Not().ToHaveCount(0))
	}

	sess.must(sess.expect.Locator(sess.page.Locator(`link[href*="katex.min.css"]`)).ToHaveCount(1))
	sess.must(sess.expect.Locator(sess.page.Locator(`script[src*="katex.min.js"]`)).ToHaveCount(1))
}

// testMathContents checks that the "On this page" list and the current-section label draw the
// formulas of the headings instead of showing their TeX source.
func testMathContents(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#titleInput", "Statistics")
	sess.fill("#editor", "Intro.\n\n## Mean $\\bar x$\n\nText.\n\n## Error \\(e_i\\) and *$y_i$*\n\nText.\n\n"+
		"## Plain heading\n\nText.\n")
	sess.openSettings()
	sess.click("#contentsLabel")
	sess.closeSettings()
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.link("Open note")

	labels := sess.page.Locator("#noteTocList .note-toc-label")
	sess.must(sess.expect.Locator(labels).ToHaveCount(3))
	sess.must(sess.expect.Locator(labels.Nth(0).Locator(".katex")).ToHaveCount(1))
	sess.must(sess.expect.Locator(labels.Nth(1).Locator(".katex")).ToHaveCount(2))
	sess.must(sess.expect.Locator(labels.Nth(2)).ToHaveText("Plain heading"))
	// One label element in each link: as loose flex items, the text pieces would lose their spaces.
	sess.must(sess.expect.Locator(sess.page.Locator("#noteTocList .note-toc-link > .note-toc-label")).ToHaveCount(3))
	// The title of a link gives the TeX source of a formula once, not the text that KaTeX draws.
	sess.must(sess.expect.Locator(sess.page.Locator("#noteTocList .note-toc-link").First()).
		ToHaveAttribute("title", `Mean \bar x`))
	sess.must(sess.expect.Locator(sess.page.Locator("#noteToc .math-inline:not(:has(.katex))")).ToHaveCount(0))
	sess.must(sess.expect.Locator(sess.page.Locator("#noteToc .math-error")).ToHaveCount(0))
	// The heading keeps its own formula: the list gets copies.
	sess.must(sess.expect.Locator(sess.page.Locator(".prose.markdown-body h2 .katex")).ToHaveCount(3))
	sess.must(sess.expect.Locator(sess.page.Locator("#noteTocCurrent .katex")).ToHaveCount(1))
}

// testNoMathNoKaTeX checks that a note page without a formula does not load KaTeX.
func testNoMathNoKaTeX(sess *uiSession) {
	_, link := sess.publishNote()
	sess.gotoPage(link)
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Hello")).ToBeVisible())
	sess.must(sess.expect.Locator(sess.page.Locator(`[href*="katex"], [src*="katex"], [src*="math.js"]`)).ToHaveCount(0))
	require.Equal(sess.t, "undefined", sess.eval("() => typeof window.katex"))
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

// testPreviewPlainText: the preview of a text/plain note is the same text as its page: escaped
// text in <pre>, without Markdown, HTML or formulas. The HTML parser removes one newline after
// <pre> on the page, so the preview removes it too.
func testPreviewPlainText(sess *uiSession) {
	const content = "\n# Plain heading\n<b>bold?</b> $x^2$\n    indented\n"

	created, err := sess.browserContext.Request().Post("/notes", playwright.APIRequestContextPostOptions{
		Headers: map[string]string{"Accept": "application/json"},
		Data:    map[string]any{"slug": "ui-plain", "content": content, "content_type": "text/plain"},

		FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil,
		MaxRetries: nil, Multipart: nil, Params: nil, Timeout: nil,
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusCreated, created.Status())

	sess.gotoPage("/edit/ui-plain")
	sess.showPreview()

	preview := sess.page.Locator("#preview")
	sess.must(sess.expect.Locator(preview.Locator("pre")).ToHaveCount(1))
	sess.must(sess.expect.Locator(preview.Locator("h1, b, .katex")).ToHaveCount(0))
	shown := sess.eval("() => document.querySelector('#preview pre').textContent")
	require.Equal(sess.t, content[1:], shown)

	// The preview follows the text as the author types.
	sess.writeText("# Changed")
	sess.showPreview()
	sess.text("#preview pre", "# Changed")
	sess.must(sess.expect.Locator(preview.Locator("h1")).ToHaveCount(0))

	sess.gotoPage("/ui-plain")
	sess.must(sess.expect.Locator(sess.page.Locator(".markdown-body pre")).ToHaveCount(1))
	require.Equal(sess.t, shown, sess.eval("() => document.querySelector('.markdown-body pre').textContent"))
}
