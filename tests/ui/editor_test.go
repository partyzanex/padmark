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

			themeTitle := strings.ToUpper(theme[:1]) + theme[1:]
			sess.button(themeTitle + " theme")
			sess.must(sess.expect.Locator(sess.page.Locator("html")).ToHaveAttribute("data-theme", theme))
			sess.expectLayout(".header", ".logo", "#themeSwitcher", ".header-links", "#editor", "#titleInput",
				"#slugInput", "#burnLabel", "#privacySelect", "#submitBtn")
			sess.screenshot("editor-" + theme + ".png")
			_, err := sess.page.Reload()
			sess.must(err)
			sess.must(sess.expect.Locator(sess.page.Locator("html")).ToHaveAttribute("data-theme", theme))
			activeButton := sess.page.Locator(fmt.Sprintf(`.theme-btn[data-theme-value="%s"]`, theme))
			sess.must(sess.expect.Locator(activeButton).ToHaveClass(regexp.MustCompile("active")))
		},
			accounts: false, allowedStatuses: nil,
		})
	}

	cases = append(cases,
		scenario{name: "Toolbar", run: testToolbar, accounts: false, allowedStatuses: nil},
		scenario{name: "FillsHeight", run: testEditorFillsHeight, accounts: false, allowedStatuses: nil},
		scenario{name: "Options", run: testOptions, accounts: false, allowedStatuses: nil},
		scenario{name: "PrivacyNeedsAccounts", allowedStatuses: []int{http.StatusUnprocessableEntity},
			run:      testPrivacyNeedsAccounts,
			accounts: false,
		},
		scenario{name: "EmptyContent", run: testEmptyContent, accounts: false, allowedStatuses: nil},
		scenario{name: "PublishAndView", run: testPublishAndView, accounts: false, allowedStatuses: nil},
		scenario{name: "ContentsSidebar", run: testContentsSidebar, accounts: false, allowedStatuses: nil},
		scenario{name: "NoContentsSidebar", run: testNoContentsSidebar, accounts: false, allowedStatuses: nil},
		scenario{name: "EditAndSave", run: testEditAndSave, accounts: false, allowedStatuses: nil},
		scenario{name: "PreserveCustomBurnTTL", run: testPreserveCustomBurnTTL, accounts: false, allowedStatuses: nil},
		scenario{name: "OneTimeEditCode", run: testOneTimeEditCode, accounts: false, allowedStatuses: nil},
		scenario{name: "Reveal", run: testReveal, accounts: false, allowedStatuses: nil},
		scenario{name: "PublishFailure", allowedStatuses: []int{503}, run: testPublishFailure, accounts: false},
		scenario{name: "InvalidEditCode", allowedStatuses: []int{403}, run: testInvalidEditCode, accounts: false},
		scenario{name: "NotFound", allowedStatuses: []int{404}, run: testNotFound, accounts: false},
	)
	runScenarios(t, cases)
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
	sess.text("#submitBtn", "Enter some text first")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	require.Empty(sess.t, submissions.String())
}

func testPublishAndView(sess *uiSession) {
	code, noteURL := sess.publishNote()
	sess.expectLayout(".header", ".success-card", "#editCodeBlock", ".action-row")
	sess.screenshot("created.png", ".copy-value", "#editCodeValue")
	sess.must(sess.browserContext.GrantPermissions([]string{"clipboard-read", "clipboard-write"}))
	sess.click("#editCodeCopyBtn")
	sess.text("#editCodeCopyBtn", "Copied")
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
	sess.link("Edit")
	sess.value("#titleInput", "UI test note")
	sess.value("#editor", "# Hello\n\nThis is **Markdown**.")
	sess.expectLayout("#editor", "#titleInput", "#editCodeInput", "#submitBtn")
	sess.screenshot("edit.png", "#slugInput")
	sess.button("Save")
	sess.text("#submitBtn", "Edit code required")
	sess.fill("#editCodeInput", code)
	sess.fill("#titleInput", "Updated title")
	sess.fill("#editor", "# Updated\n\nSaved in the browser.")
	sess.must(sess.page.Locator("#editor").Press("Control+Enter"))
	sess.must(sess.expect.Page(sess.page).Not().ToHaveURL(regexpURL("/edit/")))
	sess.must(sess.expect.Page(sess.page).ToHaveTitle("Updated title — Padmark"))
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Updated")).ToBeVisible())
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
	sess.fill("#editCodeInput", note.EditCode)
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
	sess.click("#burnLabel")
	sess.selectOption("#burnTime", "0")
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
	sess.text("#submitBtn", "Please try again")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	sess.value("#editor", "Keep this draft")
	sess.must(sess.page.Unroute("**/notes"))
	sess.button("Please try again")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
}

func testInvalidEditCode(sess *uiSession) {
	sess.publishNote()
	sess.link("Open note")
	sess.link("Edit")
	sess.fill("#editCodeInput", "wrong-edit-code")
	sess.fill("#editor", "Unsaved draft")
	sess.button("Save")
	sess.text("#submitBtn", "Invalid edit code")
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

// contentsNote has h2 and h3 headings and enough text to scroll, so a sidebar jump moves the page.
func contentsNote() string {
	filler := strings.Repeat("A paragraph that makes the section long enough to scroll.\n\n", 12)

	return "# Guide\n\nIntro.\n\n## First part\n\n" + filler + "### Detail\n\n" + filler +
		"## Second part\n\n" + filler
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
		sess.click("#contentsLabel")
		sess.must(sess.expect.Locator(sess.page.Locator("#contentsCheck")).ToBeChecked())
	}

	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.must(sess.expect.Locator(sess.page.Locator("#editCodeValue")).ToHaveText(nonempty))
	code, err := sess.page.Locator("#editCodeValue").InnerText()
	sess.must(err)
	sess.link("Open note")
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Second part")).ToBeAttached())

	return strings.TrimSpace(code)
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

func testContentsSidebar(sess *uiSession) {
	code := sess.publishContentsNote(true)

	nav := sess.role(playwright.AriaRoleNavigation, "On this page")
	sess.must(sess.expect.Locator(nav).ToBeVisible())
	links := sess.page.Locator(".note-toc-link")
	sess.must(sess.expect.Locator(links).ToHaveText([]string{"First part", "Detail", "Second part"}))
	sess.must(sess.expect.Locator(sess.page.Locator(".note-toc-item--sub")).ToHaveText("Detail"))
	sess.must(sess.expect.Locator(links.First()).ToHaveAttribute("aria-current", "location"))
	sess.expectLayout(".header", ".note-toc", ".article-card", ".site-footer")
	sess.expectContentsPlacement()
	sess.screenshot("note-toc.png")

	sess.link("Second part")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexp.MustCompile(`#second-part$`)))
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Second part")).ToBeInViewport())
	sess.must(sess.expect.Locator(links.Last()).ToHaveAttribute("aria-current", "location"))

	// Edit mode pre-sets the stored value and sends it only after a change.
	bodies := sess.putBodies()
	sess.link("Edit")
	sess.must(sess.expect.Locator(sess.page.Locator("#contentsCheck")).ToBeChecked())
	sess.fill("#editCodeInput", code)
	sess.button("Save")
	sess.must(sess.expect.Page(sess.page).Not().ToHaveURL(regexpURL("/edit/")))
	sess.must(sess.expect.Locator(nav).ToBeVisible())
	require.NotContains(sess.t, bodies.String(), "show_contents")

	sess.link("Edit")
	sess.click("#contentsLabel")
	sess.must(sess.expect.Locator(sess.page.Locator("#contentsCheck")).Not().ToBeChecked())
	sess.fill("#editCodeInput", code)
	sess.button("Save")
	sess.must(sess.expect.Page(sess.page).Not().ToHaveURL(regexpURL("/edit/")))
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Second part")).ToBeAttached())
	sess.must(sess.expect.Locator(sess.page.Locator(".note-toc")).ToHaveCount(0))
	require.Contains(sess.t, bodies.String(), `"show_contents":false`)
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

func testNoContentsSidebar(sess *uiSession) {
	sess.publishContentsNote(false)
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "First part")).ToBeVisible())
	sess.must(sess.expect.Locator(sess.page.Locator(".note-toc")).ToHaveCount(0))
	sess.must(sess.expect.Locator(sess.page.Locator(".note-layout--toc")).ToHaveCount(0))

	articleBox, err := sess.page.Locator(".article-card").BoundingBox()
	sess.must(err)
	layoutBox, err := sess.page.Locator(".note-layout").BoundingBox()
	sess.must(err)
	require.InDelta(sess.t, layoutBox.Width, articleBox.Width, 1, "the article takes the full width")
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
// and that the card footer stays at the bottom of the card.
func (sess *uiSession) expectFullHeight() {
	sess.t.Helper()
	sess.eval("() => document.fonts.ready")

	cardBox, err := sess.page.Locator(".editor-card").BoundingBox()
	sess.must(err)
	footerBox, err := sess.page.Locator(".editor-card .editor-footer").BoundingBox()
	sess.must(err)
	require.InDelta(sess.t, cardBox.Y+cardBox.Height-1, footerBox.Y+footerBox.Height, 1,
		"the editor footer is at the bottom of the card")

	if sess.screen.name != "desktop" {
		return
	}

	pageBottom, ok := sess.eval(`() => {
		const layout = document.querySelector(".create-layout");
		return Math.round(layout.getBoundingClientRect().bottom - parseFloat(getComputedStyle(layout).paddingBottom));
	}`).(int)
	require.True(sess.t, ok, "the layout bottom must be an int")
	require.InDelta(sess.t, float64(pageBottom), cardBox.Y+cardBox.Height, 1, "the editor card fills the free height")
}
