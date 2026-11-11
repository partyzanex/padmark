package ui_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// formHeadParts are the parts above the editor body that must not move when the user switches
// between the note and short-link forms.
var formHeadParts = []string{ //nolint:gochecknoglobals // A fixed selector list shared by the scenarios.
	".header", ".editor-head", ".head-row > svg", ".head-input", ".slug-field", ".slug-prefix",
	"#slugInput", ".editor-toolbar",
}

type layoutBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type formHead struct {
	Boxes map[string]layoutBox `json:"boxes"`
	Font  string               `json:"font"`
}

func TestFormHead(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "Create", run: testFormHeadCreate, accounts: false, allowedStatuses: nil},
		{name: "Edit", run: testFormHeadEdit, accounts: false, allowedStatuses: nil},
	})
}

func testFormHeadCreate(sess *uiSession) {
	sess.gotoPage("/")
	note := sess.formHead()
	sess.link("Short link")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/links"))
	sess.expectSameHead(note, sess.formHead())
}

func testFormHeadEdit(sess *uiSession) {
	_, noteURL := sess.publishNote()
	parsed, err := url.Parse(noteURL)
	sess.must(err)

	sess.gotoPage("/links")
	sess.fill("#targetInput", "https://example.com/head")
	sess.button("Create link")
	linkID, _ := sess.createdLink()

	sess.gotoPage("/edit/" + strings.TrimPrefix(parsed.Path, "/"))
	sess.must(sess.expect.Locator(sess.page.Locator("#slugInput")).ToHaveAttribute("readonly", ""))
	note := sess.formHead()
	sess.gotoPage("/links/" + linkID + "/edit")
	sess.expectSameHead(note, sess.formHead())
}

// formHead measures the head parts and the head input font after the fonts load.
func (sess *uiSession) formHead() formHead {
	sess.t.Helper()
	sess.eval("() => document.fonts.ready")

	raw, err := sess.page.Evaluate(`(selectors) => {
		const boxes = {};
		for (const selector of selectors) {
			const rect = document.querySelector(selector).getBoundingClientRect();
			boxes[selector] = {x: rect.x, y: rect.y, width: rect.width, height: rect.height};
		}
		const style = getComputedStyle(document.querySelector(".head-input"));
		const font = [style.fontFamily, style.fontSize, style.fontWeight, style.lineHeight, style.letterSpacing];
		return JSON.stringify({boxes, font: font.join(" ")});
	}`, formHeadParts)
	sess.must(err)

	text, ok := raw.(string)
	require.True(sess.t, ok, "the head measurement must be a JSON string")

	var head formHead
	sess.must(json.Unmarshal([]byte(text), &head))

	return head
}

func (sess *uiSession) expectSameHead(note, link formHead) {
	sess.t.Helper()
	require.Equal(sess.t, note.Font, link.Font, "the head inputs use the same font")

	for _, selector := range formHeadParts {
		want, got := note.Boxes[selector], link.Boxes[selector]
		require.InDelta(sess.t, want.X, got.X, 0.5, "%s x", selector)
		require.InDelta(sess.t, want.Y, got.Y, 0.5, "%s y", selector)
		require.InDelta(sess.t, want.Width, got.Width, 0.5, "%s width", selector)
		require.InDelta(sess.t, want.Height, got.Height, 0.5, "%s height", selector)
	}
}
