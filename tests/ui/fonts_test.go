package ui_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

// cyrillicTitle is the title from a user report: Cyrillic and Latin words in one line.
const cyrillicTitle = "Повторный аудит Padmark — 12 ошибок в редакторе, API, CLI и файловом хранилище"

// cyrillicNote has Cyrillic and Latin text in each font role of a note: a heading, regular,
// bold and italic prose, inline code and a code block. The paragraph after the heading has only
// regular text, because the browser reports the fonts of an element together with its children.
const cyrillicNote = "## Раздел API и CLI\n\n" +
	"Обычный текст Padmark и API.\n\n" +
	"**Жирный текст CLI** и *курсив Markdown*.\n\n" +
	"Код `строка code` в тексте.\n\n" +
	"```\nпример: go test ./...\n```\n"

// fontWait is the maximum time for the browser to download and apply the web fonts.
const fontWait = 10 * time.Second

// fontRole is a group of elements with one font: the CSS selector, the font family that must
// draw all their glyphs and the style of the face.
type fontRole struct {
	selector, family string
	italic           bool
}

// usedFont is a font that Chromium used to draw text, from CSS.getPlatformFontsForNode.
type usedFont struct {
	FamilyName, PostScriptName string
	GlyphCount                 int
}

// cdpNode is a DOM node from the Chrome DevTools Protocol (CDP).
type cdpNode struct {
	NodeID, NodeType      int
	Children, ShadowRoots []cdpNode
}

func TestCyrillicFonts(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "EditorAndNote", run: testCyrillicNote, accounts: false, files: false, allowedStatuses: nil},
		{name: "Interface", run: testCyrillicInterface, accounts: true, files: false, allowedStatuses: nil},
	})
}

// testCyrillicNote checks that the project fonts draw the Cyrillic and Latin glyphs of each font
// role of the editor and the note page: a system fallback font for the Cyrillic glyphs makes
// the words of one line look different. A page without Cyrillic text must not download the
// Cyrillic files.
func testCyrillicNote(sess *uiSession) {
	cdp, err := sess.browserContext.NewCDPSession(sess.page)
	sess.must(err)

	sess.gotoPage("/")
	sess.eval("async () => { await document.fonts.ready; }")
	require.Empty(sess.t, sess.cyrillicFontFiles(), "a page without Cyrillic text")

	sess.fill("#titleInput", cyrillicTitle)
	sess.writeText(cyrillicNote)
	sess.expectFonts(cdp,
		fontRole{selector: "#titleInput", family: "Source Serif 4", italic: false},
		fontRole{selector: "#editor", family: "JetBrains Mono", italic: false},
	)

	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.link("Open note")
	sess.text(".article-title", cyrillicTitle)
	sess.expectFonts(cdp,
		fontRole{selector: ".article-title", family: "Source Serif 4", italic: false},
		fontRole{selector: ".prose.markdown-body h2", family: "Source Serif 4", italic: false},
		fontRole{selector: ".prose.markdown-body h2 + p", family: "Source Serif 4", italic: false},
		fontRole{selector: ".prose.markdown-body strong", family: "Source Serif 4", italic: false},
		fontRole{selector: ".prose.markdown-body em", family: "Source Serif 4", italic: true},
		fontRole{selector: ".prose.markdown-body p code", family: "JetBrains Mono", italic: false},
		fontRole{selector: ".prose.markdown-body pre code", family: "JetBrains Mono", italic: false},
	)
	require.Subset(sess.t, sess.cyrillicFontFiles(), []string{
		"source-serif-4-cyrillic.woff2", "source-serif-4-italic-cyrillic.woff2", "jetbrains-mono-cyrillic.woff2",
	})
}

// testCyrillicInterface checks the interface font with a Cyrillic user name in the setup form.
func testCyrillicInterface(sess *uiSession) {
	cdp, err := sess.browserContext.NewCDPSession(sess.page)
	sess.must(err)

	sess.gotoPage("/setup")
	sess.eval("async () => { await document.fonts.ready; }")
	require.Empty(sess.t, sess.cyrillicFontFiles(), "a page without Cyrillic text")

	sess.fill("#username", "Администратор Padmark")
	sess.expectFonts(cdp, fontRole{selector: "#username", family: "Onest", italic: false})
	require.Equal(sess.t, []string{"onest-cyrillic.woff2"}, sess.cyrillicFontFiles())
}

// expectFonts waits until the browser draws the text of each role only with the family of the
// role. The fonts load in the background, thus the check repeats until the time limit.
func (sess *uiSession) expectFonts(cdp playwright.CDPSession, roles ...fontRole) {
	sess.t.Helper()

	deadline := time.Now().Add(fontWait)

	for _, role := range roles {
		for {
			problem := sess.fontProblem(cdp, role)
			if problem == "" {
				break
			}

			if time.Now().After(deadline) {
				require.Fail(sess.t, "a font other than "+role.family, "%s: %s", role.selector, problem)
			}

			sess.eval("async () => { await document.fonts.ready; await new Promise(requestAnimationFrame); }")
		}
	}
}

// fontProblem returns an empty string when the role family draws all glyphs of the role
// elements in the role style, or else a description of the fonts that the browser used.
func (sess *uiSession) fontProblem(cdp playwright.CDPSession, role fontRole) string {
	fonts := sess.platformFonts(cdp, role.selector)
	if len(fonts) == 0 {
		return "no text drawn"
	}

	for _, font := range fonts {
		italic := strings.Contains(font.PostScriptName, "Italic")
		if font.FamilyName != role.family || italic != role.italic {
			return fmt.Sprintf("%+v", fonts)
		}
	}

	return ""
}

// platformFonts returns the fonts that Chromium used for the text of the elements that match the
// selector and of their children. An input or a textarea keeps its text in a user-agent shadow
// root, thus the function also examines the elements of shadow roots. Each font occurs once.
func (sess *uiSession) platformFonts(cdp playwright.CDPSession, selector string) []usedFont {
	sess.t.Helper()
	cdpCall[struct{}](sess, cdp, "DOM.enable", nil)
	cdpCall[struct{}](sess, cdp, "CSS.enable", nil)

	document := cdpCall[struct{ Root cdpNode }](sess, cdp, "DOM.getDocument",
		map[string]any{"depth": -1, "pierce": true})
	found := cdpCall[struct{ NodeIDs []int }](sess, cdp, "DOM.querySelectorAll",
		map[string]any{"nodeId": document.Root.NodeID, "selector": selector})
	require.NotEmpty(sess.t, found.NodeIDs, selector)

	var fonts []usedFont

	seen := make(map[usedFont]bool)

	for _, nodeID := range found.NodeIDs {
		described := cdpCall[struct{ Node cdpNode }](sess, cdp, "DOM.describeNode",
			map[string]any{"nodeId": nodeID, "depth": -1, "pierce": true})
		ids := append([]int{nodeID}, shadowElements(described.Node.ShadowRoots)...)

		for _, id := range ids {
			result := cdpCall[struct{ Fonts []usedFont }](sess, cdp, "CSS.getPlatformFontsForNode",
				map[string]any{"nodeId": id})
			for _, font := range result.Fonts {
				if !seen[font] {
					seen[font] = true
					fonts = append(fonts, font)
				}
			}
		}
	}

	return fonts
}

// cyrillicFontFiles returns the names of the font files with a Cyrillic subset that the page
// downloaded.
func (sess *uiSession) cyrillicFontFiles() []string {
	sess.t.Helper()

	value, err := sess.page.Evaluate(`() => performance.getEntriesByType('resource')
		.map((entry) => new URL(entry.name).pathname)
		.filter((path) => path.startsWith('/static/fonts/') && path.endsWith('-cyrillic.woff2'))
		.map((path) => path.slice('/static/fonts/'.length))`)
	sess.must(err)

	list, ok := value.([]any)
	require.True(sess.t, ok, "the font files must be a list")

	names := make([]string, 0, len(list))
	for _, item := range list {
		names = append(names, fmt.Sprint(item))
	}

	return names
}

// --- helpers ---

// shadowElements returns the IDs of all element nodes in the shadow roots and their subtrees.
func shadowElements(roots []cdpNode) []int {
	const elementNode = 1

	var ids []int

	pending := roots
	for len(pending) > 0 {
		node := pending[0]
		pending = append(pending[1:], node.Children...)

		if node.NodeType == elementNode {
			ids = append(ids, node.NodeID)
		}
	}

	return ids
}

// cdpCall sends a CDP command and decodes its result. The CDP field names, such as nodeId,
// match the Go field names because encoding/json ignores the case.
func cdpCall[T any](sess *uiSession, cdp playwright.CDPSession, method string, params map[string]any) T {
	sess.t.Helper()

	raw, err := cdp.Send(method, params)
	sess.must(err)

	data, err := json.Marshal(raw)
	sess.must(err)

	var result T
	sess.must(json.Unmarshal(data, &result))

	return result
}
