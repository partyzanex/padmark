package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
	// auth is the account system of newAuthRouter.
	auth   *MockAuthManager
	router http.Handler
}

func TestFileHandlerSuite(t *testing.T) {
	suite.Run(t, new(FileHandlerSuite))
}

func (s *FileHandlerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.linkManager = NewMockLinkManager(s.ctrl)
	s.files = NewMockFileManager(s.ctrl)
	s.auth = NewMockAuthManager(s.ctrl)
	s.files.EXPECT().MaxSize().Return(int64(fileTestMaxSize)).AnyTimes()
	s.router = s.newRouter(true)
}

func (s *FileHandlerSuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *FileHandlerSuite) newRouter(withFiles bool) http.Handler {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, web.NoPinger{})
	handlerCfg.CSRFSecret = testCSRFSecret

	if withFiles {
		handlerCfg.Files = s.files
	}

	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 64 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
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

// access returns the access options of an anonymous reader on a router without accounts: the
// privacy check is off, as for notes and links.
func access(allowBurn bool) domain.Reader {
	return domain.Reader{CallerID: uuid.Nil, Authenticated: false, AllowBurn: allowBurn, SkipPrivacy: true}
}

// accountAccess returns the access options of an anonymous reader on a router with accounts:
// the privacy check applies.
func accountAccess(allowBurn bool) domain.Reader {
	return domain.Reader{CallerID: uuid.Nil, Authenticated: false, AllowBurn: allowBurn, SkipPrivacy: false}
}

// TestRootSlugServesFile: a client that asks for binary content gets the file on /{id}. A
// browser gets the file page instead (TestFilePage_BrowserGetsPage).
func (s *FileHandlerSuite) TestRootSlugServesFile() {
	s.expectFileKind()
	s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).
		Return(downloadOf("a.txt", "hello", true), nil)

	req := httptest.NewRequest(http.MethodGet, "/abc", nil)
	req.Header.Set("Accept", "application/octet-stream")
	rec := s.serve(req)

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Equal("hello", rec.Body.String())
}

// TestRootSlugWithoutAnyResource: a slug that names no record gets 404 after the note read,
// without a link or file Manager call.
func (s *FileHandlerSuite) TestRootSlugWithoutAnyResource() {
	expectNoteReadError(s.manager, "abc", gomock.Any(), domain.ErrNotFound)

	req := httptest.NewRequest(http.MethodGet, "/abc", nil)
	s.Equal(http.StatusNotFound, s.serve(req).Code)
}

// TestRootSlug_FileNotFound_FileAnswer: a file slug whose record the file Manager does not find
// (a parallel delete, or a burn download in progress) gets the 404 of the file pages.
func (s *FileHandlerSuite) TestRootSlug_FileNotFound_FileAnswer() {
	s.expectFileKind()
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(nil, domain.ErrNotFound)

	rec := s.serve(browserGet("/abc"))

	s.Equal(http.StatusNotFound, rec.Code)
	s.Contains(rec.Body.String(), "The file was deleted or its lifetime ended.")
}

func (s *FileHandlerSuite) TestMetadata() {
	s.files.EXPECT().Inspect(gomock.Any(), "abc", gomock.Any()).
		Return(fileRecord("a.txt", 3, 2, false), nil)

	rec := s.serve(httptest.NewRequest(http.MethodGet, "/files/abc", nil))
	s.Require().Equal(http.StatusOK, rec.Code)

	var body map[string]any
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	s.Equal("a.txt", body["name"])
	s.InDelta(2, body["downloads"], 0)
	s.NotContains(body, "edit_code")

	s.files.EXPECT().Inspect(gomock.Any(), "gone", gomock.Any()).Return(nil, domain.ErrExpired)
	s.Equal(http.StatusGone, s.serve(httptest.NewRequest(http.MethodGet, "/files/gone", nil)).Code)
}

func (s *FileHandlerSuite) TestDelete() {
	s.files.EXPECT().Delete(gomock.Any(), "abc", "code-1234", access(false)).Return(nil)

	req := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
	req.Header.Set("X-Edit-Code", "code-1234")
	s.Equal(http.StatusNoContent, s.serve(req).Code)

	s.files.EXPECT().Delete(gomock.Any(), "abc", "wrong", access(false)).Return(domain.ErrInvalidEditCode)

	req = httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
	req.Header.Set("X-Edit-Code", "wrong")
	s.Equal(http.StatusForbidden, s.serve(req).Code)
}

// newAuthRouter returns a router with files and accounts on. A request without credentials is
// anonymous, so the tests set AuthManager expectations only for the credentials that they send.
func (s *FileHandlerSuite) newAuthRouter(auth *MockAuthManager) http.Handler {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, web.NoPinger{})
	handlerCfg.Accounts = auth
	handlerCfg.Files = s.files
	handlerCfg.CSRFSecret = testCSRFSecret
	handler := newTestHandler(s.T(), handlerCfg)

	opts := web.RouterOptions{
		MaxBodyBytes: 64 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}

	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	return router
}

// expectFileKind makes the note read of GET /abc tell that a file has the slug: the handler then
// calls only the file Manager, and the strict mocks fail on a link read.
func (s *FileHandlerSuite) expectFileKind() {
	expectOtherKind(s.manager, "abc", domain.KindFile)
}

func browserGet(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Sec-Fetch-Dest", "document")

	return req
}

// TestRootSlug_WildcardDownloads: curl without flags sends */*; it keeps getting the content.
func (s *FileHandlerSuite) TestRootSlug_WildcardDownloads() {
	s.expectFileKind()
	s.files.EXPECT().Download(gomock.Any(), "abc", access(false)).
		Return(downloadOf("a.txt", "hello", false), nil)

	req := httptest.NewRequest(http.MethodGet, "/abc", nil)
	req.Header.Set("Accept", "*/*")
	rec := s.serve(req)

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Equal("hello", rec.Body.String())
}

func (s *FileHandlerSuite) serveWith(router http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// testUser returns a user with the given ID.
func testUser(userID uuid.UUID) *domain.User {
	return &domain.User{
		ID: userID, Username: "user-" + userID.String()[:8],
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
}

// TestRootSlug_HeadFile: HEAD /{id} of a file sends the download headers without a body and
// without a download (no Download call).
func (s *FileHandlerSuite) TestRootSlug_HeadFile() {
	s.expectFileKind()
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(fileRecord("a.txt", 5, 0, true), nil)

	req := httptest.NewRequest(http.MethodHead, "/abc", nil)
	req.Header.Set("Accept", "application/octet-stream")
	rec := s.serve(req)

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("5", rec.Header().Get("Content-Length"))
	s.Contains(rec.Header().Get("Content-Disposition"), "attachment")
	s.Empty(rec.Body.String())
}
