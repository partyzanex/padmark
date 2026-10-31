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
			sess.must(sess.page.GetByTitle(themeTitle, playwright.PageGetByTitleOptions{Exact: new(true)}).Click())
			sess.must(sess.expect.Locator(sess.page.Locator("html")).ToHaveAttribute("data-theme", theme))
			sess.expectLayout(".header", ".logo", "#themeSwitcher", ".header-links", "#editor", "#titleInput",
				"#slugInput", "#burnLabel", "#privacySelect", "#submitBtn")
			sess.screenshot("editor-" + theme + ".png")
			_, err := sess.page.Reload()
			sess.must(err)
			sess.must(sess.expect.Locator(sess.page.Locator("html")).ToHaveAttribute("data-theme", theme))
			activeButton := sess.page.Locator(fmt.Sprintf(`.theme-btn[data-theme="%s"]`, theme))
			sess.must(sess.expect.Locator(activeButton).ToHaveClass(regexp.MustCompile("active")))
		}})
	}

	cases = append(cases,
		scenario{name: "Toolbar", run: testToolbar},
		scenario{name: "Options", run: testOptions},
		scenario{name: "PrivacyNeedsAccounts", allowedStatuses: []int{http.StatusUnprocessableEntity},
			run: testPrivacyNeedsAccounts},
		scenario{name: "EmptyContent", run: testEmptyContent},
		scenario{name: "PublishAndView", run: testPublishAndView},
		scenario{name: "EditAndSave", run: testEditAndSave},
		scenario{name: "PreserveCustomBurnTTL", run: testPreserveCustomBurnTTL},
		scenario{name: "OneTimeEditCode", run: testOneTimeEditCode},
		scenario{name: "Reveal", run: testReveal},
		scenario{name: "PublishFailure", allowedStatuses: []int{503}, run: testPublishFailure},
		scenario{name: "InvalidEditCode", allowedStatuses: []int{403}, run: testInvalidEditCode},
		scenario{name: "NotFound", allowedStatuses: []int{404}, run: testNotFound},
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
	sess.text("#privacyHint", "Anyone can view")
}

// testPrivacyNeedsAccounts pins review R2 in the default configuration: the API refuses a
// protected level it cannot enforce, and keeps accepting the deprecated v0.1.6 `private` flag,
// which, as in v0.1.6 without auth, an independent client can still read.
func testPrivacyNeedsAccounts(sess *uiSession) {
	request := sess.browserContext.Request()
	headers := map[string]string{"Accept": "application/json"}

	refused, err := request.Post("/notes", playwright.APIRequestContextPostOptions{
		Headers: headers, Data: map[string]any{"content": "protected?", "privacy": "authenticated"},
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusUnprocessableEntity, refused.Status())

	created, err := request.Post("/notes", playwright.APIRequestContextPostOptions{
		Headers: headers, Data: map[string]any{"content": "v0.1.6 private", "private": true},
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusCreated, created.Status())

	var note struct {
		ID string `json:"id"`
	}

	sess.must(created.JSON(&note))

	read, err := request.Get("/notes/"+note.ID, playwright.APIRequestContextGetOptions{Headers: headers})
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
	sess.button("Publish")
	sess.text("#submitBtn", "Enter some text first")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	require.Empty(sess.t, submissions.String())
}

func testPublishAndView(sess *uiSession) {
	code, noteURL := sess.publishNote()
	sess.expectLayout(".header", ".success-card", "#editCodeBlock", ".actions-row")
	sess.screenshot("created.png", ".copy-value")
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
	sess.link("Go to paste")
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleHeading, "Hello")).ToBeVisible())
	sess.text(".markdown-body strong", "Markdown")
	sess.expectLayout(".header", ".meta-bar", ".article-card", ".paste-footer")
	sess.screenshot("note.png")
	raw, err := sess.page.ExpectPopup(func() error { return sess.role(playwright.AriaRoleLink, "Raw").First().Click() })
	sess.must(err)
	sess.must(sess.expect.Locator(raw.Locator("body")).ToContainText("# Hello"))
	sess.must(sess.expect.Locator(raw.Locator("body")).ToContainText("**Markdown**"))
	sess.must(raw.Close())
}

func testEditAndSave(sess *uiSession) {
	code, _ := sess.publishNote()
	sess.link("Go to paste")
	sess.link("Edit")
	sess.value("#titleInput", "UI test note")
	sess.value("#editor", "# Hello\n\nThis is **Markdown**.")
	sess.expectLayout("#editor", "#titleInput", "#editCodeInput", "#submitBtn")
	sess.screenshot("edit.png")
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
		read, readErr := request.Get("/notes/"+note.ID, playwright.APIRequestContextGetOptions{Headers: headers})
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
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleLink, "Go to paste")).ToBeVisible())
}

func testReveal(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#editor", "# One-time message")
	sess.click("#burnLabel")
	sess.selectOption("#burnTime", "0")
	sess.button("Publish")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.link("Go to paste")
	sess.text(".confirm-title", "One-time document")
	sess.must(sess.expect.Locator(sess.page.Locator(".markdown-body")).ToHaveCount(0))
	sess.expectLayout(".header", ".article-card", ".confirm-actions")
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
		})
		if err != nil {
			sess.errorf("fulfill simulated failure: %v", err)
		}
	}))
	sess.button("Publish")
	sess.text("#submitBtn", "Please try again")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	sess.value("#editor", "Keep this draft")
	sess.must(sess.page.Unroute("**/notes"))
	sess.button("Please try again")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
}

func testInvalidEditCode(sess *uiSession) {
	sess.publishNote()
	sess.link("Go to paste")
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
