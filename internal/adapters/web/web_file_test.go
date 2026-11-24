package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

// browserClick is the download request of a click on the download button of the file page.
func browserClick(path string) *http.Request {
	req := browserGet(path)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-User", "?1")

	return req
}

// TestSuccessPage_AnonymousWithAccounts (R-15): with accounts on, an anonymous author of a file
// or a link gets the success page with the link and the edit code block, not the sign-in page.
func (s *FileHandlerSuite) TestSuccessPage_AnonymousWithAccounts() {
	router := s.newAuthRouter(s.auth)

	s.Run("file", func() {
		s.files.EXPECT().Inspect(gomock.Any(), "abc", accountAccess(false)).
			Return(fileRecord("a.txt", 3, 0, false), nil)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, browserGet("/success?kind=file&id=abc"))
		body := rec.Body.String()

		s.Require().Equal(http.StatusOK, rec.Code, rec.Header().Get("Location"))
		s.Contains(body, "Your file is live")
		s.Contains(body, "http://example.com/abc")
		s.Contains(body, `id="editCodeBlock"`)
		s.Contains(body, "<strong>a.txt</strong>")
	})

	s.Run("link", func() {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, browserGet("/success?kind=link&id=abc&privacy=public"))

		s.Require().Equal(http.StatusOK, rec.Code, rec.Header().Get("Location"))
		s.Contains(rec.Body.String(), "Your link is live")
		s.Contains(rec.Body.String(), `id="editCodeBlock"`)
	})

	s.Run("private file of another user", func() {
		s.files.EXPECT().Inspect(gomock.Any(), "abc", accountAccess(false)).Return(nil, domain.ErrForbidden)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, browserGet("/success?kind=file&id=abc"))
		body := rec.Body.String()

		s.Require().Equal(http.StatusOK, rec.Code)
		s.NotContains(body, `id="successFile"`)
		s.NotContains(body, "Anyone with the link can open it.")
	})
}

// TestSuccessPage_FileLookupFailsShowsLinkHidesSettings (R-48): without the record the page still
// shows the link (fail-soft). Without the values from the upload response, the page does not show
// "Public" and "never expires" as facts, and it has no file settings block.
func (s *FileHandlerSuite) TestSuccessPage_FileLookupFailsShowsLinkHidesSettings() {
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(nil, domain.ErrStorageUnavailable)

	rec := s.serve(browserGet("/success?kind=file&id=abc"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(body, "http://example.com/abc")
	s.NotContains(body, `id="successFile"`)
	s.NotContains(body, "Anyone with the link can open it.")
	s.NotContains(body, "never expires")
	s.NotContains(body, `class="chip-list"`)
}

// TestSuccessPage_FileLookupFailsUsesQuery (R-48): the upload page puts the settings of the upload
// response into the query, and the page shows them when it cannot read the record.
func (s *FileHandlerSuite) TestSuccessPage_FileLookupFailsUsesQuery() {
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(nil, domain.ErrStorageUnavailable)

	rec := s.serve(browserGet("/success?kind=file&id=abc&privacy=owner&burn=1&expires=2030-01-02T03:04:05Z"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(body, "Only you can open it.")
	s.Contains(body, "Owner only")
	s.Contains(body, "expires Jan 2, 2030")
	s.Contains(body, "Deleted after the first download")
	s.NotContains(body, "never expires")
}

// TestSuccessPage_FileRecordWins: the record replaces the query values when the read succeeds.
func (s *FileHandlerSuite) TestSuccessPage_FileRecordWins() {
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(fileRecord("a.txt", 3, 0, false), nil)

	rec := s.serve(browserGet("/success?kind=file&id=abc&privacy=owner&burn=1"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(body, "Anyone with the link can open it.")
	s.Contains(body, "never expires")
	s.NotContains(body, "Deleted after the first download")
}

// TestEditFilePage_PrivateWithoutAccounts (R-23): without accounts there is no privacy check,
// as for notes and links. The edit page of a file with a stored private level opens.
func (s *FileHandlerSuite) TestEditFilePage_PrivateWithoutAccounts() {
	for _, privacy := range []domain.Privacy{domain.PrivacyAuthenticated, domain.PrivacyOwner} {
		s.Run(string(privacy), func() {
			ownerID := uuid.New()
			stored := fileRecord("stored-name.txt", 3, 0, false)
			stored.OwnerID, stored.Privacy = &ownerID, new(privacy)
			s.expectEditFile(stored, nil)

			rec := s.serve(browserGet("/edit/abc"))

			s.Equal(http.StatusOK, rec.Code)
			s.Contains(rec.Body.String(), "stored-name.txt")
		})
	}
}

// TestEditFilePage_PrivateWithAccounts: with accounts on, the usecase refuses the anonymous
// reader of a private file (ErrForbidden); the edit page sends the browser to the sign-in page
// and does not show the file name.
func (s *FileHandlerSuite) TestEditFilePage_PrivateWithAccounts() {
	s.expectEditFile(nil, domain.ErrForbidden)

	rec := httptest.NewRecorder()
	s.newAuthRouter(s.auth).ServeHTTP(rec, browserGet("/edit/abc"))

	s.Equal(http.StatusSeeOther, rec.Code)
	s.Equal("/login?next=%2Fedit%2Fabc", rec.Header().Get("Location"))
	s.NotContains(rec.Body.String(), "secret-name.txt")
}

// TestDownload_BrowserGetDoesNotBurn (R-17): a browser GET never consumes a burn-after-download
// file, also a click on a same-origin link. The browser goes to the file page, which has the
// burn warning and the download form.
func (s *FileHandlerSuite) TestDownload_BrowserGetDoesNotBurn() {
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"click on a same-origin link", nil},
		{"prefetch", map[string]string{"Sec-Purpose": "prefetch"}},
		{"prerender", map[string]string{"Sec-Purpose": "prefetch;prerender"}},
		{"legacy prefetch", map[string]string{"Purpose": "prefetch"}},
		{"typed URL", map[string]string{"Sec-Fetch-Site": "none"}},
		{"link in another site", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"navigation without a user action", map[string]string{"Sec-Fetch-User": ""}},
		{"browser without fetch metadata", map[string]string{"Sec-Fetch-Site": "", "Sec-Fetch-Mode": ""}},
	}

	for _, test := range cases {
		s.Run(test.name, func() {
			req := browserClick("/files/abc/download")

			for name, value := range test.headers {
				if value == "" {
					req.Header.Del(name)

					continue
				}

				req.Header.Set(name, value)
			}

			s.files.EXPECT().Download(gomock.Any(), "abc", access(false)).Return(nil, domain.ErrBurnIntentRequired)

			rec := s.serve(req)
			s.Equal(http.StatusSeeOther, rec.Code)
			s.Equal("/abc", rec.Header().Get("Location"))
		})
	}
}

// TestDownload_CrossSiteFetchDoesNotBurn: a script in another site cannot consume a file with an
// exact Accept; it gets the 409 JSON answer.
func (s *FileHandlerSuite) TestDownload_CrossSiteFetchDoesNotBurn() {
	s.files.EXPECT().Download(gomock.Any(), "abc", access(false)).Return(nil, domain.ErrBurnIntentRequired)

	req := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")

	rec := s.serve(req)
	s.Equal(http.StatusConflict, rec.Code)
	s.Contains(rec.Body.String(), "application/octet-stream")
}

// TestDownload_NonBrowserClientsBurn (R-17): clients without fetch metadata (the CLI, MCP, curl)
// keep the Accept rules.
func (s *FileHandlerSuite) TestDownload_NonBrowserClientsBurn() {
	for _, accept := range []string{"application/octet-stream", "application/json"} {
		s.Run(accept, func() {
			s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).
				Return(downloadOf("a.txt", "abc", true), nil)

			req := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
			req.Header.Set("Accept", accept)

			s.Equal(http.StatusOK, s.serve(req).Code)
		})
	}
}

// downloadForm builds the POST of the download form of the file page of "abc", with a valid
// CSRF cookie and field, as a click on the button sends it.
func downloadForm() *http.Request {
	csrf := web.GenerateCSRFTokenForTest(testCSRFSecret)
	form := url.Values{"csrf_token": {csrf}}.Encode()

	req := httptest.NewRequest(http.MethodPost, "/files/abc/download", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.AddCookie(httptestutil.RequestCookie("padmark_csrf", csrf))

	return req
}

// TestDownloadForm_Burns (R-17): the download form of the file page consumes a burn file.
func (s *FileHandlerSuite) TestDownloadForm_Burns() {
	s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).Return(downloadOf("a.txt", "abc", true), nil)

	rec := s.serve(downloadForm())

	s.Require().Equal(http.StatusOK, rec.Code, rec.Body.String())
	s.Equal("abc", rec.Body.String())
	s.Contains(rec.Header().Get("Content-Disposition"), "attachment")
}

// TestDownloadForm_Refused: without the CSRF field, or from another site, the form does not
// reach the usecase (strict mock).
func (s *FileHandlerSuite) TestDownloadForm_Refused() {
	s.Run("no CSRF field", func() {
		req := downloadForm()
		req.Body = io.NopCloser(strings.NewReader(""))
		req.ContentLength = 0

		s.Equal(http.StatusForbidden, s.serve(req).Code)
	})

	s.Run("cross-site", func() {
		req := downloadForm()
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")

		s.Equal(http.StatusForbidden, s.serve(req).Code)
	})

	s.Run("files off", func() {
		rec := httptest.NewRecorder()
		s.newRouter(false).ServeHTTP(rec, downloadForm())

		s.Equal(http.StatusServiceUnavailable, rec.Code)
	})
}

// TestFilePage_DownloadButton: a burn file has the download form; another file has a link.
func (s *FileHandlerSuite) TestFilePage_DownloadButton() {
	s.Run("burn file", func() {
		file := fileRecord("a.txt", 3, 0, true)

		s.expectFileKind()
		s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(file, nil)

		body := s.serve(browserGet("/abc")).Body.String()

		s.Contains(body, `<form method="POST" action="/files/abc/download" id="downloadForm" hidden>`)
		s.Contains(body, `name="csrf_token" value="`)
		s.Contains(body, `<button type="submit" form="downloadForm" class="btn-primary file-download" id="downloadBtn">`)
	})

	s.Run("other file", func() {
		file := fileRecord("a.txt", 3, 0, false)

		s.expectFileKind()
		s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(file, nil)

		body := s.serve(browserGet("/abc")).Body.String()

		s.Contains(body, `<a href="/files/abc/download" class="btn-primary file-download" id="downloadBtn">`)
		s.NotContains(body, `id="downloadForm"`)
	})
}

func (s *FileHandlerSuite) TestCreateFilePage() {
	rec := s.serve(browserGet("/files"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(body, `id="fileInput"`)
	s.Contains(body, `id="dropZone"`)
	s.Contains(body, `id="fileError"`)
	s.Contains(body, `<progress class="upload-progress-bar" id="progressBar"`)
	s.Contains(body, `id="cancelBtn"`)
	s.Contains(body, `id="burnCheck"`)
	s.Contains(body, `id="slugInput"`)
	s.Contains(body, `id="editCodeInput"`)
	s.Contains(body, `<span id="fileLimit">Up to 1 MiB</span>`)
	s.Contains(body, `data-max-size="1048576"`)
	s.Contains(body, `<a href="/files" class="nav-tab" aria-current="page">`)
	s.Contains(body, "<span>Padmark — notes, links &amp; files</span>")
	// The fields go before the file part; the server reads them first.
	s.Less(strings.Index(body, "data.append('ttl'"), strings.Index(body, "data.append('file'"))
}

// TestFilePage_BrowserGetsPage: a browser on /{id} gets the page. Inspect reads only the metadata,
// and no Download call is expected, so the page does not consume a burn file.
func (s *FileHandlerSuite) TestFilePage_BrowserGetsPage() {
	file := fileRecord("отчёт <1>.pdf", 3*1024*1024/2, 0, true)

	s.expectFileKind()
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(file, nil)

	rec := s.serve(browserGet("/abc"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Equal("text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	s.Equal("private, no-store", rec.Header().Get("Cache-Control"))
	s.Contains(body, "отчёт &lt;1&gt;.pdf")
	s.Contains(body, "1.5 MiB")
	s.Contains(body, `action="/files/abc/download"`)
	s.Contains(body, `href="/edit/abc"`)
	s.Contains(body, `id="burnWarning"`)
	s.Contains(body, "Never expires")
	s.Contains(body, "0 downloads")
	// The file page has the frame of the note page, and "New" opens the upload page.
	s.Contains(body, `class="card article-card"`)
	s.Contains(body, `href="/files" class="btn-primary header-new" aria-label="New file"`)
}

func (s *FileHandlerSuite) TestFilePage_NoBurnWarning() {
	file := fileRecord("a.txt", 3, 0, false)
	expires := time.Date(2030, time.March, 4, 5, 6, 0, 0, time.UTC)
	file.ExpiresAt = &expires

	s.expectFileKind()
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(file, nil)

	rec := s.serve(browserGet("/abc"))

	s.Require().Equal(http.StatusOK, rec.Code)
	s.NotContains(rec.Body.String(), `id="burnWarning"`)
	s.Contains(rec.Body.String(), "Expires Mar 4, 2030")
	s.Contains(rec.Body.String(), `data-expires="2030-03-04T05:06:00Z"`)
}

func (s *FileHandlerSuite) TestFilePage_Errors() {
	cases := []struct {
		err    error
		text   string
		status int
	}{
		{domain.ErrExpired, "The file was deleted or its lifetime ended.", http.StatusGone},
		{domain.ErrStorageUnavailable, "File storage is unavailable. Try again later.", http.StatusServiceUnavailable},
	}

	for _, test := range cases {
		s.Run(test.err.Error(), func() {
			s.expectFileKind()
			s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(nil, test.err)

			rec := s.serve(browserGet("/abc"))

			s.Equal(test.status, rec.Code)
			s.Contains(rec.Body.String(), test.text)
		})
	}
}

// expectEditFile sets the reads of GET /edit/abc for a file: the note read tells that a file has
// the slug, then the file editor reads the file with Settings, which applies privacy and expiry
// and never deletes an expired file.
func (s *FileHandlerSuite) expectEditFile(file *domain.File, err error) {
	expectOtherKindEditor(s.manager, "abc", domain.KindFile)
	s.files.EXPECT().Settings(gomock.Any(), "abc", gomock.Any()).Return(file, err)
}

func (s *FileHandlerSuite) TestEditFilePage() {
	s.expectEditFile(fileRecord("a.txt", 3, 0, true), nil)

	rec := s.serve(browserGet("/edit/abc"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(body, `id="fileName">a.txt<`)
	s.Contains(body, `id="saveBtn"`)
	s.Contains(body, `id="deleteBtn"`)
	s.Contains(body, `id="fileError"`)
	s.Contains(body, "Edit code <span class=\"optional\">· required</span>")
	s.Contains(body, `placeholder="Paste your edit code"`)
	s.Contains(body, `id="burnCheck" checked`)
	s.Contains(body, `value="keep"`)
	s.Contains(body, "headers['X-Edit-Code'] = code")
}

// TestEditFilePage_OldURL: the old editor URL sends the browser to /edit/{id}, as for notes.
func (s *FileHandlerSuite) TestEditFilePage_OldURL() {
	rec := s.serve(browserGet("/files/abc/edit"))

	s.Equal(http.StatusMovedPermanently, rec.Code)
	s.Equal("/edit/abc", rec.Header().Get("Location"))
}

func (s *FileHandlerSuite) TestEditFilePage_Errors() {
	s.Run("not found", func() {
		s.expectEditFile(nil, domain.ErrNotFound)

		rec := s.serve(browserGet("/edit/abc"))

		s.Equal(http.StatusNotFound, rec.Code)
	})

	s.Run("expired", func() {
		// The usecase applies the expiry rule; the editor shows its error.
		s.expectEditFile(nil, domain.ErrExpired)

		rec := s.serve(browserGet("/edit/abc"))

		s.Equal(http.StatusGone, rec.Code)
		s.Contains(rec.Body.String(), "The file was deleted or its lifetime ended.")
	})

	s.Run("storage unavailable", func() {
		s.expectEditFile(nil, domain.ErrStorageUnavailable)

		rec := s.serve(browserGet("/edit/abc"))

		s.Equal(http.StatusServiceUnavailable, rec.Code)
		s.Contains(rec.Body.String(), "File storage is unavailable. Try again later.")
	})
}

func (s *FileHandlerSuite) TestSuccessPage_File() {
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(fileRecord("a.txt", 2048, 0, true), nil)

	rec := s.serve(browserGet("/success?kind=file&id=abc"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(body, "Your file is live")
	s.Contains(body, "<strong>a.txt</strong><span>2 KiB</span>")
	// As for a note, the page opens the record; Edit is on the file page.
	s.Contains(body, `id="openRecordLink">`)
	s.Contains(body, `href="http://example.com/abc" class="btn-ghost" id="openRecordLink"`)
	s.NotContains(body, `/edit/abc`)
	s.Contains(body, "Deleted after the first download")
	s.Contains(body, "It is shown only once")
	s.Contains(body, "'padmark_file_edit_'")
}
