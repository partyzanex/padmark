package ui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

type linkMetadata struct {
	TargetURL      string     `json:"target_url"`
	Privacy        string     `json:"privacy"`
	ExpiresAt      *time.Time `json:"expires_at"`
	DirectRedirect *bool      `json:"direct_redirect"`
	Views          int        `json:"views"`
	BurnAfterClick bool       `json:"burn_after_click"`
}

func TestLinks(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "CreateEditDelete", run: testLinkCreateEditDelete, accounts: false, files: false, allowedStatuses: nil},
		{name: "Validation", run: testLinkValidation, accounts: false, files: false, allowedStatuses: nil},
		{name: "FailureRetry", allowedStatuses: []int{503}, run: testLinkFailureRetry, accounts: false, files: false},
		{name: "BurnAfterClick", allowedStatuses: []int{404}, run: testLinkBurnAfterClick, accounts: false, files: false},
		{name: "PrivacyAndOwnership", accounts: true, files: false, run: testLinkPrivacyAndOwnership, allowedStatuses: nil},
	})
}

func (sess *uiSession) createdLink() (string, string) {
	sess.t.Helper()
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.text(".success-title", "Your link is live")
	current, err := url.Parse(sess.page.URL())
	sess.must(err)

	id := current.Query().Get("id")
	require.NotEmpty(sess.t, id)

	return id, sess.shownEditCode()
}

func (sess *uiSession) readLink(id string) linkMetadata {
	sess.t.Helper()
	response, err := sess.browserContext.Request().Get("/links/" + id)
	sess.must(err)
	require.Equal(sess.t, http.StatusOK, response.Status())

	var metadata linkMetadata
	sess.must(response.JSON(&metadata))

	return metadata
}

func (sess *uiSession) createTestLink() (string, string) {
	sess.t.Helper()
	sess.gotoPage("/")
	sess.link("Short link")
	sess.expectPrivacyOptions("public")
	sess.fill("#targetInput", "https://example.com/original?value=1")
	sess.fill("#slugInput", "ui-link")
	sess.openSettings()
	sess.fill("#editCodeInput", "Link-Edit-Code42!")
	sess.selectOption("#expirySelect", "seconds")
	sess.fill("#ttlInput", "123")
	sess.selectOption("#redirectSelect", "false")
	sess.click("#burnLabel")
	sess.expectLayout("#editCodeInput", "#privacySelect", "#redirectSelect", "#ttlInput", "#burnLabel")
	sess.closeSettings()
	sess.expectLayout(".header", "#targetInput", "#slugInput", "#submitBtn")
	sess.screenshot("link-editor.png", "#previewHost")
	sess.button("Create link")
	id, code := sess.createdLink()
	require.Equal(sess.t, "ui-link", id)
	require.Equal(sess.t, "Link-Edit-Code42!", code)
	sess.must(sess.expect.Locator(sess.page.Locator(".copy-value").First()).ToHaveText(sess.baseURL + "/" + id))
	sess.screenshot("link-created.png", ".copy-value", ".success-subtitle")

	return id, code
}

// setLinkOptions selects the expiration and the redirect mode in the link settings.
func (sess *uiSession) setLinkOptions(expiry, redirect string) {
	sess.t.Helper()
	sess.openSettings()
	sess.selectOption("#expirySelect", expiry)
	sess.selectOption("#redirectSelect", redirect)
	sess.closeSettings()
}

func testLinkCreateEditDelete(sess *uiSession) {
	id, code := sess.createTestLink()
	original := sess.readLink(id)
	require.True(sess.t, original.BurnAfterClick)
	require.NotNil(sess.t, original.ExpiresAt)
	require.WithinDuration(sess.t, time.Now().Add(123*time.Second), *original.ExpiresAt, 10*time.Second)
	// As for a note: the result page opens the page of the link, and Edit is there.
	sess.link("Open link")
	sess.expectLinkPage(id, original.TargetURL)
	// The title has the port of the test server, and the expiry has the time of the run.
	sess.screenshot("link.png", "#linkTitle", "#expiresTag")
	sess.openEditor()
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/edit/" + id))
	sess.value("#targetInput", original.TargetURL)
	sess.value("#redirectSelect", "false")
	sess.value("#expirySelect", "keep")
	sess.must(sess.expect.Locator(sess.page.Locator("#burnCheck")).ToBeDisabled())
	sess.screenshot("link-edit.png", "#expiryHint", "#previewHost")
	sess.button("Save")
	sess.text("#linkError", "Edit code required")
	sess.openSettings()
	sess.fill("#editCodeInput", code)
	sess.closeSettings()
	sess.fill("#targetInput", "https://example.com/updated")
	sess.must(sess.page.Locator("#targetInput").Press("Control+Enter"))
	// After a save the browser shows the page of the link, as after the save of a note.
	sess.expectLinkPage(id, "https://example.com/updated")
	saved := sess.readLink(id)
	require.Equal(sess.t, "https://example.com/updated", saved.TargetURL)
	require.Equal(sess.t, original.ExpiresAt, saved.ExpiresAt)
	require.Zero(sess.t, saved.Views)
	require.True(sess.t, saved.BurnAfterClick)
	require.NotNil(sess.t, saved.DirectRedirect)
	require.False(sess.t, *saved.DirectRedirect)
	sess.editLinkWithCode(code)
	sess.setLinkOptions("never", "inherit")
	sess.button("Save")
	sess.expectLinkPage(id, "https://example.com/updated")
	sess.text("#expiresTag", "Never expires")
	saved = sess.readLink(id)
	require.Nil(sess.t, saved.ExpiresAt)
	require.Nil(sess.t, saved.DirectRedirect)
	sess.editLinkWithCode(code)
	sess.deleteLink(id)
}

// expectLinkPage checks the page of a short link: the address, the destination and the actions.
// The page does not follow the link, so the views stay the same.
func (sess *uiSession) expectLinkPage(id, target string) {
	sess.t.Helper()
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/links/" + id))
	sess.text("#linkTitle", strings.TrimPrefix(sess.baseURL, "http://")+"/"+id)
	sess.text("#targetLink", target)
	sess.must(sess.expect.Locator(sess.page.Locator("#copyLinkBtn")).ToHaveAttribute("data-url", sess.baseURL+"/"+id))
	sess.expectLayout(".header", "#linkTitle", "#targetLink", "#copyLinkBtn", "#editLinkBtn")
	sess.must(sess.expect.Locator(sess.page.Locator(".header-new")).ToHaveAttribute("href", "/links"))
}

// editLinkWithCode opens the editor from the link page and enters the edit code. After a save,
// the browser shows the link page, so each further change starts there.
func (sess *uiSession) editLinkWithCode(code string) {
	sess.t.Helper()
	sess.openEditor()
	sess.openSettings()
	sess.fill("#editCodeInput", code)
	sess.closeSettings()
}

// deleteLink deletes the link from its editor and makes sure that the API does not find it.
func (sess *uiSession) deleteLink(id string) {
	sess.t.Helper()
	sess.page.OnDialog(func(dialog playwright.Dialog) { sess.must(dialog.Accept()) })
	sess.button("Delete link")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/links"))
	deleted, err := sess.browserContext.Request().Get("/links/" + id)
	sess.must(err)
	require.Equal(sess.t, http.StatusNotFound, deleted.Status())
}

func testLinkValidation(sess *uiSession) {
	sess.gotoPage("/links")
	sess.button("Create link")
	sess.visible("#targetInput:invalid")
	sess.fill("#targetInput", "javascript:alert(1)")
	sess.button("Create link")
	sess.text("#linkError", "Use an http:// or https:// destination URL.")
	sess.fill("#targetInput", "https://example.com/")
	sess.fill("#slugInput", "invalid/slug")
	sess.button("Create link")
	sess.visible("#slugInput:invalid")
	sess.fill("#slugInput", "valid-slug")
	// The sheet stays open to the end: the main action is in view above it.
	sess.openSettings()
	sess.fill("#editCodeInput", "short")
	sess.button("Create link")
	sess.visible("#editCodeInput:invalid")
	sess.fill("#editCodeInput", "")
	sess.selectOption("#expirySelect", "seconds")
	sess.fill("#ttlInput", "0")
	sess.button("Create link")
	sess.visible("#ttlInput:invalid")
	sess.selectOption("#expirySelect", "never")
	sess.button("Create link")
	sess.createdLink()
}

func testLinkFailureRetry(sess *uiSession) {
	sess.gotoPage("/links")
	sess.fill("#targetInput", "https://example.com/keep-this")
	sess.must(sess.page.Route("**/links", func(route playwright.Route) {
		sess.must(route.Fulfill(playwright.RouteFulfillOptions{
			Status: new(503), ContentType: new("application/json"), Body: `{"message":"Please try again"}`,

			Headers: nil, Path: nil, Response: nil,
		}))
	}))
	sess.button("Create link")
	sess.text("#linkError", "Please try again")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	sess.value("#targetInput", "https://example.com/keep-this")
	sess.must(sess.page.Unroute("**/links"))
	sess.button("Create link")
	sess.createdLink()
}

func testLinkBurnAfterClick(sess *uiSession) {
	sess.gotoPage("/links")
	sess.fill("#targetInput", sess.baseURL+"/healthz")
	sess.openSettings()
	sess.click("#burnLabel")
	sess.closeSettings()
	sess.button("Create link")
	id, _ := sess.createdLink()
	metadata := sess.readLink(id)
	require.True(sess.t, metadata.BurnAfterClick)
	require.Nil(sess.t, metadata.ExpiresAt)
	// The page of the link does not burn it; only the short link /{id} does.
	sess.link("Open link")
	sess.visible("#burnBadge")
	sess.gotoPage("/links/" + id)
	require.True(sess.t, sess.readLink(id).BurnAfterClick)
	sess.gotoPage("/" + id)
	sess.visible("#continueLink")
	deleted, err := sess.browserContext.Request().Get("/links/" + id)
	sess.must(err)
	require.Equal(sess.t, http.StatusNotFound, deleted.Status())
}

func testLinkPrivacyAndOwnership(sess *uiSession) {
	sess.admin()
	sess.gotoPage("/links")
	sess.expectPrivacyOptions("public", "authenticated", "owner")
	sess.fill("#targetInput", "https://example.com/private?secret=keep-private")
	sess.openSettings()
	sess.choosePrivacy("owner")
	sess.must(sess.expect.Locator(sess.page.Locator("#privacy-owner")).ToBeChecked())
	sess.selectOption("#redirectSelect", "true")
	sess.closeSettings()
	sess.button("Create link")
	id, _ := sess.createdLink()
	metadata := sess.readLink(id)
	require.Equal(sess.t, "owner", metadata.Privacy)
	sess.link("Open link")
	sess.text(".article-title-row .badge", "Owner only")
	sess.openEditor()
	sess.must(sess.expect.Locator(sess.page.Locator(`label[for="editCodeInput"]`)).
		ToContainText("optional, you created this link"))
	sess.fill("#targetInput", "https://example.com/private-updated")
	sess.button("Save")
	sess.expectLinkPage(id, "https://example.com/private-updated")

	anonymous, err := runner.playwright.Request.NewContext(playwright.APIRequestNewContextOptions{
		BaseURL: new(sess.baseURL),

		ClientCertificates: nil, ExtraHttpHeaders: nil, FailOnStatusCode: nil,
		HttpCredentials: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil, Proxy: nil,
		StorageState: nil, StorageStatePath: nil, Timeout: nil, UserAgent: nil,
	})
	sess.must(err)

	defer anonymous.Dispose()

	// The page and the editor of a private link send a browser without a session to sign in.
	for _, path := range []string{"/links/" + id, "/edit/" + id} {
		response, err := anonymous.Get(path, playwright.APIRequestContextGetOptions{
			Headers: map[string]string{"Accept": "text/html"}, MaxRedirects: new(0),

			Data: nil, FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRetries: nil,
			Multipart: nil, Params: nil, Timeout: nil,
		})
		sess.must(err)
		require.Equal(sess.t, http.StatusSeeOther, response.Status(), path)
		require.Contains(sess.t, response.Headers()["location"], "/login?next=", path)
		body, err := response.Body()
		sess.must(err)
		require.NotContains(sess.t, string(body), "private-updated", path)
	}
}
