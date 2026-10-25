package ui_test

import (
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

func TestAccounts(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "Setup", accounts: true, run: testSetup},
		{name: "SetupSubmit", accounts: true, run: testSetupSubmit},
		{name: "Login", accounts: true, run: testLogin},
		{name: "AdminForms", accounts: true, run: testAdminForms},
		{name: "RevokeUser", accounts: true, run: testRevokeUser},
		{name: "ChangePassword", accounts: true, run: testChangePassword},
	})
}

func testSetup(e *ui) {
	e.gotoPage("/setup")
	e.expectLayout(".header", ".login-card", "#username", "#password", "#password_confirm", "#bootstrap_token", `button[type="submit"]`)
	e.screenshot("setup.png")
	e.button("Create account")
	e.must(e.expect.Locator(e.page.Locator("#username")).ToBeFocused())
	e.visible("#username:invalid")
	e.fill("#username", "new-admin")
	e.fill("#password", password)
	toggle := e.page.Locator("#password").Locator("..").Locator("button")
	e.must(toggle.Click())
	e.must(e.expect.Locator(e.page.Locator("#password")).ToHaveAttribute("type", "text"))
	e.value("#password", password)
	e.must(e.expect.Locator(toggle).ToHaveAccessibleName("Hide password"))
	e.must(toggle.Click())
	e.must(e.expect.Locator(e.page.Locator("#password")).ToHaveAttribute("type", "password"))
}

func testSetupSubmit(e *ui) {
	e.gotoPage("/setup")
	fill := func(confirmation string) {
		e.fill("#username", "new-admin")
		e.fill("#password", password)
		e.fill("#password_confirm", confirmation)
		e.fill("#bootstrap_token", bootstrapToken)
	}
	fill("Different-Password-42!")
	e.button("Create account")
	e.text(".login-error", "Passwords do not match.")
	fill(password)
	e.button("Create account")
	e.text(".login-title", "Scan QR code")
	e.visible(`img[alt="TOTP QR code"]`)
	e.must(e.page.Locator(`img[alt="TOTP QR code"]`).WaitForFunction("image => image.naturalWidth > 0", nil))
	e.link("/login")
	e.must(e.expect.Locator(e.role(playwright.AriaRoleButton, "Sign in")).ToBeVisible())
}

func testLogin(e *ui) {
	e.gotoPage("/login")
	e.expectLayout(".header", ".login-card", "#username", "#password", "#code", `button[type="submit"]`)
	e.screenshot("login.png")
	e.fill("#username", "missing-user")
	e.fill("#password", password)
	e.button("Show password")
	e.must(e.expect.Locator(e.page.Locator("#password")).ToHaveAttribute("type", "text"))
	e.button("Hide password")
	e.must(e.expect.Locator(e.page.Locator("#password")).ToHaveAttribute("type", "password"))
	e.fill("#code", "abc")
	e.button("Sign in")
	e.visible("#code:invalid")
	e.must(e.expect.Page(e.page).ToHaveURL(e.baseURL + "/login"))
	e.fill("#code", "123456")
	e.button("Sign in")
	e.text(".login-error", "Invalid credentials. Please try again.")
	e.must(e.expect.Locator(e.page.Locator("#username")).ToBeEditable())
}

func testAdminForms(e *ui) {
	e.admin()
	e.text(".user-name", "ui-admin")
	e.expectLayout(".header", ".admin-wrap", ".admin-invite-form", ".admin-key-form", "#ttl_days")
	e.screenshot("admin.png", ".user-meta")
	e.must(e.browserContext.GrantPermissions([]string{"clipboard-read", "clipboard-write"}))
	e.button("Generate invite")
	e.must(e.expect.Locator(e.page.Locator("#inviteLinkValue")).ToHaveText(regexp.MustCompile(`/setup\?invite=.+`)))
	invite, err := e.page.Locator("#inviteLinkValue").InnerText()
	e.must(err)
	e.click("#inviteLinkCopyBtn")
	e.text("#inviteLinkCopyBtn", "Copied")
	require.Equal(e.t, invite, e.eval("() => navigator.clipboard.readText()"))
	e.fill("#ttl_days", "-1")
	e.button("Create key")
	e.visible("#ttl_days:invalid")
	e.fill("#ttl_days", "7")
	e.button("Create key")
	e.must(e.expect.Locator(e.page.Locator("#apiKeyValue")).ToHaveText(nonempty))
	key, err := e.page.Locator("#apiKeyValue").InnerText()
	e.must(err)
	e.click("#apiKeyCopyBtn")
	e.text("#apiKeyCopyBtn", "Copied")
	require.Equal(e.t, key, e.eval("() => navigator.clipboard.readText()"))
	e.click(`form[action^="/admin/api-keys/"] button`)
	e.must(e.expect.Locator(e.page.GetByText("No API keys.", playwright.PageGetByTextOptions{Exact: playwright.Bool(true)})).ToBeVisible())
	e.must(e.browserContext.ClearCookies())
	e.gotoPage(invite)
	e.must(e.expect.Locator(e.page.Locator("#bootstrap_token")).ToHaveCount(0))
	e.fill("#username", "invited-user")
	e.fill("#password", password)
	e.fill("#password_confirm", password)
	e.button("Create account")
	e.visible(`img[alt="TOTP QR code"]`)
}

func testRevokeUser(e *ui) {
	e.admin()
	e.button("Generate invite")
	invite, err := e.page.Locator("#inviteLinkValue").InnerText()
	e.must(err)
	inviteURL, err := url.Parse(invite)
	e.must(err)
	response, err := e.browserContext.Request().Post("/setup", playwright.APIRequestContextPostOptions{
		Headers: map[string]string{"Accept": "application/json"},
		Data:    map[string]string{"username": "invited-user", "password": password, "invite": inviteURL.Query().Get("invite")},
	})
	e.must(err)
	require.Equal(e.t, http.StatusCreated, response.Status())
	e.gotoPage("/admin")
	user := e.page.Locator(".user-row").Filter(playwright.LocatorFilterOptions{HasText: "invited-user"})
	e.must(e.expect.Locator(user).ToBeVisible())
	e.must(user.Locator("button").Click())
	e.must(e.expect.Locator(e.page.GetByText("invited-user", playwright.PageGetByTextOptions{Exact: playwright.Bool(true)})).ToHaveCount(0))
}

func testChangePassword(e *ui) {
	secret := e.admin()
	e.gotoPage("/change-password")
	e.expectLayout(".header", ".login-card", "#old_password", "#new_password", "#new_password_confirm", "#totp_code")
	e.screenshot("change-password.png")
	newPassword := "Updated-UI-Password-84!"
	fill := func(confirmation string) {
		e.fill("#old_password", password)
		e.fill("#new_password", newPassword)
		e.fill("#new_password_confirm", confirmation)
		// Login consumed the current counter; Padmark also accepts the adjacent step.
		e.fill("#totp_code", e.code(secret, time.Now().Add(30*time.Second)))
	}
	fill("Different-Password-42!")
	e.button("Change password")
	e.text(".login-error", "New passwords do not match.")
	fill(newPassword)
	e.button("Change password")
	e.text(".login-success", "Password changed successfully.")
	e.must(e.expect.Locator(e.page.Locator(".login-error")).ToHaveCount(0))
	e.button("Sign out")
	e.must(e.expect.Page(e.page).ToHaveURL(e.baseURL + "/login"))
	e.must(e.expect.Locator(e.role(playwright.AriaRoleButton, "Sign in")).ToBeVisible())
}
