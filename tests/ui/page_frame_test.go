package ui_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// frameTolerance is the largest permitted difference between two frame measurements, in CSS pixels.
const frameTolerance = 0.5

// noCard is the pageFrame.CardTop value of a page that has no editor card and no article card.
const noCard = -1

// The number of pages that each scenario measures.
const (
	notesFramePages    = 10
	accountsFramePages = 4
	filesFramePages    = 6
)

// pageFrame holds the parts of a page that must stay in place when the user goes to a different
// page: the header, the logo and the left and right edges of the content.
type pageFrame struct {
	Header layoutBox `json:"header"`
	Logo   layoutBox `json:"logo"`
	Main   layoutBox `json:"main"`
	// CardTop is the top edge of the editor card or the article card; noCard on other pages.
	CardTop float64 `json:"card_top"`
	// Gutter is the computed scrollbar-gutter of the root element. The value "stable" keeps the
	// page width the same on pages with and without a vertical scrollbar.
	Gutter string `json:"gutter"`
}

type framedPage struct {
	name  string
	frame pageFrame
}

func TestPageFrame(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "Notes", run: testPageFrameNotes, accounts: false, files: false, allowedStatuses: []int{http.StatusNotFound}},
		{name: "Accounts", run: testPageFrameAccounts, accounts: true, files: false, allowedStatuses: nil},
		// The header of a server with files has the "File" tab, so the file pages are compared
		// with the note editor of the same server.
		{name: "Files", run: testPageFrameFiles, files: true, accounts: false, allowedStatuses: nil},
	})
}

// testPageFrameNotes: the pages of the note and short-link flows have the same frame.
func testPageFrameNotes(sess *uiSession) {
	pages := make([]framedPage, 0, notesFramePages)

	sess.gotoPage("/")
	pages = append(pages, sess.frame("note editor"))

	sess.gotoPage("/links")
	pages = append(pages, sess.frame("link editor"))

	_, noteURL := sess.publishNote()
	pages = append(pages, sess.frame("published note"))

	parsed, err := url.Parse(noteURL)
	sess.must(err)
	sess.gotoPage(parsed.Path)
	pages = append(pages, sess.frame("short note"))

	sess.openEditor()
	sess.must(sess.expect.Locator(sess.page.Locator("#slugInput")).ToHaveAttribute("readonly", ""))
	pages = append(pages, sess.frame("note edit"))

	// These notes are longer than the screen, so the browser can show a vertical scrollbar.
	sess.publishContentsNote(false)
	pages = append(pages, sess.frame("long note"))

	sess.publishContentsNote(true)
	pages = append(pages, sess.frame("note with contents"))

	sess.gotoPage("/links")
	sess.fill("#targetInput", "https://example.com/frame")
	sess.button("Create link")
	linkID, _ := sess.createdLink()
	pages = append(pages, sess.frame("published link"))

	sess.gotoPage("/links/" + linkID + "/edit")
	pages = append(pages, sess.frame("link edit"))

	sess.gotoPage("/missing-ui-note")
	sess.text(".error-code", "404")
	pages = append(pages, sess.frame("not found"))

	sess.expectSameFrame(pages)
}

// testPageFrameAccounts: the account pages have the same frame as the note editor.
func testPageFrameAccounts(sess *uiSession) {
	pages := make([]framedPage, 0, accountsFramePages)

	sess.gotoPage("/setup")
	pages = append(pages, sess.frame("setup"))

	sess.admin()
	pages = append(pages, sess.frame("admin"))

	sess.gotoPage("/")
	pages = append(pages, sess.frame("note editor"))

	sess.gotoPage("/change-password")
	pages = append(pages, sess.frame("change password"))

	sess.expectSameFrame(pages)
}

// testPageFrameFiles: the pages of the file flow have the same frame as the note editor.
func testPageFrameFiles(sess *uiSession) {
	pages := make([]framedPage, 0, filesFramePages)

	sess.gotoPage("/")
	pages = append(pages, sess.frame("note editor"))

	sess.gotoPage("/links")
	pages = append(pages, sess.frame("link editor"))

	sess.gotoPage("/files")
	pages = append(pages, sess.frame("file upload"))

	id, _ := sess.uploadTestFile()
	pages = append(pages, sess.frame("uploaded file"))

	sess.gotoPage("/" + id)
	sess.visible("#downloadBtn")
	pages = append(pages, sess.frame("file"))

	sess.gotoPage("/files/" + id + "/edit")
	sess.visible("#saveBtn")
	pages = append(pages, sess.frame("file edit"))

	sess.expectSameFrame(pages)
}

// frame measures the frame of the open page at the top scroll position, after the fonts load.
func (sess *uiSession) frame(name string) framedPage {
	sess.t.Helper()
	sess.eval("() => document.fonts.ready")

	raw, err := sess.page.Evaluate(`(noCard) => {
		window.scrollTo(0, 0);
		const box = (selector) => {
			const rect = document.querySelector(selector).getBoundingClientRect();
			return {x: rect.x, y: rect.y, width: rect.width, height: rect.height};
		};
		const card = document.querySelector(".editor-card, .article-card");
		return JSON.stringify({
			header: box(".header"),
			logo: box(".logo"),
			main: box("main"),
			card_top: card ? card.getBoundingClientRect().y : noCard,
			gutter: getComputedStyle(document.documentElement).scrollbarGutter,
		});
	}`, noCard)
	sess.must(err)

	text, ok := raw.(string)
	require.True(sess.t, ok, "the frame measurement must be a JSON string")

	var frame pageFrame
	sess.must(json.Unmarshal([]byte(text), &frame))

	return framedPage{name: name, frame: frame}
}

// expectSameFrame compares each page with the first page. Phone width puts the create tabs on a
// second header row, so the header height and the card top are compared only on wide screens.
func (sess *uiSession) expectSameFrame(pages []framedPage) {
	sess.t.Helper()
	require.NotEmpty(sess.t, pages)

	want := pages[0]
	cardTop := float64(noCard)

	for _, page := range pages {
		got := page.frame
		pair := want.name + " and " + page.name

		require.Equal(sess.t, "stable", got.Gutter, "%s: the scrollbar gutter is reserved", page.name)

		sess.expectSameEdges(want.frame.Header, got.Header, pair+": header")
		sess.expectSameEdges(want.frame.Main, got.Main, pair+": main")
		sess.expectSameEdges(want.frame.Logo, got.Logo, pair+": logo")
		require.InDelta(sess.t, want.frame.Logo.Y, got.Logo.Y, frameTolerance, "%s: logo y", pair)
		require.InDelta(sess.t, want.frame.Logo.Height, got.Logo.Height, frameTolerance, "%s: logo height", pair)

		if sess.mobile() {
			continue
		}

		require.InDelta(sess.t, want.frame.Header.Y, got.Header.Y, frameTolerance, "%s: header y", pair)
		require.InDelta(sess.t, want.frame.Header.Height, got.Header.Height, frameTolerance, "%s: header height", pair)

		if got.CardTop == noCard {
			continue
		}

		if cardTop == noCard {
			cardTop = got.CardTop
		}

		require.InDelta(sess.t, cardTop, got.CardTop, frameTolerance, "%s: card top", page.name)
	}
}

// expectSameEdges compares the left edge and the width of two boxes.
func (sess *uiSession) expectSameEdges(want, got layoutBox, name string) {
	sess.t.Helper()
	require.InDelta(sess.t, want.X, got.X, frameTolerance, "%s x", name)
	require.InDelta(sess.t, want.Width, got.Width, frameTolerance, "%s width", name)
}
