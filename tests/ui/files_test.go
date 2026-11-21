package ui_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

// The test file and the settings of the file scenarios.
const (
	testFileName = "ui-notes.txt"
	fileEditCode = "File-Edit-Code42!"
	// fileGoneTitle and fileGoneDesc are the error page of a file that is deleted or expired.
	fileGoneTitle = "File not found"
	fileGoneDesc  = "The file was deleted or its lifetime ended."
	// fileHourTolerance is the permitted difference between the expected and the stored expiry.
	fileHourTolerance = 30 * time.Second
)

// testFileContent returns the content of the test file: text lines and all byte values, so that
// a download that changes the bytes fails the comparison.
func testFileContent() []byte {
	content := []byte("Padmark UI file\nThe second line: ✓\n")
	for value := range 256 {
		content = append(content, byte(value))
	}

	return content
}

// fileMetadata is the JSON body of GET /files/{id}.
type fileMetadata struct {
	ExpiresAt         *time.Time `json:"expires_at"`
	Name              string     `json:"name"`
	Privacy           string     `json:"privacy"`
	Size              int64      `json:"size"`
	BurnAfterDownload bool       `json:"burn_after_download"`
}

func TestFiles(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "UploadAndDownload", run: testFileUploadAndDownload, files: true, accounts: false, allowedStatuses: nil},
		{
			name: "BurnAfterDownload", run: testFileBurnAfterDownload, files: true, accounts: false,
			allowedStatuses: []int{http.StatusNotFound},
		},
		{name: "EditSettings", run: testFileEditSettings, files: true, accounts: true, allowedStatuses: nil},
		{name: "DeleteWithCode", run: testFileDeleteWithCode, files: true, accounts: false, allowedStatuses: nil},
		{name: "DeleteAsOwner", run: testFileDeleteAsOwner, files: true, accounts: true, allowedStatuses: nil},
		{name: "PrivateFile", run: testPrivateFile, files: true, accounts: true, allowedStatuses: nil},
		{name: "CancelUpload", run: testFileCancelUpload, files: true, accounts: false, allowedStatuses: nil},
	})
}

// chooseFile puts a file with the given name and content into the file field of the upload page.
func (sess *uiSession) chooseFile(name string, content []byte) {
	sess.t.Helper()
	sess.must(sess.page.Locator("#fileInput").SetInputFiles([]playwright.InputFile{
		{Name: name, MimeType: "application/octet-stream", Buffer: content},
	}))
}

// uploadedFile waits for the success page of an upload and returns the file ID and the edit code.
func (sess *uiSession) uploadedFile() (string, string) {
	sess.t.Helper()
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.text(".success-title", "Your file is live")
	current, err := url.Parse(sess.page.URL())
	sess.must(err)
	require.Equal(sess.t, "file", current.Query().Get("kind"))

	id := current.Query().Get("id")
	require.NotEmpty(sess.t, id)

	return id, sess.shownEditCode()
}

// uploadTestFile uploads the test file with the default settings and returns its ID and edit code.
func (sess *uiSession) uploadTestFile() (string, string) {
	sess.t.Helper()
	sess.gotoPage("/files")
	sess.chooseFile(testFileName, testFileContent())
	sess.button("Upload file")

	return sess.uploadedFile()
}

// readFile reads the file metadata as the signed-in user of the browser context.
func (sess *uiSession) readFile(id string) fileMetadata {
	sess.t.Helper()
	response, err := sess.browserContext.Request().Get("/files/" + id)
	sess.must(err)
	require.Equal(sess.t, http.StatusOK, response.Status())

	var metadata fileMetadata
	sess.must(response.JSON(&metadata))

	return metadata
}

// fileStatus returns the status of GET /files/{id} for the browser context.
func (sess *uiSession) fileStatus(id string) int {
	sess.t.Helper()
	response, err := sess.browserContext.Request().Get("/files/" + id)
	sess.must(err)

	return response.Status()
}

// downloadFile clicks the download button of the file page and returns the saved content.
func (sess *uiSession) downloadFile() []byte {
	sess.t.Helper()
	download, err := sess.page.ExpectDownload(func() error { return sess.page.Locator("#downloadBtn").Click() })
	sess.must(err)

	path, err := download.Path()
	sess.must(err)

	content, err := os.ReadFile(filepath.Clean(path))
	sess.must(err)

	return content
}

// testFileUploadAndDownload: the upload page, the result page, the file page, the download and
// the edit page, with the screenshots of the four pages.
func testFileUploadAndDownload(sess *uiSession) {
	content := testFileContent()

	sess.openUploadPage()
	sess.fillUploadForm(content)
	sess.button("Upload file")

	id := sess.expectUploadResult()
	sess.link("Open file")
	sess.expectFilePage(id, content)
	sess.click("#editFileLink")
	sess.expectFileEditPage(id)
}

// openUploadPage goes from the note editor to the upload page.
func (sess *uiSession) openUploadPage() {
	sess.t.Helper()
	sess.gotoPage("/")

	tab := sess.page.Locator(`a.nav-tab[href="/files"]`)
	sess.visible(`a.nav-tab[href="/files"]`)
	// Phone width hides the editor footer with the upload link; the header tab stays.
	if sess.mobile() {
		sess.must(tab.Click())
	} else {
		sess.link("Upload a file")
	}

	sess.must(sess.expect.Locator(tab).ToHaveAttribute("aria-current", "page"))
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/files"))
	sess.text("#fileLimit", "Maximum: 1 MiB")
}

// fillUploadForm selects the test file and sets the slug and the edit code of the upload page.
func (sess *uiSession) fillUploadForm(content []byte) {
	sess.t.Helper()
	sess.chooseFile(testFileName, content)
	sess.text("#fileName", testFileName)
	sess.text("#fileSize", "293 B")
	sess.fill("#slugInput", "ui-file")
	sess.text("#previewSlug", "ui-file")
	sess.openSettings()
	sess.expectPrivacyOptions("public")
	sess.fill("#editCodeInput", fileEditCode)
	sess.expectLayout("#editCodeInput", "#privacySelect", "#expirySelect", "#burnLabel")
	sess.closeSettings()
	sess.expectLayout(".header", "#dropZone", "#slugInput", "#submitBtn")
	sess.screenshot("file-editor.png", "#previewHost")
}

// expectUploadResult checks the result page of fillUploadForm and returns the file ID.
func (sess *uiSession) expectUploadResult() string {
	sess.t.Helper()

	id, code := sess.uploadedFile()
	require.Equal(sess.t, "ui-file", id)
	require.Equal(sess.t, fileEditCode, code)
	sess.text("#successFile strong", testFileName)
	sess.text("#successFile span", "293 B")
	sess.must(sess.expect.Locator(sess.page.Locator(".copy-value")).ToHaveText(sess.baseURL + "/" + id))
	sess.screenshot("file-created.png", ".copy-value")

	return id
}

// expectFilePage checks the file page and two downloads of a file without burn after download.
func (sess *uiSession) expectFilePage(id string, content []byte) {
	sess.t.Helper()
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/" + id))
	sess.text("#fileTitle", testFileName)
	sess.text("#fileSize", "293 B")
	sess.must(sess.expect.Locator(sess.page.Locator("#burnWarning")).ToHaveCount(0))
	// The file page has the frame and the actions of the note page; "New" opens the upload page.
	sess.visible(".article-card")
	sess.must(sess.expect.Locator(sess.page.Locator(".header-new")).ToHaveAttribute("href", "/files"))
	sess.expectLayout("#downloadBtn", "#copyLinkBtn", "#editFileLink")
	sess.screenshot("file.png")

	require.Equal(sess.t, content, sess.downloadFile())
	// A file without burn after download stays after the download.
	require.Equal(sess.t, content, sess.downloadFile())

	metadata := sess.readFile(id)
	require.Equal(sess.t, testFileName, metadata.Name)
	require.Equal(sess.t, int64(len(content)), metadata.Size)
}

// expectFileEditPage checks the edit page of a file that never expires.
func (sess *uiSession) expectFileEditPage(id string) {
	sess.t.Helper()
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/edit/" + id))
	sess.text("#fileName", testFileName)
	sess.value("#expirySelect", "keep")
	sess.text("#expiryHint", "Never expires")
	sess.screenshot("file-edit.png", ".link-strip-short")
}

// testFileBurnAfterDownload: the file page warns before the download, the first download gives
// the content and the second download shows the error page.
func testFileBurnAfterDownload(sess *uiSession) {
	sess.gotoPage("/files")
	sess.chooseFile(testFileName, testFileContent())
	sess.openSettings()
	sess.click("#burnLabel")
	sess.must(sess.expect.Locator(sess.page.Locator("#burnCheck")).ToBeChecked())
	sess.closeSettings()
	sess.button("Upload file")
	id, _ := sess.uploadedFile()
	sess.visible(".chip--warn")
	require.True(sess.t, sess.readFile(id).BurnAfterDownload)

	// The page reads only the metadata: two visits do not use the single download.
	sess.gotoPage("/" + id)
	sess.gotoPage("/" + id)
	sess.visible("#burnWarning")
	sess.expectReadable("#burnWarning")
	require.Equal(sess.t, testFileContent(), sess.downloadFile())

	require.Equal(sess.t, http.StatusNotFound, sess.fileStatus(id))
	sess.gotoPage("/files/" + id + "/download")
	sess.expectErrorPage("404", fileGoneTitle, fileGoneDesc)
}

// testFileEditSettings: the owner changes the privacy, the lifetime and burn after download.
func testFileEditSettings(sess *uiSession) {
	sess.admin()

	id := sess.uploadOwnerFile()
	sess.openFileEditor()
	sess.must(sess.expect.Locator(sess.page.Locator("#privacy-owner")).ToBeChecked())
	sess.value("#expirySelect", "keep")
	sess.must(sess.expect.Locator(sess.page.Locator("#expiryHint")).ToContainText("Expires "))

	// The owner saves without the edit code.
	sess.saveFileSettings(id, func() {
		sess.choosePrivacy("authenticated")
		sess.selectOption("#expirySelect", "3600")
	})
	sess.click("#editFileLink")
	sess.value("#expirySelect", "keep")

	saved := sess.readFile(id)
	require.Equal(sess.t, "authenticated", saved.Privacy)
	require.NotNil(sess.t, saved.ExpiresAt)
	require.WithinDuration(sess.t, time.Now().Add(time.Hour), *saved.ExpiresAt, fileHourTolerance)
	require.False(sess.t, saved.BurnAfterDownload)

	sess.saveFileSettings(id, func() {
		sess.selectOption("#expirySelect", "never")
		sess.click("#burnLabel")
	})

	saved = sess.readFile(id)
	require.Equal(sess.t, "authenticated", saved.Privacy)
	require.Nil(sess.t, saved.ExpiresAt)
	require.True(sess.t, saved.BurnAfterDownload)

	// The file page shows the saved settings and does not use the single download.
	sess.visible("#burnWarning")
	require.True(sess.t, sess.readFile(id).BurnAfterDownload)
}

// uploadOwnerFile uploads the test file for the owner only, with a lifetime of one day.
func (sess *uiSession) uploadOwnerFile() string {
	sess.t.Helper()
	sess.gotoPage("/files")
	sess.expectPrivacyOptions("public", "authenticated", "owner")
	sess.chooseFile(testFileName, testFileContent())
	sess.openSettings()
	sess.choosePrivacy("owner")
	sess.selectOption("#expirySelect", "86400")
	sess.closeSettings()
	sess.button("Upload file")

	id, _ := sess.uploadedFile()
	sess.text(".success-subtitle", "Only you can open it.")

	original := sess.readFile(id)
	require.Equal(sess.t, "owner", original.Privacy)
	require.NotNil(sess.t, original.ExpiresAt)
	require.WithinDuration(sess.t, time.Now().Add(24*time.Hour), *original.ExpiresAt, fileHourTolerance)

	return id
}

// saveFileSettings changes the settings of the edit page and saves them. After the save, the
// browser shows the file page, as after the save of a note.
func (sess *uiSession) saveFileSettings(id string, change func()) {
	sess.t.Helper()
	sess.openSettings()
	change()
	sess.closeSettings()
	sess.button("Save")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/" + id))
}

// openFileEditor goes from the result page to the editor, as for a note: "Open file", then the
// Edit action of the file page.
func (sess *uiSession) openFileEditor() {
	sess.t.Helper()
	sess.link("Open file")
	sess.click("#editFileLink")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/edit/")))
}

// testFileDeleteWithCode: a user without an account deletes the file with the edit code.
func testFileDeleteWithCode(sess *uiSession) {
	id, code := sess.uploadTestFile()
	sess.openFileEditor()
	sess.page.OnDialog(func(dialog playwright.Dialog) { sess.must(dialog.Accept()) })

	sess.button("Delete file")
	sess.expectError("#fileError", "Edit code required")
	require.Equal(sess.t, http.StatusOK, sess.fileStatus(id))

	sess.openSettings()
	sess.fill("#editCodeInput", code)
	sess.closeSettings()
	sess.button("Delete file")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/files"))
	require.Equal(sess.t, http.StatusNotFound, sess.fileStatus(id))
}

// testFileDeleteAsOwner: the owner deletes the file without the edit code.
func testFileDeleteAsOwner(sess *uiSession) {
	sess.admin()

	id, _ := sess.uploadTestFile()
	sess.openFileEditor()
	sess.must(sess.expect.Locator(sess.page.Locator(`label[for="editCodeInput"]`)).
		ToContainText("optional, you created this file"))
	sess.page.OnDialog(func(dialog playwright.Dialog) { sess.must(dialog.Accept()) })
	sess.button("Delete file")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/files"))
	require.Equal(sess.t, http.StatusNotFound, sess.fileStatus(id))
}

// testPrivateFile: a user without a session cannot see or download a private file.
func testPrivateFile(sess *uiSession) {
	sess.admin()
	sess.gotoPage("/files")
	sess.chooseFile("private-name.txt", []byte("keep-private-content"))
	sess.openSettings()
	sess.choosePrivacy("authenticated")
	sess.closeSettings()
	sess.button("Upload file")
	id, _ := sess.uploadedFile()

	anonymous, err := runner.playwright.Request.NewContext(playwright.APIRequestNewContextOptions{
		BaseURL: new(sess.baseURL),

		ClientCertificates: nil, ExtraHttpHeaders: nil, FailOnStatusCode: nil,
		HttpCredentials: nil, IgnoreHttpsErrors: nil, MaxRedirects: nil, Proxy: nil,
		StorageState: nil, StorageStatePath: nil, Timeout: nil, UserAgent: nil,
	})
	sess.must(err)

	defer anonymous.Dispose()

	get := func(path, accept string) playwright.APIResponse {
		response, getErr := anonymous.Get(path, playwright.APIRequestContextGetOptions{
			Headers: map[string]string{"Accept": accept}, MaxRedirects: new(0),

			Data: nil, FailOnStatusCode: nil, Form: nil, IgnoreHttpsErrors: nil, MaxRetries: nil,
			Multipart: nil, Params: nil, Timeout: nil,
		})
		sess.must(getErr)

		body, bodyErr := response.Body()
		sess.must(bodyErr)
		require.NotContains(sess.t, string(body), "private-name", path)
		require.NotContains(sess.t, string(body), "keep-private-content", path)

		return response
	}

	for _, path := range []string{"/" + id, "/edit/" + id, "/files/" + id + "/download"} {
		response := get(path, "text/html")
		require.Equal(sess.t, http.StatusSeeOther, response.Status(), path)
		require.Contains(sess.t, response.Headers()["location"], "/login?next=", path)
	}

	require.Equal(sess.t, http.StatusUnauthorized, get("/files/"+id, "application/json").Status())
	require.Equal(sess.t, http.StatusUnauthorized,
		get("/files/"+id+"/download", "application/octet-stream").Status())

	// The signed-in owner still downloads the file.
	sess.gotoPage("/" + id)
	require.Equal(sess.t, []byte("keep-private-content"), sess.downloadFile())
}

// testFileCancelUpload: the user cancels an upload that the server did not answer yet.
func testFileCancelUpload(sess *uiSession) {
	sess.gotoPage("/files")
	sess.chooseFile(testFileName, testFileContent())

	// The route holds the upload without an answer, so the upload stays in progress until the
	// user cancels it.
	sess.must(sess.page.Route("**/files", func(route playwright.Route) {
		if route.Request().Method() != http.MethodPost {
			sess.must(route.Continue())
		}
	}))
	sess.button("Upload file")
	sess.visible("#uploadProgress")
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeDisabled())
	sess.expectLayout("#progressBar", "#cancelBtn")

	sess.button("Cancel upload")
	sess.text("#fileStatus", "Upload canceled")
	sess.must(sess.expect.Locator(sess.page.Locator("#uploadProgress")).ToBeHidden())
	sess.must(sess.expect.Locator(sess.page.Locator("#submitBtn")).ToBeEnabled())
	sess.must(sess.expect.Locator(sess.page.Locator("#fileError")).ToBeHidden())
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/files"))

	// The user can upload the same file after the cancel.
	sess.must(sess.page.Unroute("**/files"))
	sess.button("Upload file")
	sess.uploadedFile()
}
