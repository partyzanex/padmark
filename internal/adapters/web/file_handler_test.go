package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

const fileTestMaxSize = 1 << 20

type FileHandlerSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	manager     *MockNoteManager
	linkManager *MockLinkManager
	files       *MockFileManager
	router      http.Handler
}

func TestFileHandlerSuite(t *testing.T) {
	suite.Run(t, new(FileHandlerSuite))
}

func (s *FileHandlerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.files = NewMockFileManager(s.ctrl)
	s.files.EXPECT().MaxSize().Return(int64(fileTestMaxSize)).AnyTimes()
	s.router = s.newRouter(true)
}

func (s *FileHandlerSuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *FileHandlerSuite) newRouter(withFiles bool) http.Handler {
	handler := web.NewHandler(s.manager, s.linkManager, discardLog())
	if withFiles {
		handler.WithFileManager(s.files)
	}

	ogen := web.NewOgenHandler(s.manager, s.linkManager, web.NoPinger{}, discardLog())
	opts := web.RouterOptions{
		MaxBodyBytes: 64 * 1024,
		CSRFSecret:   testCSRFSecret,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	}

	router, err := web.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// uploadRequest builds a multipart upload with fields in order, then the file part.
func uploadRequest(fields [][2]string, name string, content []byte) *http.Request {
	var body bytes.Buffer

	writer := multipart.NewWriter(&body)
	for _, field := range fields {
		_ = writer.WriteField(field[0], field[1])
	}

	part, _ := writer.CreateFormFile("file", name)
	_, _ = part.Write(content)
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	return req
}

func (s *FileHandlerSuite) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	return rec
}

func (s *FileHandlerSuite) TestUpload_ReturnsLinkAndEditCode() {
	content := bytes.Repeat([]byte("a"), 100*1024) // larger than the general body limit

	s.files.EXPECT().Create(gomock.Any(), "report.pdf", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, name string, body io.Reader, opts files.CreateOptions) (*domain.File, error) {
			got, err := io.ReadAll(body)
			s.Require().NoError(err)
			s.Equal(content, got)
			s.True(opts.BurnAfterDownload)
			s.NotNil(opts.ExpiresAt)
			s.Equal("my-report", opts.CustomSlug)
			s.Nil(opts.OwnerID)

			file := fileRecord(name, int64(len(got)), 0, true)
			file.ID = "my-report"
			file.EditCode = "plain-code"

			return file, nil
		})

	req := uploadRequest([][2]string{{"ttl", "3600"}, {"burn_after_download", "true"}, {"slug", "my-report"}},
		"report.pdf", content)
	rec := s.serve(req)

	s.Require().Equal(http.StatusCreated, rec.Code, rec.Body.String())
	s.Equal("/my-report", rec.Header().Get("Location"))

	var body map[string]any
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	s.Equal("my-report", body["id"])
	s.Equal("http://example.com/my-report", body["url"])
	s.Equal("plain-code", body["edit_code"])
	s.Equal("public", body["privacy"])
}

func (s *FileHandlerSuite) TestUpload_Errors() {
	s.Run("not multipart", func() {
		req := httptest.NewRequest(http.MethodPost, "/files", strings.NewReader("raw"))
		s.Equal(http.StatusBadRequest, s.serve(req).Code)
	})

	s.Run("no file part", func() {
		var body bytes.Buffer

		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("ttl", "10")
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/files", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		s.Equal(http.StatusBadRequest, s.serve(req).Code)
	})

	s.Run("bad ttl", func() {
		req := uploadRequest([][2]string{{"ttl", "-1"}}, "a.txt", []byte("x"))
		s.Equal(http.StatusBadRequest, s.serve(req).Code)
	})

	s.Run("protected privacy without accounts", func() {
		req := uploadRequest([][2]string{{"privacy", "owner"}}, "a.txt", []byte("x"))
		s.Equal(http.StatusUnprocessableEntity, s.serve(req).Code)
	})

	s.Run("too large", func() {
		s.files.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, domain.ErrFileTooLarge)

		req := uploadRequest(nil, "a.txt", []byte("x"))
		s.Equal(http.StatusRequestEntityTooLarge, s.serve(req).Code)
	})

	s.Run("storage unavailable", func() {
		s.files.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, domain.ErrStorageUnavailable)

		req := uploadRequest(nil, "a.txt", []byte("x"))
		rec := s.serve(req)
		s.Equal(http.StatusServiceUnavailable, rec.Code)
		s.NotContains(rec.Body.String(), "s3")
	})

	s.Run("cross-origin form", func() {
		req := uploadRequest(nil, "a.txt", []byte("x"))
		req.Header.Set("Origin", "https://evil.example")
		s.Equal(http.StatusForbidden, s.serve(req).Code)
	})
}

func (s *FileHandlerSuite) TestUpload_FilesOff() {
	router := s.newRouter(false)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, uploadRequest(nil, "a.txt", []byte("x")))

	s.Equal(http.StatusServiceUnavailable, rec.Code)
}

// fileRecord returns a file with the given visible fields; other fields are zero.
func fileRecord(name string, size int64, downloads int, burn bool) *domain.File {
	return &domain.File{
		CreatedAt: time.Time{}, ExpiresAt: nil, Privacy: nil, OwnerID: nil, ID: "abc", ObjectKey: "",
		SealedMeta: "", Name: name, ContentKey: nil, EditCode: "hash", Size: size, Downloads: downloads,
		BurnAfterDownload: burn,
	}
}

func downloadOf(name, content string, burn bool) *files.Download {
	return files.NewDownload(fileRecord(name, int64(len(content)), 0, burn),
		func(_ context.Context, dst io.Writer) error {
			_, err := io.WriteString(dst, content)

			return err
		})
}

func access(allowBurn bool) files.AccessOptions {
	return files.AccessOptions{CallerID: uuid.Nil, Authenticated: false, AllowBurn: allowBurn}
}

func (s *FileHandlerSuite) TestDownload_SendsAttachment() {
	s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).
		Return(downloadOf("отчёт <1>.html", "<script>", false), nil)

	req := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
	req.Header.Set("Accept", "application/octet-stream")
	rec := s.serve(req)

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Equal("<script>", rec.Body.String())
	s.Equal("application/octet-stream", rec.Header().Get("Content-Type"))
	s.Equal("8", rec.Header().Get("Content-Length"))
	s.Contains(rec.Header().Get("Content-Disposition"), `attachment; filename="_____ <1>.html"; filename*=UTF-8''`)
	s.Equal("private, no-store", rec.Header().Get("Cache-Control"))
}

func (s *FileHandlerSuite) TestDownload_PreviewDoesNotBurn() {
	s.files.EXPECT().Download(gomock.Any(), "abc", access(false)).
		Return(nil, domain.ErrBurnIntentRequired)

	req := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
	req.Header.Set("Accept", "*/*")

	s.Equal(http.StatusConflict, s.serve(req).Code)
}

func (s *FileHandlerSuite) TestDownload_HeadDoesNotCount() {
	s.files.EXPECT().Info(gomock.Any(), "abc", gomock.Any()).
		Return(fileRecord("a.txt", 3, 0, false), nil)

	rec := s.serve(httptest.NewRequest(http.MethodHead, "/files/abc/download", nil))

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("3", rec.Header().Get("Content-Length"))
	s.Empty(rec.Body.String())
}

// TestRootSlugServesFile: a client that asks for binary content gets the file on /{id}. A
// browser gets the file page instead (TestFilePage_BrowserGetsPage).
func (s *FileHandlerSuite) TestRootSlugServesFile() {
	s.manager.EXPECT().Peek(gomock.Any(), "abc").Return(nil, domain.ErrNotFound)
	s.linkManager.EXPECT().RedirectWithoutBurn(gomock.Any(), "abc").Return(nil, domain.ErrNotFound)
	s.files.EXPECT().Peek(gomock.Any(), "abc").Return(fileRecord("a.txt", 5, 0, true), nil)
	s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).
		Return(downloadOf("a.txt", "hello", true), nil)

	req := httptest.NewRequest(http.MethodGet, "/abc", nil)
	req.Header.Set("Accept", "application/octet-stream")
	rec := s.serve(req)

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Equal("hello", rec.Body.String())
}

func (s *FileHandlerSuite) TestRootSlugWithoutAnyResource() {
	s.manager.EXPECT().Peek(gomock.Any(), "abc").Return(nil, domain.ErrNotFound)
	s.linkManager.EXPECT().RedirectWithoutBurn(gomock.Any(), "abc").Return(nil, domain.ErrNotFound)
	s.files.EXPECT().Peek(gomock.Any(), "abc").Return(nil, domain.ErrNotFound)

	req := httptest.NewRequest(http.MethodGet, "/abc", nil)
	s.Equal(http.StatusNotFound, s.serve(req).Code)
}

func (s *FileHandlerSuite) TestMetadata() {
	s.files.EXPECT().Info(gomock.Any(), "abc", gomock.Any()).
		Return(fileRecord("a.txt", 3, 2, false), nil)

	rec := s.serve(httptest.NewRequest(http.MethodGet, "/files/abc", nil))
	s.Require().Equal(http.StatusOK, rec.Code)

	var body map[string]any
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	s.Equal("a.txt", body["name"])
	s.InDelta(2, body["downloads"], 0)
	s.NotContains(body, "edit_code")

	s.files.EXPECT().Info(gomock.Any(), "gone", gomock.Any()).Return(nil, domain.ErrExpired)
	s.Equal(http.StatusGone, s.serve(httptest.NewRequest(http.MethodGet, "/files/gone", nil)).Code)
}

func (s *FileHandlerSuite) TestDelete() {
	s.files.EXPECT().Delete(gomock.Any(), "abc", "code-1234", uuid.Nil).Return(nil)

	req := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
	req.Header.Set("X-Edit-Code", "code-1234")
	s.Equal(http.StatusNoContent, s.serve(req).Code)

	s.files.EXPECT().Delete(gomock.Any(), "abc", "wrong", uuid.Nil).Return(domain.ErrInvalidEditCode)

	req = httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
	req.Header.Set("X-Edit-Code", "wrong")
	s.Equal(http.StatusForbidden, s.serve(req).Code)
}

// newAuthRouter returns a router with files and accounts on. A request without credentials is
// anonymous, so the tests set AuthManager expectations only for the credentials that they send.
func (s *FileHandlerSuite) newAuthRouter(auth *MockAuthManager) http.Handler {
	handler := web.NewHandler(s.manager, s.linkManager, discardLog()).WithAuthManager(auth)
	handler.WithFileManager(s.files)

	ogen := web.NewOgenHandler(s.manager, s.linkManager, web.NoPinger{}, discardLog())
	opts := web.RouterOptions{
		MaxBodyBytes: 64 * 1024,
		CSRFSecret:   testCSRFSecret,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	}

	router, err := web.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

func updateRequest(body, editCode string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/files/abc", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if editCode != "" {
		req.Header.Set("X-Edit-Code", editCode)
	}

	return req
}

func (s *FileHandlerSuite) TestUpdate_WithEditCode() {
	updated := fileRecord("a.txt", 3, 2, true)
	authenticated := domain.PrivacyAuthenticated
	updated.Privacy = &authenticated

	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", uuid.Nil, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ uuid.UUID, opts files.UpdateOptions) (*domain.File, error) {
			s.Require().NotNil(opts.ExpiresAt)
			s.WithinDuration(time.Now().Add(time.Hour), *opts.ExpiresAt, time.Minute)
			s.False(opts.ClearExpiry)
			s.Nil(opts.Privacy, "privacy is not public without accounts, so the test omits it")
			s.Require().NotNil(opts.BurnAfterDownload)
			s.True(*opts.BurnAfterDownload)

			return updated, nil
		})

	rec := s.serve(updateRequest(`{"ttl":3600,"burn_after_download":true}`, "code-1234"))
	s.Require().Equal(http.StatusOK, rec.Code)

	var body map[string]any
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	s.Equal("a.txt", body["name"])
	s.Equal("authenticated", body["privacy"])
	s.Equal(true, body["burn_after_download"])
	s.NotContains(body, "edit_code")
}

func (s *FileHandlerSuite) TestUpdate_NullTTLClearsExpiry() {
	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", uuid.Nil, files.UpdateOptions{
		ExpiresAt: nil, Privacy: nil, BurnAfterDownload: nil, ClearExpiry: true,
	}).Return(fileRecord("a.txt", 3, 0, false), nil)

	s.Equal(http.StatusOK, s.serve(updateRequest(`{"ttl":null}`, "code-1234")).Code)
}

func (s *FileHandlerSuite) TestUpdate_OwnerWithoutEditCode() {
	owner := &domain.User{
		ID: uuid.New(), Username: "owner",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	ownerPrivacy := domain.PrivacyOwner
	stored := fileRecord("a.txt", 3, 0, false)
	stored.OwnerID, stored.Privacy = &owner.ID, &ownerPrivacy

	auth := NewMockAuthManager(s.ctrl)
	auth.EXPECT().ResolveAPIToken(gomock.Any(), "owner-key").Return(owner, "token-hash", nil)
	s.files.EXPECT().Peek(gomock.Any(), "abc").Return(stored, nil)
	s.files.EXPECT().Update(gomock.Any(), "abc", "", owner.ID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ uuid.UUID, opts files.UpdateOptions) (*domain.File, error) {
			s.Require().NotNil(opts.Privacy)
			s.Equal(domain.PrivacyOwner, *opts.Privacy)

			return stored, nil
		})

	req := updateRequest(`{"privacy":"owner"}`, "")
	req.Header.Set("Authorization", "Bearer owner-key")

	rec := httptest.NewRecorder()
	s.newAuthRouter(auth).ServeHTTP(rec, req)
	s.Equal(http.StatusOK, rec.Code)
}

func (s *FileHandlerSuite) TestUpdate_InvisibleFileIsUnauthorized() {
	ownerID := uuid.New()
	ownerPrivacy := domain.PrivacyOwner
	stored := fileRecord("a.txt", 3, 0, false)
	stored.OwnerID, stored.Privacy = &ownerID, &ownerPrivacy

	s.files.EXPECT().Peek(gomock.Any(), "abc").Return(stored, nil)

	rec := httptest.NewRecorder()
	s.newAuthRouter(NewMockAuthManager(s.ctrl)).ServeHTTP(rec, updateRequest(`{"ttl":60}`, "code-1234"))
	s.Equal(http.StatusUnauthorized, rec.Code)
}

func (s *FileHandlerSuite) TestUpdate_InvalidRequest() {
	for _, testCase := range []struct {
		name   string
		body   string
		status int
	}{
		{"malformed JSON", `{"ttl":`, http.StatusBadRequest},
		{"edit code in the body", `{"edit_code":"code-1234"}`, http.StatusBadRequest},
		{"negative ttl", `{"ttl":-1}`, http.StatusUnprocessableEntity},
		{"fractional ttl", `{"ttl":1.5}`, http.StatusUnprocessableEntity},
		{"string ttl", `{"ttl":"60"}`, http.StatusUnprocessableEntity},
		{"too large ttl", `{"ttl":9223372037}`, http.StatusUnprocessableEntity},
		{"unknown privacy", `{"privacy":"friends"}`, http.StatusUnprocessableEntity},
		{"privacy without accounts", `{"privacy":"authenticated"}`, http.StatusUnprocessableEntity},
	} {
		s.Run(testCase.name, func() {
			s.Equal(testCase.status, s.serve(updateRequest(testCase.body, "code-1234")).Code)
		})
	}
}

func (s *FileHandlerSuite) TestUpdate_UsecaseErrors() {
	for _, testCase := range []struct {
		err    error
		status int
	}{
		{domain.ErrInvalidEditCode, http.StatusForbidden},
		{domain.ErrNotFound, http.StatusNotFound},
		{domain.ErrOwnerPrivacyRequiresOwner, http.StatusUnprocessableEntity},
	} {
		s.Run(testCase.err.Error(), func() {
			s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", uuid.Nil, gomock.Any()).
				Return(nil, testCase.err)

			s.Equal(testCase.status, s.serve(updateRequest(`{}`, "code-1234")).Code)
		})
	}
}

func (s *FileHandlerSuite) TestUpdate_CrossOrigin() {
	req := updateRequest(`{"ttl":60}`, "code-1234")
	req.Header.Set("Origin", "https://evil.example")

	s.Equal(http.StatusForbidden, s.serve(req).Code)
}

func (s *FileHandlerSuite) TestUpdate_FailedCodesLockOut() {
	s.files.EXPECT().Update(gomock.Any(), "abc", "wrong", uuid.Nil, gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode).Times(10)

	for range 10 {
		s.Equal(http.StatusForbidden, s.serve(updateRequest(`{}`, "wrong")).Code)
	}

	s.Equal(http.StatusTooManyRequests, s.serve(updateRequest(`{}`, "wrong")).Code)
}

func (s *FileHandlerSuite) TestUpdate_FilesOff() {
	rec := httptest.NewRecorder()
	s.newRouter(false).ServeHTTP(rec, updateRequest(`{"ttl":60}`, "code-1234"))

	s.Equal(http.StatusServiceUnavailable, rec.Code)
}

// --- browser pages ---

// expectRootFile sets the lookups of GET /{id} for a slug that names only a file. explicit is
// true when the request states the intent to read, so the link lookup may burn.
func (s *FileHandlerSuite) expectRootFile(file *domain.File, explicit bool) {
	s.manager.EXPECT().Peek(gomock.Any(), "abc").Return(nil, domain.ErrNotFound)

	if explicit {
		s.linkManager.EXPECT().Redirect(gomock.Any(), "abc").Return(nil, domain.ErrNotFound)
	} else {
		s.linkManager.EXPECT().RedirectWithoutBurn(gomock.Any(), "abc").Return(nil, domain.ErrNotFound)
	}

	s.files.EXPECT().Peek(gomock.Any(), "abc").Return(file, nil)
}

func browserGet(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Sec-Fetch-Dest", "document")

	return req
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
	s.Contains(body, "Maximum: 1 MiB")
	s.Contains(body, `data-max-size="1048576"`)
	s.Contains(body, `<a href="/files" class="nav-tab" aria-current="page">`)
	s.Contains(body, "Markdown notes, short links &amp; files")
	// The fields go before the file part; the server reads them first.
	s.Less(strings.Index(body, "data.append('ttl'"), strings.Index(body, "data.append('file'"))
}

func (s *FileHandlerSuite) TestFilePages_FilesOff() {
	router := s.newRouter(false)

	for _, path := range []string{"/files", "/files/abc/edit"} {
		s.Run(path, func() {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, browserGet(path))

			s.Equal(http.StatusServiceUnavailable, rec.Code)
			s.Contains(rec.Body.String(), "File publishing is turned off on this server.")
			s.NotContains(rec.Body.String(), `href="/files" class="nav-tab"`)
		})
	}
}

// TestFilePage_BrowserGetsPage: a browser on /{id} gets the page. Info reads only the metadata,
// and no Download call is expected, so the page does not consume a burn file.
func (s *FileHandlerSuite) TestFilePage_BrowserGetsPage() {
	file := fileRecord("отчёт <1>.pdf", 3*1024*1024/2, 0, true)
	s.expectRootFile(file, true)
	s.files.EXPECT().Info(gomock.Any(), "abc", access(false)).Return(file, nil)

	rec := s.serve(browserGet("/abc"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Equal("text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	s.Equal("private, no-store", rec.Header().Get("Cache-Control"))
	s.Contains(body, "отчёт &lt;1&gt;.pdf")
	s.Contains(body, "1.5 MiB")
	s.Contains(body, `href="/files/abc/download"`)
	s.Contains(body, `href="/files/abc/edit"`)
	s.Contains(body, `id="burnWarning"`)
	s.Contains(body, "Never expires")
}

func (s *FileHandlerSuite) TestFilePage_NoBurnWarning() {
	file := fileRecord("a.txt", 3, 0, false)
	expires := time.Date(2030, time.March, 4, 5, 6, 0, 0, time.UTC)
	file.ExpiresAt = &expires
	s.expectRootFile(file, true)
	s.files.EXPECT().Info(gomock.Any(), "abc", access(false)).Return(file, nil)

	rec := s.serve(browserGet("/abc"))

	s.Require().Equal(http.StatusOK, rec.Code)
	s.NotContains(rec.Body.String(), `id="burnWarning"`)
	s.Contains(rec.Body.String(), "Expires Mar 4, 2030 05:06 UTC")
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
			s.expectRootFile(fileRecord("a.txt", 3, 0, false), true)
			s.files.EXPECT().Info(gomock.Any(), "abc", access(false)).Return(nil, test.err)

			rec := s.serve(browserGet("/abc"))

			s.Equal(test.status, rec.Code)
			s.Contains(rec.Body.String(), test.text)
		})
	}
}

// TestRootSlug_WildcardDownloads: curl without flags sends */*; it keeps getting the content.
func (s *FileHandlerSuite) TestRootSlug_WildcardDownloads() {
	s.expectRootFile(fileRecord("a.txt", 5, 0, false), false)
	s.files.EXPECT().Download(gomock.Any(), "abc", access(false)).
		Return(downloadOf("a.txt", "hello", false), nil)

	req := httptest.NewRequest(http.MethodGet, "/abc", nil)
	req.Header.Set("Accept", "*/*")
	rec := s.serve(req)

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Equal("hello", rec.Body.String())
}

// TestDownload_BrowserErrorPage: a second click on the download button of a burn file gets a
// page with the file text, not JSON.
func (s *FileHandlerSuite) TestDownload_BrowserErrorPage() {
	s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).Return(nil, domain.ErrNotFound)

	rec := s.serve(browserGet("/files/abc/download"))

	s.Equal(http.StatusNotFound, rec.Code)
	s.Contains(rec.Body.String(), "The file was deleted or its lifetime ended.")
}

func (s *FileHandlerSuite) TestEditFilePage() {
	file := fileRecord("a.txt", 3, 0, true)
	s.files.EXPECT().Peek(gomock.Any(), "abc").Return(file, nil)

	rec := s.serve(browserGet("/files/abc/edit"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(body, `id="fileName">a.txt<`)
	s.Contains(body, `id="saveBtn"`)
	s.Contains(body, `id="deleteBtn"`)
	s.Contains(body, `id="fileError"`)
	s.Contains(body, "required to save")
	s.Contains(body, `id="burnCheck" checked`)
	s.Contains(body, `value="keep"`)
	s.Contains(body, "headers['X-Edit-Code'] = code")
}

func (s *FileHandlerSuite) TestEditFilePage_Errors() {
	s.Run("not found", func() {
		s.files.EXPECT().Peek(gomock.Any(), "abc").Return(nil, domain.ErrNotFound)

		rec := s.serve(browserGet("/files/abc/edit"))

		s.Equal(http.StatusNotFound, rec.Code)
		s.Contains(rec.Body.String(), "The file was deleted or its lifetime ended.")
	})

	s.Run("expired", func() {
		file := fileRecord("a.txt", 3, 0, false)
		past := time.Now().Add(-time.Hour)
		file.ExpiresAt = &past
		s.files.EXPECT().Peek(gomock.Any(), "abc").Return(file, nil)

		rec := s.serve(browserGet("/files/abc/edit"))

		s.Equal(http.StatusGone, rec.Code)
		s.Contains(rec.Body.String(), "The file was deleted or its lifetime ended.")
	})
}

func (s *FileHandlerSuite) TestSuccessPage_File() {
	s.files.EXPECT().Info(gomock.Any(), "abc", access(false)).Return(fileRecord("a.txt", 2048, 0, true), nil)

	rec := s.serve(browserGet("/success?kind=file&id=abc"))
	body := rec.Body.String()

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(body, "Your file is live")
	s.Contains(body, "<strong>a.txt</strong><span>2 KiB</span>")
	s.Contains(body, `href="/files/abc/edit"`)
	s.Contains(body, "Deleted after the first download")
	s.Contains(body, "It is shown only once")
	s.Contains(body, "'padmark_file_edit_'")
}

// TestSuccessPage_FileLookupFails: without the record the page still shows the link (fail-soft).
func (s *FileHandlerSuite) TestSuccessPage_FileLookupFails() {
	s.files.EXPECT().Info(gomock.Any(), "abc", access(false)).Return(nil, domain.ErrStorageUnavailable)

	rec := s.serve(browserGet("/success?kind=file&id=abc"))

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "http://example.com/abc")
	s.NotContains(rec.Body.String(), `id="successFile"`)
}
