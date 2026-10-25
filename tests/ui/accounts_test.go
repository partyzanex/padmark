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

func testSetup(sess *uiSession) {
	sess.gotoPage("/setup")
	sess.expectLayout(".header", ".login-card", "#username", "#password", "#password_confirm",
		"#bootstrap_token", `button[type="submit"]`)
	sess.screenshot("setup.png")
	sess.button("Create account")
	sess.must(sess.expect.Locator(sess.page.Locator("#username")).ToBeFocused())
	sess.visible("#username:invalid")
	sess.fill("#username", "new-admin")
	sess.fill("#password", password)
	toggle := sess.page.Locator("#password").Locator("..").Locator("button")
	sess.must(toggle.Click())
	sess.must(sess.expect.Locator(sess.page.Locator("#password")).ToHaveAttribute("type", "text"))
	sess.value("#password", password)
	sess.must(sess.expect.Locator(toggle).ToHaveAccessibleName("Hide password"))
	sess.must(toggle.Click())
	sess.must(sess.expect.Locator(sess.page.Locator("#password")).ToHaveAttribute("type", "password"))
}

func testSetupSubmit(sess *uiSession) {
	sess.gotoPage("/setup")

	fill := func(confirmation string) {
		sess.fill("#username", "new-admin")
		sess.fill("#password", password)
		sess.fill("#password_confirm", confirmation)
		sess.fill("#bootstrap_token", bootstrapToken)
	}
	fill("Different-Password-42!")
	sess.button("Create account")
	sess.text(".login-error", "Passwords do not match.")
	fill(password)
	sess.button("Create account")
	sess.text(".login-title", "Scan QR code")
	sess.visible(`img[alt="TOTP QR code"]`)
	sess.must(sess.page.Locator(`img[alt="TOTP QR code"]`).WaitForFunction("image => image.naturalWidth > 0", nil))
	sess.link("/login")
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleButton, "Sign in")).ToBeVisible())
}

func testLogin(sess *uiSession) {
	sess.gotoPage("/login")
	sess.expectLayout(".header", ".login-card", "#username", "#password", "#code", `button[type="submit"]`)
	sess.screenshot("login.png")
	sess.fill("#username", "missing-user")
	sess.fill("#password", password)
	sess.button("Show password")
	sess.must(sess.expect.Locator(sess.page.Locator("#password")).ToHaveAttribute("type", "text"))
	sess.button("Hide password")
	sess.must(sess.expect.Locator(sess.page.Locator("#password")).ToHaveAttribute("type", "password"))
	sess.fill("#code", "abc")
	sess.button("Sign in")
	sess.visible("#code:invalid")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/login"))
	sess.fill("#code", "123456")
	sess.button("Sign in")
	sess.text(".login-error", "Invalid credentials. Please try again.")
	sess.must(sess.expect.Locator(sess.page.Locator("#username")).ToBeEditable())
}

func testAdminForms(sess *uiSession) {
	sess.admin()
	sess.text(".user-name", "ui-admin")
	sess.expectLayout(".header", ".admin-wrap", ".admin-invite-form", ".admin-key-form", "#ttl_days")
	sess.screenshot("admin.png", ".user-meta")
	sess.must(sess.browserContext.GrantPermissions([]string{"clipboard-read", "clipboard-write"}))
	sess.button("Generate invite")
	inviteLink := sess.page.Locator("#inviteLinkValue")
	sess.must(sess.expect.Locator(inviteLink).ToHaveText(regexp.MustCompile(`/setup\?invite=.+`)))
	invite, err := sess.page.Locator("#inviteLinkValue").InnerText()
	sess.must(err)
	sess.click("#inviteLinkCopyBtn")
	sess.text("#inviteLinkCopyBtn", "Copied")
	require.Equal(sess.t, invite, sess.eval("() => navigator.clipboard.readText()"))
	sess.fill("#ttl_days", "-1")
	sess.button("Create key")
	sess.visible("#ttl_days:invalid")
	sess.fill("#ttl_days", "7")
	sess.button("Create key")
	sess.must(sess.expect.Locator(sess.page.Locator("#apiKeyValue")).ToHaveText(nonempty))
	key, err := sess.page.Locator("#apiKeyValue").InnerText()
	sess.must(err)
	sess.click("#apiKeyCopyBtn")
	sess.text("#apiKeyCopyBtn", "Copied")
	require.Equal(sess.t, key, sess.eval("() => navigator.clipboard.readText()"))
	sess.click(`form[action^="/admin/api-keys/"] button`)
	noKeys := sess.page.GetByText("No API keys.", playwright.PageGetByTextOptions{Exact: new(true)})
	sess.must(sess.expect.Locator(noKeys).ToBeVisible())
	sess.must(sess.browserContext.ClearCookies())
	sess.gotoPage(invite)
	sess.must(sess.expect.Locator(sess.page.Locator("#bootstrap_token")).ToHaveCount(0))
	sess.fill("#username", "invited-user")
	sess.fill("#password", password)
	sess.fill("#password_confirm", password)
	sess.button("Create account")
	sess.visible(`img[alt="TOTP QR code"]`)
}

func testRevokeUser(sess *uiSession) {
	sess.admin()
	sess.button("Generate invite")
	invite, err := sess.page.Locator("#inviteLinkValue").InnerText()
	sess.must(err)
	inviteURL, err := url.Parse(invite)
	sess.must(err)
	response, err := sess.browserContext.Request().Post("/setup", playwright.APIRequestContextPostOptions{
		Headers: map[string]string{"Accept": "application/json"},
		Data: map[string]string{
			"username": "invited-user", "password": password, "invite": inviteURL.Query().Get("invite"),
		},
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusCreated, response.Status())
	sess.gotoPage("/admin")
	user := sess.page.Locator(".user-row").Filter(playwright.LocatorFilterOptions{HasText: "invited-user"})
	sess.must(sess.expect.Locator(user).ToBeVisible())
	sess.must(user.Locator("button").Click())
	invitedName := sess.page.GetByText("invited-user", playwright.PageGetByTextOptions{Exact: new(true)})
	sess.must(sess.expect.Locator(invitedName).ToHaveCount(0))
}

func testChangePassword(sess *uiSession) {
	secret := sess.admin()
	sess.gotoPage("/change-password")
	sess.expectLayout(".header", ".login-card", "#old_password", "#new_password", "#new_password_confirm", "#totp_code")
	sess.screenshot("change-password.png")

	newPassword := "Updated-UI-Password-84!" //nolint:gosec // G101: a test-only password
	fill := func(confirmation string) {
		sess.fill("#old_password", password)
		sess.fill("#new_password", newPassword)
		sess.fill("#new_password_confirm", confirmation)
		// Login consumed the current counter; Padmark also accepts the adjacent step.
		sess.fill("#totp_code", sess.code(secret, time.Now().Add(30*time.Second)))
	}
	fill("Different-Password-42!")
	sess.button("Change password")
	sess.text(".login-error", "New passwords do not match.")
	fill(newPassword)
	sess.button("Change password")
	sess.text(".login-success", "Password changed successfully.")
	sess.must(sess.expect.Locator(sess.page.Locator(".login-error")).ToHaveCount(0))
	sess.button("Sign out")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/login"))
	sess.must(sess.expect.Locator(sess.role(playwright.AriaRoleButton, "Sign in")).ToBeVisible())
}
