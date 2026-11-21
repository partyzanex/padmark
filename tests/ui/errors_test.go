package ui_test

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

// The messages of the server that the forms show without a change.
const (
	slugTakenMessage    = "slug is already taken"
	invalidSlugMessage  = "invalid slug"
	titleTooLongMessage = "title exceeds maximum length"
	wrongEditCode       = "Wrong-Edit-Code-42!"
	weakPasswordMessage = "Password must be at least 12 characters with upper, lower, digit, and special character."
	otherPassword       = "Different-Password-42!"
)

// pageStatuses are the statuses of the HTML error pages.
func pageStatuses() []int {
	return []int{http.StatusForbidden, http.StatusNotFound, http.StatusGone}
}

// TestErrors checks each error that the web interface shows. Each check makes sure that the
// user can read the error on the page: the text is correct, the element is in the viewport and
// the text is not clipped. The user must not need the browser network log to find the cause.
func TestErrors(t *testing.T) {
	rejections := []int{
		http.StatusForbidden, http.StatusConflict, http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity, http.StatusInternalServerError, http.StatusServiceUnavailable,
	}

	runScenarios(t, []scenario{
		{name: "NoteEditor", run: testNoteEditorErrors, accounts: false, files: false, allowedStatuses: rejections},
		{name: "NoteEdit", run: testNoteEditErrors, accounts: false, files: false, allowedStatuses: rejections},
		{name: "NoteServerReplies", run: testNoteServerReplies, accounts: false, files: false, allowedStatuses: rejections},
		{name: "LinkEditor", run: testLinkEditorErrors, accounts: false, files: false, allowedStatuses: rejections},
		{name: "LinkEdit", run: testLinkEditErrors, accounts: false, files: false, allowedStatuses: rejections},
		{name: "LinkServerReplies", run: testLinkServerReplies, accounts: false, files: false, allowedStatuses: rejections},
		{name: "ErrorPages", run: testErrorPages, accounts: false, files: false, allowedStatuses: pageStatuses()},
		{name: "Setup", run: testSetupErrors, accounts: true, files: false, allowedStatuses: pageStatuses()},
		{name: "Login", run: testLoginErrors, accounts: true, files: false, allowedStatuses: nil},
		{name: "Invite", run: testInviteErrors, accounts: true, files: false, allowedStatuses: nil},
		{name: "ChangePassword", run: testChangePasswordErrors, accounts: true, files: false, allowedStatuses: nil},
		{name: "Admin", run: testAdminErrors, accounts: true, files: false, allowedStatuses: nil},
		{name: "AccessPages", run: testAccessErrorPages, accounts: true, files: false, allowedStatuses: pageStatuses()},
		{name: "FileEditor", run: testFileEditorErrors, files: true, accounts: false, allowedStatuses: rejections},
		{
			name: "FileEdit", run: testFileEditErrors, files: true, accounts: false,
			allowedStatuses: append(slices.Clone(rejections), http.StatusUnauthorized, http.StatusNotFound, http.StatusGone),
		},
		{name: "FileErrorPages", run: testFileErrorPages, files: true, accounts: false, allowedStatuses: pageStatuses()},
		{
			name: "FilesOff", run: testFilesOff, accounts: false, files: false,
			allowedStatuses: []int{http.StatusServiceUnavailable},
		},
	})
}

// expectError checks that the page shows the error text in full: the element has the text, is
// in the viewport, is inside the screen width and does not clip its content.
func (sess *uiSession) expectError(selector, text string) {
	sess.t.Helper()
	sess.text(selector, text)
	sess.expectReadable(selector)
}

// expectErrorContains is expectError for a message that has a variable part.
func (sess *uiSession) expectErrorContains(selector, part string) {
	sess.t.Helper()
	sess.must(sess.expect.Locator(sess.page.Locator(selector)).ToContainText(part))
	sess.expectReadable(selector)
}

// expectReadable checks that the element is in the viewport, is inside the screen width and
// does not clip its content.
func (sess *uiSession) expectReadable(selector string) {
	sess.t.Helper()
	sess.expectLayout(selector)

	locator := sess.page.Locator(selector)
	sess.must(sess.expect.Locator(locator).ToBeInViewport())

	// The text must not be wider than its element, and an element that hides its overflow must
	// not hold more lines than it shows. A heading with a small line height has glyphs that are
	// taller than the line box; that is not a clipped text.
	clipped, err := locator.Evaluate(`(box) => {
		const content = document.createRange();
		content.selectNodeContents(box);
		const text = content.getBoundingClientRect();
		const own = box.getBoundingClientRect();
		const wide = text.left < own.left - 1 || text.right > own.right + 1;
		const cut = getComputedStyle(box).overflowY !== "visible" && box.scrollHeight > box.clientHeight + 1;
		return wide || cut;
	}`, nil)
	sess.must(err)
	require.Equal(sess.t, false, clipped, "%s shows its full text", selector)
}

// createNote makes a note with the given slug through the API.
func (sess *uiSession) createNote(slug string) {
	sess.t.Helper()
	sess.post("/notes", map[string]any{"content": "A note that holds the slug", "slug": slug})
}

// post sends a JSON request that must make a resource.
func (sess *uiSession) post(path string, data map[string]any) {
	sess.t.Helper()
	response, err := sess.browserContext.Request().Post(path, playwright.APIRequestContextPostOptions{
		Headers: map[string]string{"Accept": "application/json"}, Data: data,

		FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil,
		MaxRetries: nil, Multipart: nil, Params: nil, Timeout: nil,
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusCreated, response.Status())
}

// testNoteEditorErrors: the errors of a new note, from the browser checks and from the server.
func testNoteEditorErrors(sess *uiSession) {
	sess.createNote("taken-note")
	sess.gotoPage("/")

	sess.button("Publish note")
	sess.expectError("#noteError", "Enter some text first")

	sess.fill("#editor", "Keep this draft")
	sess.openSettings()
	sess.fill("#editCodeInput", "short")
	sess.closeSettings()
	sess.button("Publish note")
	sess.expectError("#noteError", "Edit code: 8 characters or more")
	sess.openSettings()
	sess.fill("#editCodeInput", "")
	sess.closeSettings()

	sess.fill("#slugInput", "taken-note")
	sess.button("Publish note")
	sess.expectError("#noteError", slugTakenMessage)

	sess.fill("#slugInput", "not a slug!")
	sess.button("Publish note")
	sess.expectErrorContains("#noteError", invalidSlugMessage)

	// A reserved first segment is a route name of the server.
	sess.fill("#slugInput", "login")
	sess.button("Publish note")
	sess.expectErrorContains("#noteError", invalidSlugMessage)

	// The title field stops at the server limit; without the stop the server refuses the title.
	sess.eval(`() => document.getElementById("titleInput").removeAttribute("maxlength")`)
	sess.fill("#slugInput", "free-note")
	sess.fill("#titleInput", strings.Repeat("t", 501))
	sess.button("Publish note")
	sess.expectError("#noteError", titleTooLongMessage)
	sess.fill("#titleInput", "A short title")

	sess.value("#editor", "Keep this draft")
	sess.text("#submitBtn", "Publish note")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())

	// A correct attempt removes the error and publishes the note.
	sess.fill("#slugInput", "free-note")
	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
}

// testNoteEditErrors: the errors of the edit form of a note.
func testNoteEditErrors(sess *uiSession) {
	sess.publishNote()
	sess.link("Open note")
	sess.openEditor()

	sess.button("Save")
	sess.expectError("#noteError", "Edit code required")

	sess.fill("#editor", "   ")
	sess.button("Save")
	sess.expectError("#noteError", "Enter some text first")

	sess.fill("#editor", "Unsaved draft")
	sess.openSettings()
	sess.fill("#editCodeInput", wrongEditCode)
	sess.closeSettings()
	sess.button("Save")
	sess.expectError("#noteError", "Invalid edit code")
	sess.value("#editor", "Unsaved draft")
	sess.text("#submitBtn", "Save")

	// "Delete note" checks the edit code in the same way.
	sess.page.OnDialog(func(dialog playwright.Dialog) { sess.must(dialog.Accept()) })
	sess.button("Delete note")
	sess.expectError("#noteError", "Invalid edit code")
	sess.must(sess.expect.Locator(sess.page.Locator("#deleteBtn")).ToBeEnabled())
	sess.openSettings()
	sess.fill("#editCodeInput", "")
	sess.closeSettings()
	sess.button("Delete note")
	sess.expectError("#noteError", "Edit code required")
}

// serverReply is a simulated reply to a form request and the error text that the form must show.
type serverReply struct {
	name        string
	status      int
	contentType string
	body        string
	// abort drops the request without a reply, as a broken connection does.
	abort bool
	// want is the full error text; an empty value accepts each text that is not empty.
	want string
}

// serverReplies are the kinds of failed replies that a form can get.
func serverReplies(fallback string) []serverReply {
	const jsonType = "application/json"

	longSentence := strings.TrimSpace(strings.Repeat("a reason of many words ", 12))

	return []serverReply{
		{
			name: "validation", status: http.StatusUnprocessableEntity, contentType: jsonType,
			body:  `{"message":"title exceeds maximum length"}`,
			abort: false, want: titleTooLongMessage,
		},
		{
			name: "long sentence", status: http.StatusUnprocessableEntity, contentType: jsonType,
			body:  `{"message":"` + longSentence + `"}`,
			abort: false, want: longSentence,
		},
		{
			name: "conflict", status: http.StatusConflict, contentType: jsonType,
			body: `{"message":"slug is already taken"}`, abort: false, want: slugTakenMessage,
		},
		{
			name: "too large", status: http.StatusRequestEntityTooLarge, contentType: jsonType,
			body: `{"message":"request body too large"}`, abort: false, want: "request body too large",
		},
		{
			name: "cross-origin", status: http.StatusForbidden, contentType: jsonType,
			body:  `{"message":"cross-origin request rejected"}`,
			abort: false, want: "cross-origin request rejected",
		},
		{
			name: "API disabled", status: http.StatusServiceUnavailable, contentType: jsonType,
			body: `{"message":"the API is disabled"}`, abort: false, want: "the API is disabled",
		},
		{
			name: "long word", status: http.StatusUnprocessableEntity, contentType: jsonType,
			body:  `{"message":"` + strings.Repeat("unbroken", 30) + `"}`,
			abort: false, want: strings.Repeat("unbroken", 30),
		},
		{
			name: "not JSON", status: http.StatusInternalServerError, contentType: "text/plain",
			body: "upstream failure", abort: false, want: fallback,
		},
		{
			name: "empty message", status: http.StatusInternalServerError, contentType: jsonType,
			body: `{"message":""}`, abort: false, want: fallback,
		},
		{name: "no connection", status: 0, contentType: "", body: "", abort: true, want: ""},
	}
}

// simulate makes the server path answer with the reply until the next simulate or Unroute call.
func (sess *uiSession) simulate(pattern string, reply serverReply) {
	sess.t.Helper()
	sess.must(sess.page.Unroute(pattern))
	sess.must(sess.page.Route(pattern, func(route playwright.Route) {
		var err error
		if reply.abort {
			err = route.Abort("aborted")
		} else {
			err = route.Fulfill(playwright.RouteFulfillOptions{
				Status: new(reply.status), ContentType: new(reply.contentType), Body: reply.body,

				Headers: nil, Path: nil, Response: nil,
			})
		}

		if err != nil {
			sess.errorf("simulate %s: %v", reply.name, err)
		}
	}))
}

// expectReply checks the error text of a simulated reply.
func (sess *uiSession) expectReply(selector string, reply serverReply) {
	sess.t.Helper()

	if reply.want == "" {
		sess.must(sess.expect.Locator(sess.page.Locator(selector)).ToHaveText(nonempty))
		sess.expectReadable(selector)

		return
	}

	sess.expectError(selector, reply.want)
}

// testNoteServerReplies: the note forms show the reason of each failed reply.
func testNoteServerReplies(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#editor", "Keep this draft")

	for _, reply := range serverReplies("Error — try again") {
		sess.simulate("**/notes", reply)
		sess.button("Publish note")
		sess.expectReply("#noteError", reply)
		sess.text("#submitBtn", "Publish note")
		sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
		sess.value("#editor", "Keep this draft")
	}

	sess.must(sess.page.Unroute("**/notes"))
	code, noteURL := sess.publishNote()
	parsed, err := url.Parse(noteURL)
	sess.must(err)
	sess.gotoPage("/edit" + parsed.Path)
	sess.openSettings()
	sess.fill("#editCodeInput", code)
	sess.closeSettings()

	for _, reply := range serverReplies("HTTP 500") {
		sess.simulate("**/notes/*", reply)
		sess.button("Save")
		sess.expectReply("#noteError", reply)
		sess.text("#submitBtn", "Save")
		sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	}
}

// testLinkEditorErrors: the errors of a new short link.
func testLinkEditorErrors(sess *uiSession) {
	sess.post("/links", map[string]any{"target_url": "https://example.com/first", "slug": "taken-link"})
	sess.gotoPage("/links")

	sess.fill("#targetInput", "javascript:alert(1)")
	sess.button("Create link")
	sess.expectError("#linkError", "Use an http:// or https:// destination URL.")

	sess.fill("#targetInput", "https://example.com/second")
	sess.fill("#slugInput", "taken-link")
	sess.button("Create link")
	sess.expectError("#linkError", slugTakenMessage)

	sess.fill("#slugInput", "login")
	sess.button("Create link")
	sess.expectErrorContains("#linkError", invalidSlugMessage)
	sess.value("#targetInput", "https://example.com/second")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())

	sess.fill("#slugInput", "free-link")
	sess.button("Create link")
	sess.createdLink()
}

// testLinkEditErrors: the errors of the edit form of a short link.
func testLinkEditErrors(sess *uiSession) {
	sess.gotoPage("/links")
	sess.fill("#targetInput", "https://example.com/edit-errors")
	sess.button("Create link")
	sess.createdLink()
	sess.link("Open link")
	sess.openEditor()
	sess.page.OnDialog(func(dialog playwright.Dialog) { sess.must(dialog.Accept()) })

	sess.button("Save")
	sess.expectError("#linkError", "Edit code required")

	sess.button("Delete link")
	sess.expectError("#linkError", "Edit code required")

	sess.openSettings()
	sess.fill("#editCodeInput", wrongEditCode)
	sess.closeSettings()
	sess.button("Save")
	sess.expectError("#linkError", "invalid edit code")

	sess.button("Delete link")
	sess.expectError("#linkError", "invalid edit code")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	sess.must(sess.expect.Locator(sess.page.Locator("#deleteBtn")).ToBeEnabled())
}

// testLinkServerReplies: the link form shows the reason of each failed reply.
func testLinkServerReplies(sess *uiSession) {
	sess.gotoPage("/links")
	sess.fill("#targetInput", "https://example.com/keep-this")

	for _, reply := range serverReplies("Unable to save the link. Please try again.") {
		sess.simulate("**/links", reply)
		sess.button("Create link")
		sess.expectReply("#linkError", reply)
		sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
		sess.value("#targetInput", "https://example.com/keep-this")
	}
}

// expectErrorPage checks the error page: the code, the title, the description and the actions.
func (sess *uiSession) expectErrorPage(code, title, description string) {
	sess.t.Helper()
	sess.expectError(".error-code", code)
	sess.expectError(".error-title", title)
	sess.expectError(".error-desc", description)
	sess.expectReadable(".error-actions")
}

const (
	notFoundTitle = "Paste not found"
	notFoundDesc  = "The paste you're looking for doesn't exist, has expired, or was deleted."
	expiredTitle  = "Paste expired"
	expiredDesc   = "This paste had a limited lifetime and has been automatically deleted after expiration."

	// expiryWait and expiryPoll bound the wait for a resource with a lifetime of one second.
	expiryWait = 10 * time.Second
	expiryPoll = 200 * time.Millisecond
)

// testErrorPages: the error pages of the note and short-link routes without accounts.
func testErrorPages(sess *uiSession) {
	for _, path := range []string{"/missing-ui-note", "/edit/missing-ui-note", "/links/missing-ui-link"} {
		sess.gotoPage(path)
		sess.expectErrorPage("404", notFoundTitle, notFoundDesc)
	}

	// A burn-after-reading note is gone after the first read.
	sess.post("/notes", map[string]any{"content": "Read once", "slug": "burned-note", "burn_after_reading": true})
	sess.gotoPage("/burned-note")
	sess.button("Reveal and burn")
	sess.must(sess.expect.Locator(sess.page.Locator(".markdown-body")).ToContainText("Read once"))
	sess.gotoPage("/burned-note")
	sess.expectErrorPage("404", notFoundTitle, notFoundDesc)

	// A note and a short link with a lifetime of one second.
	sess.post("/notes", map[string]any{"content": "Short life", "slug": "short-note", "ttl": 1})
	sess.post("/links", map[string]any{"target_url": "https://example.com/short", "slug": "short-link", "ttl": 1})
	sess.post("/links", map[string]any{"target_url": "https://example.com/short", "slug": "short-edit", "ttl": 1})

	for _, path := range []string{"/short-note", "/short-link", "/links/short-edit", "/edit/short-edit"} {
		sess.awaitErrorCode(path, "410")
		sess.expectErrorPage("410", expiredTitle, expiredDesc)
	}
}

// awaitErrorCode opens the path again and again until the page shows the error code. It is for
// a resource that expires a short time after its creation.
func (sess *uiSession) awaitErrorCode(path, code string) {
	sess.t.Helper()
	require.Eventually(sess.t, func() bool {
		sess.gotoPage(path)

		shown := sess.page.Locator(".error-code")

		count, err := shown.Count()
		if err != nil || count == 0 {
			return false
		}

		text, err := shown.InnerText()

		return err == nil && strings.TrimSpace(text) == code
	}, expiryWait, expiryPoll, "%s shows the error code %s", path, code)
}

// fillSetup fills the account form of the setup page; an empty token leaves the field alone.
func (sess *uiSession) fillSetup(username, pass, confirmation, token string) {
	sess.t.Helper()
	sess.fill("#username", username)
	sess.fill("#password", pass)
	sess.fill("#password_confirm", confirmation)

	if token != "" {
		sess.fill("#bootstrap_token", token)
	}

	sess.button("Create account")
}

// testSetupErrors: the errors of the first-admin form and the page of a closed setup.
func testSetupErrors(sess *uiSession) {
	sess.gotoPage("/setup")

	sess.fillSetup("new-admin", password, otherPassword, bootstrapToken)
	sess.expectError(".login-error", "Passwords do not match.")

	sess.fillSetup("new-admin", "weak", "weak", bootstrapToken)
	sess.expectError(".login-error", weakPasswordMessage)

	sess.fillSetup("not a name!", password, password, bootstrapToken)
	sess.expectErrorContains(".login-error", "username must be")

	sess.fillSetup("new-admin", password, password, "wrong-bootstrap-token-0123456789abcdef")
	sess.expectError(".login-error", "Invalid bootstrap token.")

	sess.admin()
	sess.must(sess.browserContext.ClearCookies())
	sess.gotoPage("/setup")
	sess.expectErrorPage("403", "Setup is closed",
		"The first admin already exists. New accounts can only be created via an invite link.")
}

// testLoginErrors: the sign-in form shows one message for each refused attempt.
func testLoginErrors(sess *uiSession) {
	sess.gotoPage("/login")
	sess.fill("#username", "missing-user")
	sess.fill("#password", password)
	sess.fill("#code", "123456")
	sess.button("Sign in")
	sess.expectError(".login-error", "That didn't work. Check all three fields — the code changes every 30 seconds.")
}

// inviteToken makes an invite link in the admin panel and returns its token.
func (sess *uiSession) inviteToken() string {
	sess.t.Helper()
	sess.gotoPage("/admin")
	sess.button("Invite user")
	invite, err := sess.page.Locator("#inviteLinkValue").InnerText()
	sess.must(err)
	parsed, err := url.Parse(invite)
	sess.must(err)

	token := parsed.Query().Get("invite")
	require.NotEmpty(sess.t, token)

	return token
}

// register makes an account from an invite token through the API. It returns the TOTP secret.
func (sess *uiSession) register(username, token string) string {
	sess.t.Helper()
	response, err := sess.browserContext.Request().Post("/setup", playwright.APIRequestContextPostOptions{
		Headers: map[string]string{"Accept": "application/json"},
		Data:    map[string]string{"username": username, "password": password, "invite": token},

		FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil,
		MaxRetries: nil, Multipart: nil, Params: nil, Timeout: nil,
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusCreated, response.Status())

	var body struct {
		URI string `json:"otp_auth_uri"`
	}
	sess.must(response.JSON(&body))
	enrollment, err := url.Parse(body.URI)
	sess.must(err)

	secret := enrollment.Query().Get("secret")
	require.NotEmpty(sess.t, secret)

	return secret
}

// signIn replaces the current session with a session of the given account.
func (sess *uiSession) signIn(username, secret string) {
	sess.t.Helper()
	sess.must(sess.browserContext.ClearCookies())
	sess.gotoPage("/login")
	sess.fill("#username", username)
	sess.fill("#password", password)
	sess.fill("#code", sess.code(secret, time.Now()))
	sess.button("Sign in")
	sess.must(sess.expect.Page(sess.page).Not().ToHaveURL(regexpURL("/login")))
}

// testInviteErrors: the errors of the invite form.
func testInviteErrors(sess *uiSession) {
	sess.admin()
	token := sess.inviteToken()
	sess.must(sess.browserContext.ClearCookies())
	sess.gotoPage("/setup?invite=" + token)

	sess.fillSetup("ui-admin", password, password, "")
	sess.expectError(".login-error", "Username is already taken.")

	sess.fillSetup("invited-user", password, otherPassword, "")
	sess.expectError(".login-error", "Passwords do not match.")

	sess.fillSetup("invited-user", "weak", "weak", "")
	sess.expectError(".login-error", weakPasswordMessage)

	sess.fillSetup("invited-user", password, password, "")
	sess.visible(`img[alt="TOTP QR code"]`)

	// The invite link works one time.
	sess.gotoPage("/setup?invite=" + token)
	sess.fillSetup("second-user", password, password, "")
	sess.expectError(".login-error", "Invite link has already been used.")
}

// testChangePasswordErrors: the errors of the change-password form.
func testChangePasswordErrors(sess *uiSession) {
	secret := sess.admin()
	sess.gotoPage("/change-password")

	newPassword := "Updated-UI-Password-84!" //nolint:gosec // G101: a test-only password
	submit := func(current, next, confirmation, code string) {
		sess.fill("#old_password", current)
		sess.fill("#new_password", next)
		sess.fill("#new_password_confirm", confirmation)
		sess.fill("#totp_code", code)
		sess.button("Change password")
	}
	// Login consumed the current counter; Padmark also accepts the adjacent step.
	nextCode := func() string { return sess.code(secret, time.Now().Add(30*time.Second)) }

	submit(password, newPassword, otherPassword, nextCode())
	sess.expectError(".login-error", "New passwords do not match.")

	submit(password, "weak", "weak", nextCode())
	sess.expectError(".login-error", weakPasswordMessage)

	submit(otherPassword, newPassword, newPassword, nextCode())
	sess.expectError(".login-error", "Current password is incorrect.")

	submit(password, newPassword, newPassword, "000000")
	sess.expectError(".login-error", "TOTP code is invalid or has expired.")
}

// testAdminErrors: the errors of the admin panel.
func testAdminErrors(sess *uiSession) {
	sess.admin()

	// The lifetime field stops at the server limits; without the stops the server refuses the value.
	sess.eval(`() => { const field = document.getElementById("ttl_days"); field.removeAttribute("max"); }`)
	sess.fill("#ttl_days", "99999")
	sess.button("Create key")
	sess.expectError(".msg-err", "Lifetime must be 0 (never expires) to 3650 days.")

	// The revoke handlers send their errors to the panel in the address of the redirect.
	for _, message := range []string{
		"Cannot revoke: self-revoke or last admin.", "Failed to revoke user.",
	} {
		sess.gotoPage("/admin?revoke_error=" + url.QueryEscape(message))
		sess.expectError(".msg-err", message)
	}

	sess.gotoPage("/admin?key_error=" + url.QueryEscape("Failed to revoke API key."))
	sess.expectError(".msg-err", "Failed to revoke API key.")

	// An account holds 20 API keys at most.
	sess.gotoPage("/admin")

	for range maxAPIKeys {
		sess.button("Create key")
		sess.must(sess.expect.Locator(sess.page.Locator("#apiKeyValue")).ToHaveText(nonempty))
	}

	sess.button("Create key")
	sess.expectError(".msg-err", "This user already holds the maximum number of API keys; revoke one first.")
}

// maxAPIKeys is the server limit of live API keys for one account.
const maxAPIKeys = 20

// testAccessErrorPages: the pages that refuse a signed-in user.
func testAccessErrorPages(sess *uiSession) {
	sess.admin()
	sess.post("/notes", map[string]any{"content": "Owner only", "slug": "owner-note", "privacy": "owner"})
	secret := sess.register("invited-user", sess.inviteToken())
	sess.signIn("invited-user", secret)

	sess.gotoPage("/owner-note")
	sess.expectErrorPage("403", "Not yours",
		"This is private to another account. Signing in again won't change that.")

	sess.gotoPage("/admin")
	sess.expectErrorPage("403", "Forbidden", "You don't have permission to perform this action.")
}

// The texts of #fileError on the upload and edit pages of a file.
const (
	fileTooLargeMessage = "The file is larger than the server limit (1 MiB)."
	fileSlugTaken       = "This slug is already taken. Choose another slug."
	fileStorageMessage  = "File storage is unavailable. Try again later."
	filesOffMessage     = "File publishing is turned off on this server."
	fileNetworkMessage  = "The connection failed. Check the network and try again."
	fileWrongEditCode   = "The edit code is not correct."
)

// fileReply is a simulated reply with a JSON body for the file forms.
func fileReply(name string, status int, message, want string) serverReply {
	return serverReply{
		name: name, status: status, contentType: "application/json",
		body: `{"message":"` + message + `"}`, abort: false, want: want,
	}
}

// fileServerReplies are the failed replies that the upload and edit pages of a file translate.
// tooLarge is the text of a 413 reply: only the upload page knows the size limit.
func fileServerReplies(tooLarge string) []serverReply {
	return []serverReply{
		fileReply("conflict", http.StatusConflict, "slug is already taken", fileSlugTaken),
		fileReply("too large", http.StatusRequestEntityTooLarge, "file is too large", tooLarge),
		fileReply("file name", http.StatusUnprocessableEntity, "file name must be 1-255 bytes",
			"The file name is not valid."),
		fileReply("validation", http.StatusUnprocessableEntity, "invalid ttl", "invalid ttl"),
		{
			name: "validation without text", status: http.StatusUnprocessableEntity, contentType: "text/plain",
			body: "bad request", abort: false, want: "The server did not accept these settings.",
		},
		fileReply("storage unavailable", http.StatusServiceUnavailable, "file storage is unavailable",
			fileStorageMessage),
		fileReply("files off", http.StatusServiceUnavailable, "feature not supported", filesOffMessage),
		fileReply("cross-origin", http.StatusForbidden, "cross-origin request rejected",
			"cross-origin request rejected"),
		{
			name: "not JSON", status: http.StatusInternalServerError, contentType: "text/plain",
			body: "upstream failure", abort: false, want: "The request failed. Try again.",
		},
	}
}

// testFileEditorErrors: the errors of the upload page, from the page checks and from the server.
func testFileEditorErrors(sess *uiSession) {
	// Notes, short links and files share one slug namespace.
	sess.createNote("taken-file")
	sess.gotoPage("/files")

	sess.button("Upload file")
	sess.expectError("#fileError", "Choose a file to upload.")

	// The page checks the size when the user selects the file and again before the upload.
	sess.chooseFile("large.bin", make([]byte, maxFileSize+1))
	sess.expectError("#fileError", fileTooLargeMessage)
	sess.button("Upload file")
	sess.expectError("#fileError", fileTooLargeMessage)

	sess.chooseFile(testFileName, testFileContent())
	sess.must(sess.expect.Locator(sess.page.Locator("#fileError")).ToBeHidden())

	sess.fill("#slugInput", "taken-file")
	sess.button("Upload file")
	sess.expectError("#fileError", fileSlugTaken)

	sess.fill("#slugInput", "login")
	sess.button("Upload file")
	sess.expectErrorContains("#fileError", invalidSlugMessage)
	sess.fill("#slugInput", "")

	for _, reply := range fileServerReplies(fileTooLargeMessage) {
		sess.simulate("**/files", reply)
		sess.button("Upload file")
		sess.expectReply("#fileError", reply)
		sess.must(sess.expect.Locator(sess.page.Locator("#uploadProgress")).ToBeHidden())
		sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
		sess.text("#fileName", testFileName)
	}

	// XMLHttpRequest takes a canceled request (ERR_ABORTED) as a cancel of the user, so a broken
	// connection needs a different network error.
	sess.allowFailure("net::ERR_FAILED")
	sess.must(sess.page.Unroute("**/files"))
	sess.must(sess.page.Route("**/files", func(route playwright.Route) {
		err := route.Abort("failed")
		if err != nil {
			sess.errorf("abort upload: %v", err)
		}
	}))
	sess.button("Upload file")
	sess.expectError("#fileError", fileNetworkMessage)
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())

	// A correct attempt after the errors uploads the file.
	sess.must(sess.page.Unroute("**/files"))
	sess.button("Upload file")
	sess.uploadedFile()
}

// testFileEditErrors: the errors of the edit page of a file.
func testFileEditErrors(sess *uiSession) {
	id, _ := sess.uploadTestFile()
	sess.openFileEditor()
	sess.page.OnDialog(func(dialog playwright.Dialog) { sess.must(dialog.Accept()) })

	sess.button("Save")
	sess.expectError("#fileError", "Edit code required")

	sess.button("Delete file")
	sess.expectError("#fileError", "Edit code required")

	sess.openSettings()
	sess.fill("#editCodeInput", wrongEditCode)
	sess.closeSettings()
	sess.button("Save")
	sess.expectError("#fileError", fileWrongEditCode)

	sess.button("Delete file")
	sess.expectError("#fileError", fileWrongEditCode)

	replies := append(fileServerReplies("The file is larger than the server limit."),
		fileReply("sign in", http.StatusUnauthorized, "unauthorized", "Sign in to change this file."),
		fileReply("not found", http.StatusNotFound, "not found", fileGoneDesc),
		fileReply("expired", http.StatusGone, "expired", fileGoneDesc),
		serverReply{name: "no connection", status: 0, contentType: "", body: "", abort: true, want: fileNetworkMessage},
	)
	for _, reply := range replies {
		sess.simulate("**/files/"+id, reply)
		sess.button("Save")
		sess.expectReply("#fileError", reply)
		sess.must(sess.expect.Locator(sess.page.Locator("#saveBtn")).ToBeEnabled())
	}

	sess.simulate("**/files/"+id, fileReply("not found", http.StatusNotFound, "not found", fileGoneDesc))
	sess.button("Delete file")
	sess.expectError("#fileError", fileGoneDesc)
	sess.must(sess.expect.Locator(sess.page.Locator("#deleteBtn")).ToBeEnabled())
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/edit/" + id))
}

// testFileErrorPages: the error pages of a missing file and of an expired file.
func testFileErrorPages(sess *uiSession) {
	sess.gotoPage("/files/missing-ui-file/download")
	sess.expectErrorPage("404", fileGoneTitle, fileGoneDesc)

	// The editor of each record is at /edit/{id}; without a record it is a missing note, as for a
	// short link. The old editor URL goes there.
	for _, path := range []string{"/edit/missing-ui-file", "/files/missing-ui-file/edit"} {
		sess.gotoPage(path)
		sess.expectErrorPage("404", notFoundTitle, notFoundDesc)
	}

	// The script uploads the file without the success page: that page reads the file, and a
	// reader of an expired file deletes it.
	id, isText := sess.eval(`async () => {
		const data = new FormData();
		data.append('ttl', '1');
		data.append('file', new Blob(['Short life']), 'short-life.txt');
		const response = await fetch('/files', {method: 'POST', headers: {Accept: 'application/json'}, body: data});
		return (await response.json()).id;
	}`).(string)
	require.True(sess.t, isText, "the upload returns the file ID")

	// The edit page reads only the record, so it shows the expiry without a change to the file.
	sess.awaitErrorCode("/edit/"+id, "410")
	sess.expectErrorPage("410", fileGoneTitle, fileGoneDesc)

	// The first reader of the file page deletes the expired file; then the file is not found.
	sess.gotoPage("/" + id)
	sess.expectErrorPage("410", fileGoneTitle, fileGoneDesc)

	sess.gotoPage("/files/" + id + "/download")
	sess.expectErrorPage("404", fileGoneTitle, fileGoneDesc)
	sess.gotoPage("/edit/" + id)
	sess.expectErrorPage("404", notFoundTitle, notFoundDesc)

	// Without the file record, the short address is a missing note.
	sess.gotoPage("/" + id)
	sess.expectErrorPage("404", notFoundTitle, notFoundDesc)
}

// testFilesOff: a server without object storage has no file tab and shows the page of files off.
func testFilesOff(sess *uiSession) {
	sess.gotoPage("/")
	sess.must(sess.expect.Locator(sess.page.Locator(`a.nav-tab[href="/files"]`)).ToHaveCount(0))
	sess.text(".site-footer > span:first-child", "Padmark — notes, links & files")

	for _, path := range []string{"/files", "/files/any-file/edit"} {
		sess.gotoPage(path)
		sess.expectErrorPage("503", "Files are off", filesOffMessage)
	}
}
