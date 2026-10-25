package ui_test

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

func TestEditor(t *testing.T) {
	cases := []scenario{}
	for _, theme := range []string{"light", "dim", "dark"} {
		cases = append(cases, scenario{name: "Theme_" + theme, run: func(e *ui) {
			e.gotoPage("/")
			e.fill("#titleInput", "Release notes")
			e.fill("#slugInput", "release-notes")
			e.fill("#editor", "# Release notes\n\nA small **Markdown** example.")
			e.must(e.page.GetByTitle(strings.ToUpper(theme[:1])+theme[1:], playwright.PageGetByTitleOptions{Exact: playwright.Bool(true)}).Click())
			e.must(e.expect.Locator(e.page.Locator("html")).ToHaveAttribute("data-theme", theme))
			e.expectLayout(".header", ".logo", "#themeSwitcher", ".header-links", "#editor", "#titleInput", "#slugInput", "#burnLabel", "#privacySelect", "#submitBtn")
			e.screenshot("editor-" + theme + ".png")
			_, err := e.page.Reload()
			e.must(err)
			e.must(e.expect.Locator(e.page.Locator("html")).ToHaveAttribute("data-theme", theme))
			e.must(e.expect.Locator(e.page.Locator(fmt.Sprintf(`.theme-btn[data-theme="%s"]`, theme))).ToHaveClass(regexp.MustCompile("active")))
		}})
	}
	cases = append(cases,
		scenario{name: "Toolbar", run: testToolbar},
		scenario{name: "Options", run: testOptions},
		scenario{name: "EmptyContent", run: testEmptyContent},
		scenario{name: "PublishAndView", run: testPublishAndView},
		scenario{name: "EditAndSave", run: testEditAndSave},
		scenario{name: "OneTimeEditCode", run: testOneTimeEditCode},
		scenario{name: "Reveal", run: testReveal},
		scenario{name: "PublishFailure", allowedStatuses: []int{503}, run: testPublishFailure},
		scenario{name: "InvalidEditCode", allowedStatuses: []int{403}, run: testInvalidEditCode},
		scenario{name: "NotFound", allowedStatuses: []int{404}, run: testNotFound},
	)
	runScenarios(t, cases)
}

func testToolbar(e *ui) {
	e.gotoPage("/")
	for _, format := range []struct{ button, expected string }{
		{"#mdBold", "**text**"}, {"#mdItalic", "_text_"},
		{"#mdCode", "`text`"}, {"#mdBlock", "\n```\ntext\n```\n"},
	} {
		e.fill("#editor", "text")
		e.eval("() => document.querySelector('#editor').select()")
		e.click(format.button)
		e.value("#editor", format.expected)
		e.text("#charCount", fmt.Sprintf("%d characters", len(format.expected)))
		e.must(e.expect.Locator(e.page.Locator("#editor")).ToBeFocused())
	}
}

func testOptions(e *ui) {
	e.gotoPage("/")
	e.must(e.expect.Locator(e.page.Locator("#burnTime")).ToBeDisabled())
	e.click("#burnLabel")
	e.must(e.expect.Locator(e.page.Locator("#burnCheck")).ToBeChecked())
	e.must(e.expect.Locator(e.page.Locator("#burnTime")).ToBeEnabled())
	e.selectOption("#burnTime", "3600")
	e.value("#burnTime", "3600")
	e.click("#burnLabel")
	e.must(e.expect.Locator(e.page.Locator("#burnTime")).ToBeDisabled())
	for _, privacy := range []struct{ value, hint string }{
		{"authenticated", "Requires login to view"}, {"owner", "Only you can view"}, {"public", "Anyone can view"},
	} {
		e.selectOption("#privacySelect", privacy.value)
		e.text("#privacyHint", privacy.hint)
	}
}

func testEmptyContent(e *ui) {
	e.gotoPage("/")
	var submissions safeLog
	e.page.OnRequest(func(request playwright.Request) {
		if request.Method() == "POST" {
			_, _ = fmt.Fprintln(&submissions, request.URL())
		}
	})
	e.fill("#titleInput", "Empty note")
	e.fill("#editor", "   ")
	e.button("Publish")
	e.text("#submitBtn", "Enter some text first")
	e.must(e.expect.Locator(e.page.Locator("#submitBtn")).ToBeEnabled())
	require.Empty(e.t, submissions.String())
}

func testPublishAndView(e *ui) {
	code, noteURL := e.publishNote()
	e.expectLayout(".header", ".success-card", "#editCodeBlock", ".actions-row")
	e.screenshot("created.png", ".copy-value")
	e.must(e.browserContext.GrantPermissions([]string{"clipboard-read", "clipboard-write"}))
	e.click("#editCodeCopyBtn")
	e.text("#editCodeCopyBtn", "Copied")
	require.Equal(e.t, code, e.eval("() => navigator.clipboard.readText()"))
	e.click("#copyLinkBtn")
	e.text("#copyLinkBtn", "Copied")
	base, err := url.Parse(e.baseURL)
	e.must(err)
	relative, err := url.Parse(noteURL)
	e.must(err)
	require.Equal(e.t, base.ResolveReference(relative).String(), e.eval("() => navigator.clipboard.readText()"))
	e.link("Go to paste")
	e.must(e.expect.Locator(e.role(playwright.AriaRoleHeading, "Hello")).ToBeVisible())
	e.text(".markdown-body strong", "Markdown")
	e.expectLayout(".header", ".meta-bar", ".article-card", ".paste-footer")
	e.screenshot("note.png")
	raw, err := e.page.ExpectPopup(func() error { return e.role(playwright.AriaRoleLink, "Raw").First().Click() })
	e.must(err)
	e.must(e.expect.Locator(raw.Locator("body")).ToContainText("# Hello"))
	e.must(e.expect.Locator(raw.Locator("body")).ToContainText("**Markdown**"))
	e.must(raw.Close())
}

func testEditAndSave(e *ui) {
	code, _ := e.publishNote()
	e.link("Go to paste")
	e.link("Edit")
	e.value("#titleInput", "UI test note")
	e.value("#editor", "# Hello\n\nThis is **Markdown**.")
	e.expectLayout("#editor", "#titleInput", "#editCodeInput", "#submitBtn")
	e.screenshot("edit.png")
	e.button("Save")
	e.text("#submitBtn", "Edit code required")
	e.fill("#editCodeInput", code)
	e.fill("#titleInput", "Updated title")
	e.fill("#editor", "# Updated\n\nSaved in the browser.")
	e.must(e.page.Locator("#editor").Press("Control+Enter"))
	e.must(e.expect.Page(e.page).Not().ToHaveURL(regexpURL("/edit/")))
	e.must(e.expect.Page(e.page).ToHaveTitle("Updated title — Padmark"))
	e.must(e.expect.Locator(e.role(playwright.AriaRoleHeading, "Updated")).ToBeVisible())
}

func testOneTimeEditCode(e *ui) {
	e.publishNote()
	_, err := e.page.Reload()
	e.must(err)
	e.must(e.expect.Locator(e.page.Locator("#editCodeBlock")).ToBeHidden())
	e.must(e.expect.Locator(e.role(playwright.AriaRoleLink, "Go to paste")).ToBeVisible())
}

func testReveal(e *ui) {
	e.gotoPage("/")
	e.fill("#editor", "# One-time message")
	e.click("#burnLabel")
	e.selectOption("#burnTime", "0")
	e.button("Publish")
	e.must(e.expect.Page(e.page).ToHaveURL(regexpURL("/success?")))
	e.link("Go to paste")
	e.text(".confirm-title", "One-time document")
	e.must(e.expect.Locator(e.page.Locator(".markdown-body")).ToHaveCount(0))
	e.expectLayout(".header", ".article-card", ".confirm-actions")
	e.screenshot("reveal.png")
	e.button("Reveal and burn")
	e.must(e.expect.Locator(e.role(playwright.AriaRoleHeading, "One-time message")).ToBeVisible())
}

func testPublishFailure(e *ui) {
	e.gotoPage("/")
	e.fill("#editor", "Keep this draft")
	e.must(e.page.Route("**/notes", func(route playwright.Route) {
		if err := route.Fulfill(playwright.RouteFulfillOptions{
			Status: playwright.Int(503), ContentType: playwright.String("application/json"),
			Body: `{"message":"Please try again"}`,
		}); err != nil {
			e.errorf("fulfill simulated failure: %v", err)
		}
	}))
	e.button("Publish")
	e.text("#submitBtn", "Please try again")
	e.must(e.expect.Locator(e.page.Locator("#submitBtn")).ToBeEnabled())
	e.value("#editor", "Keep this draft")
	e.must(e.page.Unroute("**/notes"))
	e.button("Please try again")
	e.must(e.expect.Page(e.page).ToHaveURL(regexpURL("/success?")))
}

func testInvalidEditCode(e *ui) {
	e.publishNote()
	e.link("Go to paste")
	e.link("Edit")
	e.fill("#editCodeInput", "wrong-edit-code")
	e.fill("#editor", "Unsaved draft")
	e.button("Save")
	e.text("#submitBtn", "Invalid edit code")
	e.must(e.expect.Locator(e.page.Locator("#submitBtn")).ToBeEnabled())
	e.value("#editor", "Unsaved draft")
}

func testNotFound(e *ui) {
	e.gotoPage("/missing-ui-note")
	e.text(".error-code", "404")
	e.expectLayout(".header", ".error-content", ".error-actions")
	e.screenshot("not-found.png")
	e.link("Create new paste")
	e.visible("#editor")
}
