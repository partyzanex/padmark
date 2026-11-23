package web_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// FileSecuritySuite holds the checks of section 9 of the file plan: rate limit, download
// headers, cache headers, storage failures and log contents.
type FileSecuritySuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	manager     *MockNoteManager
	linkManager *MockLinkManager
	files       *MockFileManager
	auth        *MockAuthManager
	logs        *bytes.Buffer
}

func TestFileSecuritySuite(t *testing.T) {
	suite.Run(t, new(FileSecuritySuite))
}

func (s *FileSecuritySuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.files = NewMockFileManager(s.ctrl)
	s.auth = NewMockAuthManager(s.ctrl)
	s.logs = new(bytes.Buffer)
	s.files.EXPECT().MaxSize().Return(int64(fileTestMaxSize)).AnyTimes()
}

func (s *FileSecuritySuite) TearDownTest() {
	s.ctrl.Finish()
}

// routerOptions describes the router of one test.
type routerOptions struct {
	rateLimit int
	accounts  bool
}

// newRouter returns a router with files on. All log records at debug level and above go to
// s.logs.
func (s *FileSecuritySuite) newRouter(opts routerOptions) http.Handler {
	log := slog.New(slog.NewTextHandler(s.logs, &slog.HandlerOptions{
		AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil,
	}))

	handler := web.NewHandler(s.manager, s.linkManager, log)
	if opts.accounts {
		handler.WithAuthManager(s.auth)
	}

	handler.WithFileManager(s.files)

	ogen := web.NewOgenHandler(s.manager, s.linkManager, web.NoPinger{}, log)
	routerOpts := web.RouterOptions{
		MaxBodyBytes: 64 * 1024,
		CSRFSecret:   testCSRFSecret,
		RateLimit:    opts.rateLimit,
		RateBurst:    opts.rateLimit,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil,
		SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	}

	router, err := web.NewRouter(handler, ogen, &routerOpts)
	s.Require().NoError(err)

	return router
}

func serveOnce(router http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func uploadedFile() *domain.File {
	file := fileRecord("a.txt", 1, 0, false)
	file.EditCode = "plain-code"

	return file
}

// TestRateLimit_CoversUpload: the general per-IP limit applies to POST /files. The second
// upload in the same second gets 429, and the usecase does not see it.
func (s *FileSecuritySuite) TestRateLimit_CoversUpload() {
	router := s.newRouter(routerOptions{rateLimit: 1, accounts: false})

	s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).
		Return(uploadedFile(), nil).Times(1)

	first := serveOnce(router, uploadRequest(nil, "a.txt", []byte("x")))
	s.Require().Equal(http.StatusCreated, first.Code, first.Body.String())

	second := serveOnce(router, uploadRequest(nil, "a.txt", []byte("x")))
	s.Equal(http.StatusTooManyRequests, second.Code)
}

// TestAnonymousUpload_LikeLinks: with accounts on, an anonymous POST /files reaches the
// usecase, as an anonymous POST /links does. The upload right is one rule for both.
func (s *FileSecuritySuite) TestAnonymousUpload_LikeLinks() {
	router := s.newRouter(routerOptions{rateLimit: 0, accounts: true})

	s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ io.Reader, opts files.CreateOptions) (*domain.File, error) {
			s.Nil(opts.OwnerID)

			return uploadedFile(), nil
		})
	s.linkManager.EXPECT().Create(gomock.Any(), "https://example.com/dest", gomock.Any()).
		Return(newTestLink(), nil)

	fileRec := serveOnce(router, uploadRequest(nil, "a.txt", []byte("x")))
	s.Equal(http.StatusCreated, fileRec.Code, fileRec.Body.String())

	linkReq := httptest.NewRequest(http.MethodPost, "/links",
		strings.NewReader(`{"target_url":"https://example.com/dest"}`))
	linkReq.Header.Set("Content-Type", "application/json")

	linkRec := serveOnce(router, linkReq)
	s.Equal(http.StatusCreated, linkRec.Code, linkRec.Body.String())
}

// TestDownload_ContentDisposition: the header has an ASCII filename and the full UTF-8 name in
// filename*. A name with a quote, a semicolon, a backslash or a line break cannot add a
// parameter or a header, and a standard parser gets the original name back.
func (s *FileSecuritySuite) TestDownload_ContentDisposition() {
	router := s.newRouter(routerOptions{rateLimit: 0, accounts: false})

	tests := []struct {
		name     string
		fileName string
		want     string
	}{
		{
			name: "cyrillic", fileName: "отчёт.pdf",
			want: `attachment; filename="_____.pdf"; filename*=UTF-8''%D0%BE%D1%82%D1%87%D1%91%D1%82.pdf`,
		},
		{
			name: "ascii", fileName: "report-1.pdf",
			want: `attachment; filename="report-1.pdf"; filename*=UTF-8''report-1.pdf`,
		},
		{
			name: "quote", fileName: `a".pdf`,
			want: `attachment; filename="a_.pdf"; filename*=UTF-8''a%22.pdf`,
		},
		{
			name: "semicolon", fileName: "a; filename=evil.html",
			want: `attachment; filename="a; filename=evil.html"; filename*=UTF-8''a%3B%20filename%3Devil.html`,
		},
		{
			name: "backslash", fileName: `a\b.pdf`,
			want: `attachment; filename="a_b.pdf"; filename*=UTF-8''a%5Cb.pdf`,
		},
		{
			name: "line break", fileName: "a\r\nX-Injected: 1.pdf",
			want: `attachment; filename="a__X-Injected: 1.pdf"; filename*=UTF-8''a%0D%0AX-Injected%3A%201.pdf`,
		},
	}

	for _, test := range tests {
		s.Run(test.name, func() {
			s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).
				Return(downloadOf(test.fileName, "data", false), nil)

			req := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
			req.Header.Set("Accept", "application/octet-stream")
			rec := serveOnce(router, req)

			s.Require().Equal(http.StatusOK, rec.Code)

			disposition := rec.Header().Get("Content-Disposition")
			s.Equal(test.want, disposition)
			s.NotContains(disposition, "\r")
			s.NotContains(disposition, "\n")
			s.Empty(rec.Header().Get("X-Injected"))

			kind, params, err := mime.ParseMediaType(disposition)
			s.Require().NoError(err)
			s.Equal("attachment", kind)
			s.Equal(test.fileName, params["filename"])
		})
	}
}

// TestDownload_HeadersStopSniffAndCache: GET and HEAD of a download have nosniff and
// "private, no-store", so a browser does not run the content and a shared cache keeps nothing.
func (s *FileSecuritySuite) TestDownload_HeadersStopSniffAndCache() {
	router := s.newRouter(routerOptions{rateLimit: 0, accounts: false})

	s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).
		Return(downloadOf("page.html", "<script>", false), nil)
	s.files.EXPECT().Inspect(gomock.Any(), "abc", gomock.Any()).
		Return(fileRecord("page.html", 8, 0, false), nil)

	get := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
	get.Header.Set("Accept", "application/octet-stream")

	for _, req := range []*http.Request{get, httptest.NewRequest(http.MethodHead, "/files/abc/download", nil)} {
		rec := serveOnce(router, req)

		s.Require().Equal(http.StatusOK, rec.Code, req.Method)
		s.Equal("application/octet-stream", rec.Header().Get("Content-Type"), req.Method)
		s.Equal("nosniff", rec.Header().Get("X-Content-Type-Options"), req.Method)
		s.Equal("private, no-store", rec.Header().Get("Cache-Control"), req.Method)
	}
}

// TestFilePages_NotCached: the file page on /{id} and the edit page show the file name; a
// shared cache must not keep them. They have the header of the other private pages.
func (s *FileSecuritySuite) TestFilePages_NotCached() {
	router := s.newRouter(routerOptions{rateLimit: 0, accounts: false})
	file := fileRecord("a.txt", 3, 0, false)

	// The note read tells that a file has the slug for both requests: GET /abc shows the file
	// page, and GET /edit/abc opens the file editor.
	expectOtherKind(s.manager, "abc", domain.KindFile)
	expectOtherKindEditor(s.manager, "abc", domain.KindFile)
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(file, nil)
	s.files.EXPECT().Settings(gomock.Any(), "abc", access(false)).Return(file, nil)

	for _, path := range []string{"/abc", "/edit/abc"} {
		rec := serveOnce(router, browserGet(path))

		s.Require().Equal(http.StatusOK, rec.Code, path)
		s.Equal("private, no-store", rec.Header().Get("Cache-Control"), path)
		s.Equal("nosniff", rec.Header().Get("X-Content-Type-Options"), path)
	}
}

// TestStorageUnavailable_Is503: a storage failure at run time gives 503 on each file route,
// and the response does not show the internal error.
func (s *FileSecuritySuite) TestStorageUnavailable_Is503() {
	router := s.newRouter(routerOptions{rateLimit: 0, accounts: false})
	storageErr := fmt.Errorf("check file object: %w",
		errors.Join(domain.ErrStorageUnavailable, errors.New("dial tcp 10.9.8.7:9000: connection refused")))

	s.files.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, storageErr)
	s.files.EXPECT().Download(gomock.Any(), "abc", gomock.Any()).Return(nil, storageErr).Times(2)
	s.files.EXPECT().Inspect(gomock.Any(), "abc", gomock.Any()).Return(nil, storageErr).Times(2)
	s.files.EXPECT().Delete(gomock.Any(), "abc", "code-1234", access(false)).Return(storageErr)
	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), gomock.Any()).Return(nil, storageErr)

	download := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
	download.Header.Set("Accept", "application/octet-stream")

	remove := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
	remove.Header.Set("X-Edit-Code", "code-1234")

	tests := []struct {
		req  *http.Request
		name string
	}{
		{name: "upload", req: uploadRequest(nil, "a.txt", []byte("x"))},
		{name: "download", req: download},
		{name: "download page", req: browserGet("/files/abc/download")},
		{name: "head", req: httptest.NewRequest(http.MethodHead, "/files/abc/download", nil)},
		{name: "info", req: httptest.NewRequest(http.MethodGet, "/files/abc", nil)},
		{name: "delete", req: remove},
		{name: "update", req: updateRequest(`{"burn_after_download":true}`, "code-1234")},
	}

	for _, tc := range tests {
		rec := serveOnce(router, tc.req)

		s.Equal(http.StatusServiceUnavailable, rec.Code, tc.name)
		s.NotContains(rec.Body.String(), "10.9.8.7", tc.name)
	}
}

// TestLogs_NoFileSecrets: the log records of the file routes do not contain the slug, the file
// name or the edit code. A failed stream and failed mutations write log records.
func (s *FileSecuritySuite) TestLogs_NoFileSecrets() {
	const (
		slug     = "slug-secret-xyz"
		fileName = "name-secret.pdf"
		editCode = "edit-secret-code"
	)

	router := s.newRouter(routerOptions{rateLimit: 0, accounts: false})
	failing := files.NewDownload(fileRecord(fileName, 10, 0, false),
		func(context.Context, io.Writer) error { return errors.New("stream stopped") })

	s.files.EXPECT().Download(gomock.Any(), slug, access(true)).Return(failing, nil)
	s.files.EXPECT().Delete(gomock.Any(), slug, editCode, access(false)).Return(errors.New("database is locked"))
	s.files.EXPECT().Update(gomock.Any(), slug, editCode, access(false), gomock.Any()).
		Return(nil, errors.New("database is locked"))
	s.files.EXPECT().Inspect(gomock.Any(), slug, gomock.Any()).Return(nil, errors.New("metadata is locked"))

	download := httptest.NewRequest(http.MethodGet, "/files/"+slug+"/download", nil)
	download.Header.Set("Accept", "application/octet-stream")
	serveOnce(router, download)

	remove := httptest.NewRequest(http.MethodDelete, "/files/"+slug, nil)
	remove.Header.Set("X-Edit-Code", editCode)
	s.Equal(http.StatusInternalServerError, serveOnce(router, remove).Code)

	update := httptest.NewRequest(http.MethodPut, "/files/"+slug, strings.NewReader(`{}`))
	update.Header.Set("Content-Type", "application/json")
	update.Header.Set("X-Edit-Code", editCode)
	s.Equal(http.StatusInternalServerError, serveOnce(router, update).Code)

	s.Equal(http.StatusInternalServerError,
		serveOnce(router, httptest.NewRequest(http.MethodGet, "/files/"+slug, nil)).Code)

	logs := s.logs.String()
	s.Require().Contains(logs, "send file")
	s.Require().Contains(logs, "update file failed")
	// R-47: Metadata and Delete log the unknown errors behind a 500.
	s.Require().Contains(logs, "metadata is locked")
	s.Require().Contains(logs, "database is locked")
	s.Require().Contains(logs, "file request failed")
	s.NotContains(logs, slug)
	s.NotContains(logs, fileName)
	s.NotContains(logs, editCode)
}

// TestLogs_MalformedUploadIsQuiet (R-08): a damaged multipart body gets 400 and writes no error
// record. No record contains the field values from the body, such as an edit code.
func (s *FileSecuritySuite) TestLogs_MalformedUploadIsQuiet() {
	const (
		boundary = "XBOUNDARY"
		editCode = "edit-secret-code"
	)

	router := s.newRouter(routerOptions{rateLimit: 0, accounts: false})
	body := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"edit_code\"\r\n\r\n" + editCode +
		"\r\n--" + boundary + "\r\nbroken header " + editCode + "\r\n\r\nx\r\n"

	req := httptest.NewRequest(http.MethodPost, "/files", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

	rec := serveOnce(router, req)

	s.Equal(http.StatusBadRequest, rec.Code)
	s.NotContains(rec.Body.String(), editCode)
	s.NotContains(s.logs.String(), editCode)
	s.NotContains(s.logs.String(), "level=ERROR")
}

// TestLogs_CanceledUploadIsDebug (R-08): a canceled upload writes only a debug record.
func (s *FileSecuritySuite) TestLogs_CanceledUploadIsDebug() {
	router := s.newRouter(routerOptions{rateLimit: 0, accounts: false})
	ctx, cancel := context.WithCancel(s.T().Context())

	s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, string, io.Reader, files.CreateOptions) (*domain.File, error) {
			cancel()

			return nil, fmt.Errorf("upload file: %w", context.Canceled)
		})

	serveOnce(router, uploadRequest(nil, "a.txt", []byte("x")).WithContext(ctx))

	s.Contains(s.logs.String(), "file upload canceled")
	s.NotContains(s.logs.String(), "level=ERROR")
	s.NotContains(s.logs.String(), "level=WARN")
}
