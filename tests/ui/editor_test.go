package ui_test

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

func TestEditor(t *testing.T) {
	cases := []scenario{}
	for _, theme := range []string{"light", "dim", "dark"} {
		cases = append(cases, scenario{name: "Theme_" + theme, run: func(sess *uiSession) {
			sess.gotoPage("/")
			sess.fill("#titleInput", "Release notes")
			sess.fill("#slugInput", "release-notes")
			sess.fill("#editor", "# Release notes\n\nA small **Markdown** example.")

			sess.chooseTheme(theme)
			sess.must(sess.expect.Locator(sess.page.Locator("html")).ToHaveAttribute("data-theme", theme))
			sess.expectLayout(sess.editorParts()...)
			sess.screenshot("editor-" + theme + ".png")
			_, err := sess.page.Reload()
			sess.must(err)
			sess.must(sess.expect.Locator(sess.page.Locator("html")).ToHaveAttribute("data-theme", theme))
			activeButton := sess.page.Locator(fmt.Sprintf(`.theme-btn[data-theme-value="%s"]`, theme))
			sess.must(sess.expect.Locator(activeButton).ToHaveClass(regexp.MustCompile("active")))
		},
			accounts: false, files: false, allowedStatuses: nil,
		})
	}

	cases = append(cases,
		scenario{name: "Toolbar", run: testToolbar, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "FillsHeight", run: testEditorFillsHeight, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "Options", run: testOptions, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "PhoneMenu", run: testPhoneMenu, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "ChosenEditCode", run: testChosenEditCode, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "PrivacyNeedsAccounts", allowedStatuses: []int{http.StatusUnprocessableEntity},
			run:      testPrivacyNeedsAccounts,
			accounts: false, files: false,
		},
		scenario{name: "EmptyContent", run: testEmptyContent, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "PublishAndView", run: testPublishAndView, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "ContentsSidebar", run: testContentsSidebar, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "ContentsSections", run: testContentsSections, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "ContentsScroll", run: testContentsScroll, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "NoContentsSidebar", run: testNoContentsSidebar, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "EditAndSave", run: testEditAndSave, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "DeleteNote", run: testDeleteNote, accounts: false, files: false, allowedStatuses: nil},
		scenario{
			name: "PreserveCustomBurnTTL", run: testPreserveCustomBurnTTL, accounts: false, files: false,
			allowedStatuses: nil,
		},
		scenario{name: "OneTimeEditCode", run: testOneTimeEditCode, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "Reveal", run: testReveal, accounts: false, files: false, allowedStatuses: nil},
		scenario{
			name: "PublishFailure", allowedStatuses: []int{503}, run: testPublishFailure, accounts: false, files: false,
		},
		scenario{name: "LongFirstLine", run: testLongFirstLine, accounts: false, files: false, allowedStatuses: nil},
		scenario{
			name: "InvalidEditCode", allowedStatuses: []int{403}, run: testInvalidEditCode, accounts: false, files: false,
		},
		scenario{name: "NotFound", allowedStatuses: []int{404}, run: testNotFound, accounts: false, files: false},
		scenario{name: "KeepsWhitespace", run: testKeepsWhitespace, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "BlockedStorage", run: testBlockedStorage, accounts: false, files: false, allowedStatuses: nil},
		scenario{name: "BurnDelay", run: testBurnDelay, accounts: false, files: false, allowedStatuses: nil},
	)
	runScenarios(t, cases)
}

// editorParts lists the editor controls that a screen size shows without a further action.
// Phone width keeps the header links in the menu and the publish settings in the sheet.
func (sess *uiSession) editorParts() []string {
	if sess.mobile() {
		return []string{
			".header", ".logo", "#themeCycle", "#menuBtn", "#editor", "#titleInput", "#slugInput",
			"#settingsToggle", "#submitBtn",
		}
	}

	return []string{
		".header", ".logo", "#themeSwitcher", ".header-links", "#editor", "#titleInput", "#slugInput",
		"#burnLabel", "#privacySelect", "#submitBtn",
	}
}

func testToolbar(sess *uiSession) {
	sess.gotoPage("/")

	for _, format := range []struct{ button, expected string }{
		{"#mdBold", "**text**"}, {"#mdItalic", "_text_"},
		{"#mdCode", "`text`"}, {"#mdBlock", "\n```\ntext\n```\n"},
	} {
		sess.fill("#editor", "text")
		sess.eval("() => document.querySelector('#editor').select()")
		sess.click(format.button)
		sess.value("#editor", format.expected)
		sess.text("#charCount", fmt.Sprintf("%d characters", len(format.expected)))
		sess.must(sess.expect.Locator(sess.page.Locator("#editor")).ToBeFocused())
	}
}

func testOptions(sess *uiSession) {
	sess.gotoPage("/")
	sess.openSettings()

	if sess.mobile() {
		sess.expectLayout("#settingsSheet", "#privacySelect", "#burnLabel", "#contentsLabel", "#editCodeInput", "#submitBtn")
		sess.screenshot("settings-sheet.png")
	}

	sess.must(sess.expect.Locator(sess.page.Locator("#burnTime")).ToBeDisabled())
	sess.click("#burnLabel")
	sess.must(sess.expect.Locator(sess.page.Locator("#burnCheck")).ToBeChecked())
	sess.must(sess.expect.Locator(sess.page.Locator("#burnTime")).ToBeEnabled())
	sess.selectOption("#burnTime", "3600")
	sess.value("#burnTime", "3600")
	sess.click("#burnLabel")
	sess.must(sess.expect.Locator(sess.page.Locator("#burnTime")).ToBeDisabled())

	// Without the account system nothing enforces a protected level, so only public is offered.
	sess.expectPrivacyOptions("public")
	sess.must(sess.expect.Locator(sess.page.Locator("#privacy-public")).ToBeChecked())
}

// testPhoneMenu checks the header menu of phone width: it holds the links that wide screens show
// in the header, and Escape closes it. Reader pages have the same menu; their "+" button goes
// before the theme and menu buttons, so these two stay at the right edge.
func testPhoneMenu(sess *uiSession) {
	sess.gotoPage("/")
	sess.expectPhoneMenu()

	_, noteURL := sess.publishNote()
	parsed, err := url.Parse(noteURL)
	sess.must(err)
	sess.gotoPage(parsed.Path)
	sess.visible(".header-new")
	sess.expectPhoneMenu()

	if !sess.mobile() {
		return
	}

	newBox, err := sess.page.Locator(".header-new").BoundingBox()
	sess.must(err)
	themeBox, err := sess.page.Locator("#themeCycle").BoundingBox()
	sess.must(err)
	menuBox, err := sess.page.Locator("#menuBtn").BoundingBox()
	sess.must(err)
	require.Less(sess.t, newBox.X, themeBox.X, "the \"+\" button is left of the theme button")
	require.Less(sess.t, themeBox.X, menuBox.X, "the theme button is left of the menu button")
}

// expectPhoneMenu checks the header menu of the open page.
func (sess *uiSession) expectPhoneMenu() {
	sess.t.Helper()

	api := sess.page.Locator(".header-links").GetByRole(*playwright.AriaRoleLink, playwright.LocatorGetByRoleOptions{
		Name: "API", Exact: new(true),

		Checked: nil, Description: nil, Disabled: nil, Expanded: nil, IncludeHidden: nil,
		Level: nil, Pressed: nil, Selected: nil,
	})

	if !sess.mobile() {
		sess.must(sess.expect.Locator(api).ToBeVisible())
		sess.must(sess.expect.Locator(sess.page.Locator("#menuBtn")).ToBeHidden())

		return
	}

	sess.must(sess.expect.Locator(api).ToBeHidden())
	sess.click("#menuBtn")
	sess.must(sess.expect.Locator(sess.page.Locator("#menuBtn")).ToHaveAttribute("aria-expanded", "true"))
	sess.must(sess.expect.Locator(api).ToBeVisible())
	sess.expectLayout("#headerMenu")
	sess.must(sess.page.Keyboard().Press("Escape"))
	sess.must(sess.expect.Locator(api).ToBeHidden())
	sess.must(sess.expect.Locator(sess.page.Locator("#menuBtn")).ToHaveAttribute("aria-expanded", "false"))
}

// testChosenEditCode checks the edit code field of a new note: a short code is refused before a
// request, and a generated code is the code that the success page shows.
func testChosenEditCode(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#editor", "A note with a chosen edit code")
	sess.openSettings()
	sess.fill("#editCodeInput", "short")
	sess.button("Publish note")
	sess.text("#noteError", "Edit code: 8 characters or more")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/"))

	sess.click("#editCodeGenerate")
	sess.must(sess.expect.Locator(sess.page.Locator("#editCodeInput")).ToHaveValue(
		regexp.MustCompile(`^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`)))
	generated, err := sess.page.Locator("#editCodeInput").InputValue()
	sess.must(err)

	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	require.Equal(sess.t, generated, sess.shownEditCode())
}

// testPrivacyNeedsAccounts pins review R2 in the default configuration: the API refuses a
// protected level it cannot enforce, and keeps accepting the deprecated v0.1.6 `private` flag,
// which, as in v0.1.6 without auth, an independent client can still read.
func testPrivacyNeedsAccounts(sess *uiSession) {
	request := sess.browserContext.Request()
	headers := map[string]string{"Accept": "application/json"}

	refused, err := request.Post("/notes", playwright.APIRequestContextPostOptions{
		Headers: headers, Data: map[string]any{"content": "protected?", "privacy": "authenticated"},

		FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil,
		MaxRetries: nil, Multipart: nil, Params: nil, Timeout: nil,
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusUnprocessableEntity, refused.Status())

	created, err := request.Post("/notes", playwright.APIRequestContextPostOptions{
		Headers: headers, Data: map[string]any{"content": "v0.1.6 private", "private": true},

		FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil,
		MaxRetries: nil, Multipart: nil, Params: nil, Timeout: nil,
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusCreated, created.Status())

	var note struct {
		ID string `json:"id"`
	}

	sess.must(created.JSON(&note))

	read, err := request.Get("/notes/"+note.ID, playwright.APIRequestContextGetOptions{
		Headers: headers,

		Data: nil, FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil,
		MaxRetries: nil, Multipart: nil, Params: nil, Timeout: nil,
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusOK, read.Status())
}

func testEmptyContent(sess *uiSession) {
	sess.gotoPage("/")

	var submissions safeLog

	sess.page.OnRequest(func(request playwright.Request) {
		if request.Method() == "POST" {
			_, _ = fmt.Fprintln(&submissions, request.URL())
		}
	})
	sess.fill("#titleInput", "Empty note")
	sess.fill("#editor", "   ")
	sess.button("Publish note")
	sess.text("#noteError", "Enter some text first")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	require.Empty(sess.t, submissions.String())
}

func testPublishAndView(sess *uiSession) {
	code, noteURL := sess.publishNote()
	sess.expectLayout(".header", ".success-card", "#editCodeBlock", ".action-row")
	sess.screenshot("created.png", ".copy-value", "#editCodeValue")
	sess.must(sess.browserContext.GrantPermissions([]string{"clipboard-read", "clipboard-write"}))
	sess.click("#editCodeCopyBtn")
	sess.must(sess.expect.Locator(sess.page.Locator("#editCodeCopyBtn")).ToHaveClass(regexp.MustCompile("copied")))
	require.Equal(sess.t, code, sess.eval("() => navigator.clipboard.readText()"))
	sess.click("#copyLinkBtn")
	sess.text("#copyLinkBtn", "Copied")
	base, err := url.Parse(sess.baseURL)
	sess.must(err)
	relative, err := url.Parse(noteURL)
	sess.must(err)
	require.Equal(sess.t, base.ResolveReference(relative).String(), sess.eval("() => navigator.clipboard.readText()"))
	sess.link("Open note")
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Hello")).ToBeVisible())
	sess.text(".markdown-body strong", "Markdown")
	sess.expectLayout(".header", ".article-meta", ".article-card", ".site-footer")
	sess.screenshot("note.png")
	raw, err := sess.page.ExpectPopup(func() error { return sess.role(playwright.AriaRoleLink, "Raw").First().Click() })
	sess.must(err)
	sess.must(sess.expect.Locator(raw.Locator("body")).ToContainText("# Hello"))
	sess.must(sess.expect.Locator(raw.Locator("body")).ToContainText("**Markdown**"))
	sess.must(raw.Close())
}

func testEditAndSave(sess *uiSession) {
	code, _ := sess.publishNote()
	sess.link("Open note")
	sess.openEditor()
	sess.value("#titleInput", "UI test note")
	sess.value("#editor", "# Hello\n\nThis is **Markdown**.")
	sess.expectLayout("#editor", "#titleInput", "#submitBtn")
	sess.screenshot("edit.png", "#slugFixed")
	sess.button("Save")
	sess.text("#noteError", "Edit code required")
	sess.openSettings()
	sess.expectLayout("#editCodeInput")
	sess.fill("#editCodeInput", code)
	sess.closeSettings()
	sess.fill("#titleInput", "Updated title")
	sess.fill("#editor", "# Updated\n\nSaved in the browser.")
	sess.must(sess.page.Locator("#editor").Press("Control+Enter"))
	sess.must(sess.expect.Page(sess.page).Not().ToHaveURL(regexpURL("/edit/")))
	sess.must(sess.expect.Page(sess.page).ToHaveTitle("Updated title — Padmark"))
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Updated")).ToBeVisible())
}

// testDeleteNote: "Delete note" asks for a confirmation, deletes the note with the edit code and
// opens a new note editor, as the link and file editors do.
func testDeleteNote(sess *uiSession) {
	code, noteURL := sess.publishNote()
	parsed, err := url.Parse(noteURL)
	sess.must(err)

	sess.link("Open note")
	sess.openEditor()
	sess.openSettings()
	sess.fill("#editCodeInput", code)
	sess.closeSettings()
	sess.page.OnDialog(func(dialog playwright.Dialog) { sess.must(dialog.Accept()) })
	sess.button("Delete note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/"))

	deleted, err := sess.browserContext.Request().Get("/notes" + parsed.Path)
	sess.must(err)
	require.Equal(sess.t, http.StatusNotFound, deleted.Status())
}

func testPreserveCustomBurnTTL(sess *uiSession) {
	request := sess.browserContext.Request()
	headers := map[string]string{"Accept": "application/json"}
	created, err := request.Post("/notes", playwright.APIRequestContextPostOptions{
		Headers: headers, Data: map[string]any{
			"content": "Original content", "burn_after_reading": true, "ttl": 123,
		},

		FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil,
		MaxRetries: nil, Multipart: nil, Params: nil, Timeout: nil,
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusCreated, created.Status())

	var note struct {
		ID       string `json:"id"`
		EditCode string `json:"edit_code"`
	}

	sess.must(created.JSON(&note))
	sess.gotoPage("/edit/" + note.ID)
	sess.must(sess.expect.Locator(sess.page.Locator("#burnCheck")).ToBeChecked())
	sess.value("#burnTime", "123")
	sess.fill("#editor", "Updated content")
	sess.openSettings()
	sess.fill("#editCodeInput", note.EditCode)
	sess.closeSettings()
	sess.button("Save")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexp.MustCompile("/" + regexp.QuoteMeta(note.ID) + "$")))

	// Saving must preserve a grace period, so both subsequent reads succeed rather than
	// consuming the note on the first read as an accidental ttl=0 would do.
	for range 2 {
		read, readErr := request.Get("/notes/"+note.ID, playwright.APIRequestContextGetOptions{
			Headers: headers,

			Data: nil, FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil,
			MaxRetries: nil, Multipart: nil, Params: nil, Timeout: nil,
		})
		sess.must(readErr)
		require.Equal(sess.t, http.StatusOK, read.Status())

		var saved struct {
			Content   string    `json:"content"`
			ExpiresAt time.Time `json:"expires_at"`
		}

		sess.must(read.JSON(&saved))
		require.Equal(sess.t, "Updated content", saved.Content)
		require.WithinDuration(sess.t, time.Now().Add(123*time.Second), saved.ExpiresAt, 10*time.Second)
	}
}

func testOneTimeEditCode(sess *uiSession) {
	sess.publishNote()
	_, err := sess.page.Reload()
	sess.must(err)
	sess.must(sess.expect.Locator(sess.page.Locator("#editCodeBlock")).ToBeHidden())
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleLink, "Open note")).ToBeVisible())
}

func testReveal(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#editor", "# One-time message")
	sess.openSettings()
	sess.click("#burnLabel")
	sess.selectOption("#burnTime", "0")
	sess.closeSettings()
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.link("Open note")
	sess.text(".confirm-title", "One-time document")
	sess.must(sess.expect.Locator(sess.page.Locator(".markdown-body")).ToHaveCount(0))
	sess.expectLayout(".header", ".confirm-card", ".confirm-actions")
	sess.screenshot("reveal.png")
	sess.button("Reveal and burn")
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "One-time message")).ToBeVisible())
}

func testPublishFailure(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#editor", "Keep this draft")
	sess.must(sess.page.Route("**/notes", func(route playwright.Route) {
		err := route.Fulfill(playwright.RouteFulfillOptions{
			Status: new(503), ContentType: new("application/json"),
			Body: `{"message":"Please try again"}`,

			Headers: nil, Path: nil, Response: nil,
		})
		if err != nil {
			sess.errorf("fulfill simulated failure: %v", err)
		}
	}))
	sess.button("Publish note")
	sess.text("#noteError", "Please try again")
	sess.text("#submitBtn", "Publish note")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	sess.value("#editor", "Keep this draft")
	sess.must(sess.page.Unroute("**/notes"))
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
}

// derivedTitleLength is the largest length of a title that the editor takes from the text.
const derivedTitleLength = 80

// testLongFirstLine: a note without a title takes its title from the first text line. A first
// line longer than the server limit of 500 characters must not stop the publication.
func testLongFirstLine(sess *uiSession) {
	line := strings.Repeat("long first line ", 40)
	require.Greater(sess.t, len(line), 500)

	sess.gotoPage("/")
	sess.must(sess.expect.Locator(sess.page.Locator("#titleInput")).ToHaveAttribute("maxlength", "500"))
	sess.fill("#editor", line+"\n\nSecond paragraph.")
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.link("Open note")
	sess.text(".article-title", strings.TrimSpace(line[:derivedTitleLength])+"…")
}

func testInvalidEditCode(sess *uiSession) {
	sess.publishNote()
	sess.link("Open note")
	sess.openEditor()
	sess.openSettings()
	sess.fill("#editCodeInput", "wrong-edit-code")
	sess.closeSettings()
	sess.fill("#editor", "Unsaved draft")
	sess.button("Save")
	sess.text("#noteError", "Invalid edit code")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	sess.value("#editor", "Unsaved draft")
}

func testNotFound(sess *uiSession) {
	sess.gotoPage("/missing-ui-note")
	sess.text(".error-code", "404")
	sess.expectLayout(".header", ".error-content", ".error-actions")
	sess.screenshot("not-found.png")
	sess.link("Create new paste")
	sess.visible("#editor")
}

// contentsLongHeading is longer than two lines of the contents list at both screen sizes.
const contentsLongHeading = "A second detail with a long heading that does not fit on two lines " +
	"of the narrow contents column"

// contentsLabels returns the rows of the contents list: the h2, h3 and h4 headings of contentsNote.
// The h5 heading "Too deep" is not in the list.
func contentsLabels() []string {
	return []string{
		"First part", "Detail", "Fine point", contentsLongHeading, "Second part", "Second part detail", "Third part",
	}
}

// contentsNote has h2, h3, h4 and h5 headings and enough text to scroll, so a sidebar jump moves
// the page.
func contentsNote() string {
	filler := strings.Repeat("A paragraph that makes the section long enough to scroll.\n\n", 12)

	return "# Guide\n\nIntro.\n\n## First part\n\n" + filler + "### Detail\n\n" + filler +
		"#### Fine point\n\n" + filler + "##### Too deep\n\n" + filler +
		"### " + contentsLongHeading + "\n\n" + filler +
		"## Second part\n\n" + filler + "### Second part detail\n\n" + filler +
		"## Third part\n\n" + filler
}

// publishContentsNote publishes contentsNote with the sidebar switch set as requested. It returns
// the edit code from the success page and opens the note.
func (sess *uiSession) publishContentsNote(showContents bool) string {
	sess.t.Helper()
	sess.gotoPage("/")
	sess.fill("#titleInput", "Guide")
	sess.fill("#editor", contentsNote())
	sess.must(sess.expect.Locator(sess.page.Locator("#contentsCheck")).Not().ToBeChecked())

	if showContents {
		sess.openSettings()
		sess.click("#contentsLabel")
		sess.must(sess.expect.Locator(sess.page.Locator("#contentsCheck")).ToBeChecked())
		sess.closeSettings()
	}

	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	code := sess.shownEditCode()
	sess.link("Open note")
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Second part")).ToBeAttached())

	return code
}

// putBodies records the JSON body of each PUT request, so a test can check which fields the
// editor sent.
func (sess *uiSession) putBodies() *safeLog {
	var bodies safeLog

	sess.page.OnRequest(func(request playwright.Request) {
		if request.Method() != http.MethodPut {
			return
		}

		body, err := request.PostData()
		if err == nil {
			_, _ = fmt.Fprintln(&bodies, body)
		}
	})

	return &bodies
}

// expectContentsPlacement checks the sidebar position: beside the article on desktop and above
// it on a phone.
func (sess *uiSession) expectContentsPlacement() {
	sess.t.Helper()

	navBox, err := sess.page.Locator(".note-toc").BoundingBox()
	sess.must(err)
	articleBox, err := sess.page.Locator(".article-card").BoundingBox()
	sess.must(err)

	if sess.screen.name == "mobile" {
		require.LessOrEqual(sess.t, navBox.Y+navBox.Height, articleBox.Y, "the sidebar is above the article")

		return
	}

	require.LessOrEqual(sess.t, navBox.X+navBox.Width, articleBox.X, "the sidebar is beside the article")
}

// openContents opens the "On this page" drop-down of narrow screens. Wide screens always show the
// list beside the article.
func (sess *uiSession) openContents() {
	sess.t.Helper()

	if !sess.mobile() {
		return
	}

	sess.click("#noteTocToggle")
	sess.must(sess.expect.Locator(sess.page.Locator("#noteTocToggle")).ToHaveAttribute("aria-expanded", "true"))
	sess.visible("#noteTocList")
}

// visibleContents returns the labels of the rows that the list shows: collapsed sections hide
// their subsections.
//
//nolint:ireturn // playwright-go returns an interface
func (sess *uiSession) visibleContents() playwright.Locator {
	return sess.page.Locator(".note-toc-label:visible")
}

func testContentsSidebar(sess *uiSession) {
	code := sess.publishContentsNote(true)

	nav := sess.role(playwright.AriaRoleNavigation, "On this page")
	sess.must(sess.expect.Locator(nav).ToBeVisible())
	sess.expectLayout(".header", ".note-toc", ".article-card", ".site-footer")
	sess.expectContentsPlacement()
	sess.expectNoteCard()
	sess.screenshot("note-toc.png")
	sess.expectMenuOverContents()
	sess.openContents()

	// The rows have no numbers: the label is the only text of a row. H5 is not in the list.
	sess.must(sess.expect.Locator(sess.page.Locator(".note-toc-link")).ToHaveText(contentsLabels()))
	sess.must(sess.expect.Locator(sess.page.Locator(".note-toc-item--h4 .note-toc-label")).ToHaveText("Fine point"))
	// Only the section that the reader is in is expanded.
	sess.must(sess.expect.Locator(sess.visibleContents()).ToHaveText(
		[]string{"First part", "Detail", "Fine point", contentsLongHeading, "Second part", "Third part"}))
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleLink, "First part")).
		ToHaveAttribute("aria-current", "location"))
	sess.expectClampedContents()

	sess.link("Second part")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexp.MustCompile(`#second-part$`)))
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Second part")).ToBeInViewport())
	sess.must(sess.expect.Locator(sess.page.Locator(".note-toc-link.active .note-toc-label")).ToHaveText("Second part"))
	sess.expectHeadingBelowContents("Second part")

	sess.expectContentsSaved(code, nav)
}

// expectMenuOverContents checks that the header menu of a phone covers the sticky contents block.
func (sess *uiSession) expectMenuOverContents() {
	sess.t.Helper()

	if !sess.mobile() {
		return
	}

	sess.click("#menuBtn")
	sess.visible("#headerMenu")
	onTop := sess.eval(`() => {
		const menu = document.getElementById("headerMenu");
		const nav = document.getElementById("noteToc").getBoundingClientRect();
		const box = menu.getBoundingClientRect();
		const y = Math.max(box.top, nav.top) + 4;
		const hit = document.elementFromPoint(box.left + box.width / 2, Math.min(y, box.bottom - 4));
		return box.bottom > nav.top && menu.contains(hit);
	}`)
	require.Equal(sess.t, true, onTop, "the open menu is above the contents block")
	sess.must(sess.page.Keyboard().Press("Escape"))
	sess.must(sess.expect.Locator(sess.page.Locator("#headerMenu")).ToBeHidden())
}

// expectClampedContents checks that a long heading takes two lines of the list at most, with the
// full text in the title of the link.
func (sess *uiSession) expectClampedContents() {
	sess.t.Helper()

	link := sess.page.Locator(".note-toc-link").Filter(playwright.LocatorFilterOptions{
		HasText: contentsLongHeading,

		Has: nil, HasNot: nil, HasNotText: nil, Visible: nil,
	})
	sess.must(sess.expect.Locator(link).ToHaveAttribute("title", contentsLongHeading))

	clamp := sess.evalWith(`(text) => {
		const label = [...document.querySelectorAll(".note-toc-label")].find((node) => node.textContent === text);
		const style = getComputedStyle(label);
		const lines = Math.round(label.clientHeight / parseFloat(style.lineHeight));
		return [style.webkitLineClamp, lines, label.scrollHeight > label.clientHeight + 1].join(" ");
	}`, contentsLongHeading)
	require.Equal(sess.t, "2 2 true", clamp, "the long label shows two lines and cuts the rest")
}

// expectHeadingBelowContents checks that a jump on a phone stops the heading below the sticky
// "On this page" block, and that the jump closes the drop-down.
func (sess *uiSession) expectHeadingBelowContents(heading string) {
	sess.t.Helper()

	if !sess.mobile() {
		return
	}

	sess.must(sess.expect.Locator(sess.page.Locator("#noteTocToggle")).ToHaveAttribute("aria-expanded", "false"))
	navBox, err := sess.page.Locator(".note-toc").BoundingBox()
	sess.must(err)
	headingBox, err := sess.role(playwright.AriaRoleHeading, heading).BoundingBox()
	sess.must(err)
	require.GreaterOrEqual(sess.t, headingBox.Y, navBox.Y+navBox.Height, "the heading is not under the block")
}

// expectContentsSaved checks the sidebar switch of the editor: edit mode pre-sets the stored value
// and sends it only after a change.
func (sess *uiSession) expectContentsSaved(code string, nav playwright.Locator) {
	sess.t.Helper()

	bodies := sess.putBodies()
	sess.openEditor()
	sess.must(sess.expect.Locator(sess.page.Locator("#contentsCheck")).ToBeChecked())
	sess.openSettings()
	sess.fill("#editCodeInput", code)
	sess.closeSettings()
	sess.button("Save")
	sess.must(sess.expect.Page(sess.page).Not().ToHaveURL(regexpURL("/edit/")))
	sess.must(sess.expect.Locator(nav).ToBeVisible())
	require.NotContains(sess.t, bodies.String(), "show_contents")

	sess.openEditor()
	sess.openSettings()
	sess.click("#contentsLabel")
	sess.must(sess.expect.Locator(sess.page.Locator("#contentsCheck")).Not().ToBeChecked())
	sess.fill("#editCodeInput", code)
	sess.closeSettings()
	sess.button("Save")
	sess.must(sess.expect.Page(sess.page).Not().ToHaveURL(regexpURL("/edit/")))
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Second part")).ToBeAttached())
	sess.must(sess.expect.Locator(sess.page.Locator(".note-toc")).ToHaveCount(0))
	require.Contains(sess.t, bodies.String(), `"show_contents":false`)
}

// testContentsSections checks the chevrons of the sections and the "Expand all" button.
func testContentsSections(sess *uiSession) {
	sess.publishContentsNote(true)
	sess.openContents()
	visible := sess.visibleContents()

	expand := sess.role(playwright.AriaRoleButton, "Expand Second part")
	sess.must(sess.expect.Locator(expand).ToHaveText("1"))
	sess.must(sess.expect.Locator(expand).ToHaveAttribute("aria-expanded", "false"))
	sess.must(expand.Click())

	// A chevron expands its section and collapses the others; a second click collapses it.
	collapse := sess.role(playwright.AriaRoleButton, "Collapse Second part")
	sess.must(sess.expect.Locator(collapse).ToHaveAttribute("aria-expanded", "true"))
	sess.must(sess.expect.Locator(visible).ToHaveText(
		[]string{"First part", "Second part", "Second part detail", "Third part"}))
	sess.must(collapse.Click())
	sess.must(sess.expect.Locator(visible).ToHaveText([]string{"First part", "Second part", "Third part"}))

	all := sess.page.Locator("#noteTocAll")
	if sess.mobile() {
		// The phone drop-down has the chevrons only, as in the mobile design.
		sess.must(sess.expect.Locator(all).ToBeHidden())

		return
	}

	sess.must(sess.expect.Locator(all).ToHaveText("Expand all"))
	sess.click("#noteTocAll")
	sess.must(sess.expect.Locator(all).ToHaveText("Collapse"))
	sess.must(sess.expect.Locator(all).ToHaveAttribute("aria-expanded", "true"))
	sess.must(sess.expect.Locator(visible).ToHaveText(contentsLabels()))

	// "Collapse" goes back to the section that the reader is in.
	sess.click("#noteTocAll")
	sess.must(sess.expect.Locator(all).ToHaveText("Expand all"))
	sess.must(sess.expect.Locator(visible).ToHaveText(
		[]string{"First part", "Detail", "Fine point", contentsLongHeading, "Second part", "Third part"}))
}

// testContentsScroll checks that the current row follows the scroll position, that the list
// scrolls to it without a page scroll, the sticky block and "Back to top".
func testContentsScroll(sess *uiSession) {
	sess.publishContentsNote(true)
	// A low window makes the list longer than its box, so the list must scroll to the current row.
	sess.must(sess.page.SetViewportSize(sess.screen.width, contentsWindowHeight))
	sess.openContents()

	if !sess.mobile() {
		sess.click("#noteTocAll")
	}

	current := sess.page.Locator(".note-toc-link.active .note-toc-label")
	scrollY := sess.scrollToHeading("Second part detail")
	sess.must(sess.expect.Locator(current).ToHaveText("Second part detail"))
	sess.must(sess.expect.Locator(sess.page.Locator(".note-toc-item--section > .note-toc-row .note-toc-label")).
		ToHaveText("Second part"))
	sess.expectCurrentInList(scrollY)
	sess.expectStickyContents()

	if !sess.mobile() {
		sess.click("#noteTocAll")
	}

	// Only the section that the reader is in is expanded, also after the reader goes to another one.
	sess.must(sess.expect.Locator(sess.visibleContents()).ToHaveText(
		[]string{"First part", "Second part", "Second part detail", "Third part"}))
	sess.scrollToHeading("Detail")
	sess.must(sess.expect.Locator(current).ToHaveText("Detail"))
	sess.must(sess.expect.Locator(sess.visibleContents()).ToHaveText(
		[]string{"First part", "Detail", "Fine point", contentsLongHeading, "Second part", "Third part"}))

	top := sess.role(playwright.AriaRoleLink, "Back to top")
	if sess.mobile() {
		sess.must(sess.expect.Locator(top).ToBeHidden())

		return
	}

	sess.must(top.Click())
	_, err := sess.page.WaitForFunction("() => window.scrollY === 0", nil)
	sess.must(err)
	sess.must(sess.expect.Locator(current).ToHaveText("First part"))
}

// contentsWindowHeight is the window height of testContentsScroll: the list box is then lower
// than the rows of an expanded section.
const contentsWindowHeight = 280

// scrollToHeading scrolls the page so that the heading is near the top of the window, and returns
// the new scroll position.
func (sess *uiSession) scrollToHeading(name string) float64 {
	sess.t.Helper()

	return sess.number(sess.evalWith(`(name) => {
		const headings = document.querySelectorAll(".prose.markdown-body :is(h2, h3, h4)");
		const heading = [...headings].find((node) => node.textContent.trim() === name);
		window.scrollTo(0, Math.round(heading.getBoundingClientRect().top + window.scrollY - 60));
		return window.scrollY;
	}`, name))
}

// expectCurrentInList checks that the current row is inside the visible part of the list and that
// the page stayed at scrollY: only the list scrolls.
func (sess *uiSession) expectCurrentInList(scrollY float64) {
	sess.t.Helper()

	inside := sess.eval(`() => {
		const list = document.getElementById("noteTocList");
		const box = list.getBoundingClientRect();
		const row = list.querySelector(".note-toc-link.active").getBoundingClientRect();
		return {scrolled: list.scrollTop > 0, below: row.top >= box.top - 1, above: row.bottom <= box.bottom + 1};
	}`)
	require.Equal(sess.t, map[string]any{"scrolled": true, "below": true, "above": true}, inside,
		"the list scrolls to the current row")
	require.InDelta(sess.t, scrollY, sess.number(sess.eval("() => window.scrollY")), 1, "the page does not scroll")
}

// expectStickyContents checks that the contents stay at the top of the window after a scroll.
func (sess *uiSession) expectStickyContents() {
	sess.t.Helper()

	box, err := sess.page.Locator(".note-toc").BoundingBox()
	sess.must(err)

	top := 24.0
	if sess.mobile() {
		top = 8
	}

	require.InDelta(sess.t, top, box.Y, 1, "the contents are sticky")
}

// number converts a JavaScript number from Evaluate to float64.
func (sess *uiSession) number(value any) float64 {
	sess.t.Helper()

	switch typed := value.(type) {
	case int:
		return float64(typed)
	case float64:
		return typed
	default:
		require.Failf(sess.t, "not a number", "%T %v", value, value)

		return 0
	}
}

// evalWith runs a JavaScript function with one argument and returns its result.
//
//nolint:ireturn // playwright-go returns any
func (sess *uiSession) evalWith(expression string, arg any) any {
	sess.t.Helper()
	value, err := sess.page.Evaluate(expression, arg)
	sess.must(err)

	return value
}

func testNoContentsSidebar(sess *uiSession) {
	sess.publishContentsNote(false)
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "First part")).ToBeVisible())
	sess.must(sess.expect.Locator(sess.page.Locator(".note-toc")).ToHaveCount(0))
	sess.must(sess.expect.Locator(sess.page.Locator(".note-layout--toc")).ToHaveCount(0))
	sess.must(sess.expect.Locator(sess.page.Locator(`script[src*="note-contents.js"]`)).ToHaveCount(0))

	articleBox, err := sess.page.Locator(".article-card").BoundingBox()
	sess.must(err)
	layoutBox, err := sess.page.Locator(".note-layout").BoundingBox()
	sess.must(err)
	require.InDelta(sess.t, layoutBox.Width, articleBox.Width, 1, "the article takes the full width")
	sess.expectNoteCard()
}

// expectNoteCard checks the frame of the note: a card on wide screens; on a phone the text is on the
// page background, 16px from the screen edges.
func (sess *uiSession) expectNoteCard() {
	sess.t.Helper()

	frame := sess.eval(`() => {
		const article = document.querySelector(".note-article");
		const style = getComputedStyle(article);
		const box = article.getBoundingClientRect();
		return [style.borderTopWidth, style.backgroundColor, style.boxShadow,
			Math.round(box.left), Math.round(window.innerWidth - box.right)].join(" | ");
	}`)
	if !sess.mobile() {
		require.Contains(sess.t, frame, "1px | ", "a wide screen shows the note in a card")

		return
	}

	require.Equal(sess.t, "0px | rgba(0, 0, 0, 0) | none | 16 | 16", frame, "a phone shows the note without a card")
}

func testEditorFillsHeight(sess *uiSession) {
	sess.gotoPage("/")
	require.Equal(sess.t, "none", sess.eval(`() => getComputedStyle(document.getElementById("editor")).resize`),
		"the textarea has no resize handle")
	sess.expectFullHeight()
	sess.selectView("Preview")
	sess.expectFullHeight()

	if sess.screen.name != "desktop" {
		return
	}

	// The settings panel is taller than the default screen, so a tall screen shows the extra height.
	const tallHeight = 1200
	sess.must(sess.page.SetViewportSize(sess.screen.width, tallHeight))
	sess.selectView("Write")
	sess.expectFullHeight()

	siteFooterBox, err := sess.page.Locator(".site-footer").BoundingBox()
	sess.must(err)
	require.InDelta(sess.t, float64(tallHeight), siteFooterBox.Y+siteFooterBox.Height, 1,
		"the page fits the screen height")

	editorBox, err := sess.page.Locator("#editor").BoundingBox()
	sess.must(err)
	require.Greater(sess.t, editorBox.Height, float64(470), "the textarea takes the extra height")
	sess.must(sess.page.SetViewportSize(sess.screen.width, sess.screen.height))
}

// expectFullHeight checks that the editor card fills the free page height above the site footer
// and that the card footer stays at the bottom of the card. Phone width has no card footer and
// no site footer: there the card reaches the fixed bar with the main action.
func (sess *uiSession) expectFullHeight() {
	sess.t.Helper()
	sess.eval("() => document.fonts.ready")

	cardBox, err := sess.page.Locator(".editor-card").BoundingBox()
	sess.must(err)

	if sess.mobile() {
		barBox, barErr := sess.page.Locator(".publish-bar").BoundingBox()
		sess.must(barErr)
		require.InDelta(sess.t, barBox.Y, cardBox.Y+cardBox.Height, 1, "the editor card reaches the bar")

		return
	}

	footerBox, err := sess.page.Locator(".editor-card .editor-footer").BoundingBox()
	sess.must(err)
	require.InDelta(sess.t, cardBox.Y+cardBox.Height-1, footerBox.Y+footerBox.Height, 1,
		"the editor footer is at the bottom of the card")

	pageBottom, ok := sess.eval(`() => {
		const layout = document.querySelector(".create-layout");
		return Math.round(layout.getBoundingClientRect().bottom - parseFloat(getComputedStyle(layout).paddingBottom));
	}`).(int)
	require.True(sess.t, ok, "the layout bottom must be an int")
	require.InDelta(sess.t, float64(pageBottom), cardBox.Y+cardBox.Height, 1, "the editor card fills the free height")
}

// noteContent reads the stored content of a note through the JSON API.
func (sess *uiSession) noteContent(id string) string {
	sess.t.Helper()
	response, err := sess.browserContext.Request().Get("/notes/"+id, playwright.APIRequestContextGetOptions{
		Headers: map[string]string{"Accept": "application/json"},

		Data: nil, FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil,
		MaxRetries: nil, Multipart: nil, Params: nil, Timeout: nil,
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusOK, response.Status())

	var note struct {
		Content string `json:"content"`
	}

	sess.must(response.JSON(&note))

	return note.Content
}

// testKeepsWhitespace: the editor sends the text as the author wrote it. Four leading spaces make
// a code block in the preview and on the page, and the final newline stays. The edit page keeps
// a leading newline, which the HTML parser removes after <textarea> without the template newline.
func testKeepsWhitespace(sess *uiSession) {
	const (
		content = "    print(42)\n"
		edited  = "\n    edited()\n"
		code    = "Whitespace-Code-42"
	)

	sess.gotoPage("/")
	sess.fill("#slugInput", "ui-code")
	sess.writeText(content)
	sess.showPreview()
	sess.text("#preview pre code", "print(42)")
	sess.openSettings()
	sess.fill("#editCodeInput", code)
	sess.closeSettings()
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	require.Equal(sess.t, content, sess.noteContent("ui-code"))

	sess.gotoPage("/edit/ui-code")
	sess.value("#editor", content)
	sess.writeText(edited)
	sess.openSettings()
	sess.fill("#editCodeInput", code)
	sess.closeSettings()
	sess.button("Save")
	// The exact URL: a pattern "/ui-code$" also matches the editor at /edit/ui-code.
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/ui-code"))
	require.Equal(sess.t, edited, sess.noteContent("ui-code"))

	sess.gotoPage("/edit/ui-code")
	sess.value("#editor", edited)
}

// blockSessionStorage makes each sessionStorage write of the context fail, as in a browser that
// blocks site data or has no free storage space.
func (sess *uiSession) blockSessionStorage() {
	sess.t.Helper()
	sess.must(sess.browserContext.AddInitScript(playwright.Script{
		Content: new(`Storage.prototype.setItem = function () {
			throw new DOMException('The quota has been exceeded.', 'QuotaExceededError');
		};`),

		Path: nil,
	}))
}

// testBlockedStorage: the note exists after 201, so a failed sessionStorage write must not keep
// the author in the editor. The success page shows the link and the generated edit code once.
func testBlockedStorage(sess *uiSession) {
	sess.blockSessionStorage()
	sess.gotoPage("/")
	sess.fill("#editor", "# Stored nowhere")
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.text(".success-title", "Your note is live")
	sess.must(sess.expect.Locator(sess.page.Locator(".copy-value")).ToHaveText(regexpURL(sess.baseURL + "/")))
	require.NotEmpty(sess.t, sess.shownEditCode())

	// The page removes the code from window.name: other pages of the tab cannot read it, and a
	// reload does not show it again.
	require.Empty(sess.t, sess.eval("() => window.name"))
	sess.reload()
	sess.must(sess.expect.Locator(sess.page.Locator("#editCodeBlock")).ToBeHidden())
}

// testBurnDelay: a note that burns 5 minutes after its first read has no expiry date before that
// read. The success page shows the delay and does not say that the note burns immediately.
func testBurnDelay(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#editor", "Burns later")
	sess.openSettings()
	sess.click("#burnLabel")
	sess.selectOption("#burnTime", "300")
	sess.closeSettings()
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.text(".chip-text", "burns 5 minutes after the first read")
	sess.text(".chip--warn", "Burns after reading")
}
