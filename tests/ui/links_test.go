package ui_test

import (
	"net/http"
	"net/url"
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
		{name: "CreateEditDelete", run: testLinkCreateEditDelete, accounts: false, allowedStatuses: nil},
		{name: "Validation", run: testLinkValidation, accounts: false, allowedStatuses: nil},
		{name: "FailureRetry", allowedStatuses: []int{503}, run: testLinkFailureRetry, accounts: false},
		{name: "BurnAfterClick", allowedStatuses: []int{404}, run: testLinkBurnAfterClick, accounts: false},
		{name: "PrivacyAndOwnership", accounts: true, run: testLinkPrivacyAndOwnership, allowedStatuses: nil},
	})
}

func (sess *uiSession) createdLink() (string, string) {
	sess.t.Helper()
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.text(".success-title", "Link created")
	current, err := url.Parse(sess.page.URL())
	sess.must(err)

	id := current.Query().Get("id")
	require.NotEmpty(sess.t, id)
	sess.must(sess.expect.Locator(sess.page.Locator("#editCodeValue")).ToHaveText(nonempty))
	code, err := sess.page.Locator("#editCodeValue").InnerText()
	sess.must(err)

	return id, code
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
	sess.link("Links")
	sess.expectPrivacyOptions("public")
	sess.fill("#targetInput", "https://example.com/original?value=1")
	sess.fill("#slugInput", "ui-link")
	sess.fill("#editCodeInput", "Link-Edit-Code42!")
	sess.selectOption("#expirySelect", "seconds")
	sess.fill("#ttlInput", "123")
	sess.selectOption("#redirectSelect", "false")
	sess.click("#burnLabel")
	sess.expectLayout(".header", "#targetInput", "#slugInput", "#editCodeInput", "#privacySelect",
		"#redirectSelect", "#ttlInput", "#burnLabel", "#submitBtn")
	sess.screenshot("link-editor.png")
	sess.button("Create link")
	id, code := sess.createdLink()
	require.Equal(sess.t, "ui-link", id)
	require.Equal(sess.t, "Link-Edit-Code42!", code)
	sess.must(sess.expect.Locator(sess.page.Locator(".copy-value").First()).ToHaveText(sess.baseURL + "/" + id))
	sess.screenshot("link-created.png", ".copy-value", ".success-subtitle")

	return id, code
}

func testLinkCreateEditDelete(sess *uiSession) {
	id, code := sess.createTestLink()
	original := sess.readLink(id)
	require.True(sess.t, original.BurnAfterClick)
	require.NotNil(sess.t, original.ExpiresAt)
	require.WithinDuration(sess.t, time.Now().Add(123*time.Second), *original.ExpiresAt, 10*time.Second)
	sess.link("Edit link")
	sess.value("#targetInput", original.TargetURL)
	sess.value("#redirectSelect", "false")
	sess.value("#expirySelect", "keep")
	sess.must(sess.expect.Locator(sess.page.Locator("#burnCheck")).ToBeDisabled())
	sess.screenshot("link-edit.png", "#expiryHint")
	sess.button("Save link")
	sess.text("#linkError", "Edit code required")
	sess.fill("#editCodeInput", code)
	sess.fill("#targetInput", "https://example.com/updated")
	sess.must(sess.page.Locator("#targetInput").Press("Control+Enter"))
	sess.text("#linkStatus", "Link saved")
	saved := sess.readLink(id)
	require.Equal(sess.t, "https://example.com/updated", saved.TargetURL)
	require.Equal(sess.t, original.ExpiresAt, saved.ExpiresAt)
	require.Zero(sess.t, saved.Views)
	require.True(sess.t, saved.BurnAfterClick)
	require.NotNil(sess.t, saved.DirectRedirect)
	require.False(sess.t, *saved.DirectRedirect)
	sess.selectOption("#expirySelect", "never")
	sess.selectOption("#redirectSelect", "inherit")
	sess.button("Save link")
	sess.text("#linkStatus", "Link saved")
	saved = sess.readLink(id)
	require.Nil(sess.t, saved.ExpiresAt)
	require.Nil(sess.t, saved.DirectRedirect)
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
	sess.click("#burnLabel")
	sess.button("Create link")
	id, _ := sess.createdLink()
	metadata := sess.readLink(id)
	require.True(sess.t, metadata.BurnAfterClick)
	require.Nil(sess.t, metadata.ExpiresAt)
	sess.link("Open link")
	sess.visible("#continueLink")
	deleted, err := sess.browserContext.Request().Get("/links/" + id)
	sess.must(err)
	require.Equal(sess.t, http.StatusNotFound, deleted.Status())
}

func testLinkPrivacyAndOwnership(sess *uiSession) {
	sess.admin()
	sess.gotoPage("/links")
	sess.expectPrivacyOptions("public", "authenticated", "owner")
	sess.selectOption("#privacySelect", "owner")
	sess.text("#privacyHint", "Only you can follow")
	sess.fill("#targetInput", "https://example.com/private?secret=keep-private")
	sess.selectOption("#redirectSelect", "true")
	sess.button("Create link")
	id, _ := sess.createdLink()
	metadata := sess.readLink(id)
	require.Equal(sess.t, "owner", metadata.Privacy)
	sess.link("Edit link")
	sess.fill("#targetInput", "https://example.com/private-updated")
	sess.button("Save link")
	sess.text("#linkStatus", "Link saved")

	anonymous, err := runner.playwright.Request.NewContext(playwright.APIRequestNewContextOptions{
		BaseURL: new(sess.baseURL),

		ClientCertificates: nil, ExtraHttpHeaders: nil, FailOnStatusCode: nil,
		HttpCredentials: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil, Proxy: nil,
		StorageState: nil, StorageStatePath: nil, Timeout: nil, UserAgent: nil,
	})
	sess.must(err)

	defer anonymous.Dispose()

	response, err := anonymous.Get("/links/"+id+"/edit", playwright.APIRequestContextGetOptions{
		Headers: map[string]string{"Accept": "text/html"}, MaxRedirects: new(0),

		Data: nil, FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRetries: nil,
		Multipart: nil, Params: nil, Timeout: nil,
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusSeeOther, response.Status())
	require.Contains(sess.t, response.Headers()["location"], "/login?next=")
	body, err := response.Body()
	sess.must(err)
	require.NotContains(sess.t, string(body), "private-updated")
}
