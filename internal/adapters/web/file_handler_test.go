package web_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
	"github.com/partyzanex/padmark/internal/usecases/slugs"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

const fileTestMaxSize = 1 << 20

type FileHandlerSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	manager     *MockNoteManager
	linkManager *MockLinkManager
	slugs       *MockSlugResolver
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
	s.slugs = NewMockSlugResolver(s.ctrl)
	s.files = NewMockFileManager(s.ctrl)
	s.files.EXPECT().MaxSize().Return(int64(fileTestMaxSize)).AnyTimes()
	s.router = s.newRouter(true)
}

func (s *FileHandlerSuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *FileHandlerSuite) newRouter(withFiles bool) http.Handler {
	handler := web.NewHandler(s.manager, s.linkManager, s.slugs, discardLog())
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

// uploadFields returns the five text fields of the upload form.
func uploadFields() [][2]string {
	return [][2]string{
		{"ttl", "60"}, {"privacy", "public"}, {"burn_after_download", "false"},
		{"slug", "all-fields"}, {"edit_code", "my-edit-code"},
	}
}

func (s *FileHandlerSuite) TestUpload_AllFields() {
	s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, name string, body io.Reader, opts files.CreateOptions) (*domain.File, error) {
			got, err := io.ReadAll(body)
			s.Require().NoError(err)
			s.Equal("content", string(got))
			s.Require().NotNil(opts.ExpiresAt)
			s.Require().NotNil(opts.Privacy)
			s.Equal(domain.Privacy("public"), *opts.Privacy)
			s.False(opts.BurnAfterDownload)
			s.Equal("all-fields", opts.CustomSlug)
			s.Equal("my-edit-code", opts.EditCode)

			file := fileRecord(name, int64(len(got)), 0, false)
			file.ID = "all-fields"

			return file, nil
		})

	rec := s.serve(uploadRequest(uploadFields(), "a.txt", []byte("content")))
	s.Equal(http.StatusCreated, rec.Code, rec.Body.String())
}

// TestUpload_FileAboveLimit: the file part keeps the full upload limit after the small limit
// of the fields.
func (s *FileHandlerSuite) TestUpload_FileAboveLimit() {
	const multipartOverhead = 1 << 20

	s.files.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, body io.Reader, _ files.CreateOptions) (*domain.File, error) {
			_, err := io.Copy(io.Discard, body)

			return nil, err
		})

	content := bytes.Repeat([]byte("a"), fileTestMaxSize+multipartOverhead+1)
	req := uploadRequest(uploadFields(), "a.txt", content)
	// An unknown length (a chunked body) skips the Content-Length check, so the body limit
	// stops the read.
	req.ContentLength = -1
	rec := s.serve(req)
	s.Equal(http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
}

// TestUpload_FormFieldRules: the handler accepts only the known fields, each one time, and a
// small amount of data before the file part. The manager is not called (strict mock).
func (s *FileHandlerSuite) TestUpload_FormFieldRules() {
	s.Run("unknown field", func() {
		rec := s.serve(uploadRequest([][2]string{{"ttl", "60"}, {"other", "x"}}, "a.txt", []byte("x")))
		s.Equal(http.StatusBadRequest, rec.Code)
		s.Contains(rec.Body.String(), "unknown field")
		s.NotContains(rec.Body.String(), "other")
	})

	s.Run("repeated field", func() {
		rec := s.serve(uploadRequest([][2]string{{"slug", "one"}, {"slug", "two"}}, "a.txt", []byte("x")))
		s.Equal(http.StatusBadRequest, rec.Code)
		s.Contains(rec.Body.String(), "slug is repeated")
	})

	s.Run("more parts than known fields", func() {
		fields := append(uploadFields(), [2]string{"ttl", "10"})
		rec := s.serve(uploadRequest(fields, "a.txt", []byte("x")))
		s.Equal(http.StatusBadRequest, rec.Code)
	})

	s.Run("part without a name", func() {
		rec := s.serve(rawUploadRequest(nil, func(writer *multipart.Writer) {
			_, _ = writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain"}})
		}))
		s.Equal(http.StatusBadRequest, rec.Code)
	})
}

func (s *FileHandlerSuite) TestUpload_FormSizeLimit() {
	const fieldsLimit = 64 << 10

	s.Run("long field name stops the read", func() {
		name := strings.Repeat("n", 1<<20)
		body := &countingReader{reader: nil, read: 0}
		req := rawUploadRequest(body, func(writer *multipart.Writer) {
			_ = writer.WriteField(name, "x")
		})

		rec := s.serve(req)
		s.Equal(http.StatusBadRequest, rec.Code)
		s.Contains(rec.Body.String(), "too large")
		s.NotContains(rec.Body.String(), "nnnn")
		s.LessOrEqual(body.read, int64(fieldsLimit), "the handler reads only the field limit")
	})

	s.Run("long header of a known field", func() {
		rec := s.serve(rawUploadRequest(nil, func(writer *multipart.Writer) {
			part, _ := writer.CreatePart(textproto.MIMEHeader{
				"Content-Disposition": {`form-data; name="ttl"`},
				"X-Padding":           {strings.Repeat("p", fieldsLimit)},
			})
			_, _ = part.Write([]byte("60"))
		}))
		s.Equal(http.StatusBadRequest, rec.Code)
	})

	s.Run("long preamble", func() {
		rec := s.serve(rawUploadRequest(nil, func(*multipart.Writer) {}, preambleOf(fieldsLimit)))
		s.Equal(http.StatusBadRequest, rec.Code)
	})

	s.Run("data under the limit", func() {
		s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, name string, body io.Reader, _ files.CreateOptions) (*domain.File, error) {
				got, err := io.ReadAll(body)
				s.Require().NoError(err)
				s.Equal("content", string(got))

				return fileRecord(name, int64(len(got)), 0, false), nil
			})

		// Half of the limit before the fields, plus the read-ahead into the file data.
		preamble := preambleOf(fieldsLimit / 2)
		rec := s.serve(rawUploadRequest(nil, func(writer *multipart.Writer) {
			for _, field := range uploadFields() {
				_ = writer.WriteField(field[0], field[1])
			}

			part, _ := writer.CreateFormFile("file", "a.txt")
			_, _ = part.Write([]byte("content"))
		}, preamble))
		s.Equal(http.StatusCreated, rec.Code, rec.Body.String())
	})
}

// rawUploadRequest builds an upload body with write and an optional preamble. If counter is
// not nil, it counts the bytes that the server reads from the body.
func rawUploadRequest(counter *countingReader, write func(*multipart.Writer), preamble ...string) *http.Request {
	var body bytes.Buffer

	for _, text := range preamble {
		body.WriteString(text)
	}

	writer := multipart.NewWriter(&body)
	write(writer)
	_ = writer.Close()

	var reader io.Reader = &body
	if counter != nil {
		counter.reader = &body
		reader = counter
	}

	req := httptest.NewRequest(http.MethodPost, "/files", reader)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	return req
}

// preambleOf returns size bytes of short text lines. The multipart reader skips the lines
// before the first boundary, but they count for the limit of the fields.
func preambleOf(size int) string {
	line := strings.Repeat("p", 62) + "\r\n"

	return strings.Repeat(line, size/len(line)+1)
}

// countingReader counts the bytes read from reader.
type countingReader struct {
	reader io.Reader
	read   int64
}

func (c *countingReader) Read(buf []byte) (int, error) {
	n, err := c.reader.Read(buf)
	c.read += int64(n)

	return n, err
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
	s.files.EXPECT().Inspect(gomock.Any(), "abc", gomock.Any()).
		Return(fileRecord("a.txt", 3, 0, false), nil)

	rec := s.serve(httptest.NewRequest(http.MethodHead, "/files/abc/download", nil))

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("3", rec.Header().Get("Content-Length"))
	s.Empty(rec.Body.String())
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

// TestRootSlugWithoutAnyResource: a slug that names no record gets 404 without a Manager call.
func (s *FileHandlerSuite) TestRootSlugWithoutAnyResource() {
	s.slugs.EXPECT().KindOf(gomock.Any(), "abc").Return(slugs.Kind(""), domain.ErrNotFound)

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
	handler := web.NewHandler(s.manager, s.linkManager, s.slugs, discardLog()).WithAuthManager(auth)
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

	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, opts files.UpdateOptions) (*domain.File, error) {
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
	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), files.UpdateOptions{
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
	s.files.EXPECT().Update(gomock.Any(), "abc", "", signedInReader(owner.ID), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, opts files.UpdateOptions) (*domain.File, error) {
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

	// The usecase finds that the caller cannot see the file; the handler answers 401.
	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", anonymousReader(), gomock.Any()).
		Return(nil, domain.ErrForbidden)

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
			s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), gomock.Any()).
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
	s.files.EXPECT().Update(gomock.Any(), "abc", "wrong", access(false), gomock.Any()).
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

// expectFileKind makes the resolver give a file for the slug "abc": GET /{id} and GET /edit/{id}
// then call only the file Manager, and the strict mocks fail on a note or link read.
func (s *FileHandlerSuite) expectFileKind() {
	s.slugs.EXPECT().KindOf(gomock.Any(), "abc").Return(slugs.KindFile, nil)
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
	s.Contains(body, `<span id="fileLimit">Up to 1 MiB</span>`)
	s.Contains(body, `data-max-size="1048576"`)
	s.Contains(body, `<a href="/files" class="nav-tab" aria-current="page">`)
	s.Contains(body, "<span>Padmark — notes, links &amp; files</span>")
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

// TestDownload_BrowserErrorPage: a second click on the download button of a burn file gets a
// page with the file text, not JSON.
func (s *FileHandlerSuite) TestDownload_BrowserErrorPage() {
	s.files.EXPECT().Download(gomock.Any(), "abc", access(false)).Return(nil, domain.ErrNotFound)

	rec := s.serve(browserGet("/files/abc/download"))

	s.Equal(http.StatusNotFound, rec.Code)
	s.Contains(rec.Body.String(), "The file was deleted or its lifetime ended.")
}

// expectEditFile sets the reads of GET /edit/abc for a file: the resolver gives a file, then the
// file editor reads the file with Settings, which applies privacy and expiry and never deletes an
// expired file.
func (s *FileHandlerSuite) expectEditFile(file *domain.File, err error) {
	s.expectFileKind()
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

// TestSuccessPage_FileLookupFails: without the record the page still shows the link (fail-soft).
func (s *FileHandlerSuite) TestSuccessPage_FileLookupFails() {
	s.files.EXPECT().Inspect(gomock.Any(), "abc", access(false)).Return(nil, domain.ErrStorageUnavailable)

	rec := s.serve(browserGet("/success?kind=file&id=abc"))

	s.Require().Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), "http://example.com/abc")
	s.NotContains(rec.Body.String(), `id="successFile"`)
}

// --- upload body rules ---

// readAllAsUsecase reads body as the usecase does and wraps a read error the same way.
func readAllAsUsecase(_ context.Context, name string, body io.Reader, _ files.CreateOptions) (*domain.File, error) {
	got, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}

	return fileRecord(name, int64(len(got)), 0, false), nil
}

// TestUpload_PartAfterFile (R-03): a field or a second file after the file part fails the
// upload with 400. The error comes from the body read, so the usecase fails and deletes the
// object itself.
func (s *FileHandlerSuite) TestUpload_PartAfterFile() {
	cases := map[string]func(*multipart.Writer){
		"field after file": func(writer *multipart.Writer) {
			part, _ := writer.CreateFormFile("file", "a.txt")
			_, _ = part.Write([]byte("content"))
			_ = writer.WriteField("privacy", "owner")
		},
		"second file": func(writer *multipart.Writer) {
			for range 2 {
				part, _ := writer.CreateFormFile("file", "a.txt")
				_, _ = part.Write([]byte("content"))
			}
		},
	}

	for name, write := range cases {
		s.Run(name, func() {
			s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).DoAndReturn(readAllAsUsecase)

			rec := s.serve(rawUploadRequest(nil, write))

			s.Equal(http.StatusBadRequest, rec.Code)
			s.Contains(rec.Body.String(), `the \"file\" part must be the last part of the form`)
			s.Equal("close", rec.Header().Get("Connection"))
		})
	}
}

// TestUpload_EpilogueAfterFile: text after the closing boundary is not a part, so the upload
// succeeds.
func (s *FileHandlerSuite) TestUpload_EpilogueAfterFile() {
	s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).DoAndReturn(readAllAsUsecase)

	req := uploadRequest(nil, "a.txt", []byte("content"))
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), strings.NewReader("epilogue\r\n")))
	req.ContentLength = -1

	s.Equal(http.StatusCreated, s.serve(req).Code)
}

// TestUpload_ContentLengthAboveLimit (R-04): a declared body above the limit gets 413 before the
// handler reads it. The response closes the connection, so the server does not read the body.
func (s *FileHandlerSuite) TestUpload_ContentLengthAboveLimit() {
	const multipartOverhead = 1 << 20

	counter := &countingReader{reader: nil, read: 0}
	req := rawUploadRequest(counter, func(writer *multipart.Writer) {
		part, _ := writer.CreateFormFile("file", "a.txt")
		_, _ = part.Write([]byte("x"))
	})
	req.ContentLength = fileTestMaxSize + multipartOverhead + 1

	rec := s.serve(req)

	s.Equal(http.StatusRequestEntityTooLarge, rec.Code)
	s.Equal("close", rec.Header().Get("Connection"))
	s.Zero(counter.read)
}

// TestUpload_EarlyErrorsCloseConnection: each upload error that can leave the body unread
// closes the connection.
func (s *FileHandlerSuite) TestUpload_EarlyErrorsCloseConnection() {
	rec := s.serve(uploadRequest([][2]string{{"other", "x"}}, "a.txt", []byte("x")))

	s.Equal(http.StatusBadRequest, rec.Code)
	s.Equal("close", rec.Header().Get("Connection"))
}

// TestUpload_MalformedBody (R-08): a damaged or cut multipart body gets 400 with a constant
// text, also when the parser error quotes form data.
func (s *FileHandlerSuite) TestUpload_MalformedBody() {
	const boundary = "XBOUNDARY"

	cases := map[string]string{
		"bad part header": "--" + boundary + "\r\nContent-Disposition form-data name=\"ttl\" secret-value\r\n\r\n60\r\n",
		"cut field":       "--" + boundary + "\r\nContent-Disposition: form-data; name=\"ttl\"\r\n\r\n60",
	}

	for name, body := range cases {
		s.Run(name, func() {
			req := httptest.NewRequest(http.MethodPost, "/files", strings.NewReader(body))
			req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

			rec := s.serve(req)

			s.Equal(http.StatusBadRequest, rec.Code)
			s.Contains(rec.Body.String(), "the multipart body is malformed or incomplete")
			s.NotContains(rec.Body.String(), "secret-value")
		})
	}

	s.Run("cut file part", func() {
		s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).DoAndReturn(readAllAsUsecase)

		body := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n\r\ncontent"
		req := httptest.NewRequest(http.MethodPost, "/files", strings.NewReader(body))
		req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

		rec := s.serve(req)

		s.Equal(http.StatusBadRequest, rec.Code)
		s.Contains(rec.Body.String(), "the multipart body is malformed or incomplete")
	})
}

// TestUpload_Canceled (R-08): when the client goes away, the handler writes no response.
func (s *FileHandlerSuite) TestUpload_Canceled() {
	ctx, cancel := context.WithCancel(s.T().Context())

	s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, string, io.Reader, files.CreateOptions) (*domain.File, error) {
			cancel()

			return nil, fmt.Errorf("upload file: %w", context.Canceled)
		})

	rec := s.serve(uploadRequest(nil, "a.txt", []byte("x")).WithContext(ctx))

	s.False(rec.Flushed)
	s.Empty(rec.Body.String())
	s.Empty(rec.Header().Get("Content-Type"))
}

// newLimitedRouter returns a router with files on and the given transfer limits.
func (s *FileHandlerSuite) newLimitedRouter(limits web.FileTransferLimits) http.Handler {
	handler := web.NewHandler(s.manager, s.linkManager, s.slugs, discardLog()).WithFileManager(s.files)
	handler.WithFileTransferLimits(limits)

	ogen := web.NewOgenHandler(s.manager, s.linkManager, web.NoPinger{}, discardLog())
	opts := web.RouterOptions{
		MaxBodyBytes: 64 * 1024, CSRFSecret: testCSRFSecret,
		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	}

	router, err := web.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	return router
}

// TestUpload_ConcurrencyLimit (R-04): the server limit gives 503 and the per-client limit gives
// 429, both with Retry-After. A finished upload frees its slot.
func (s *FileHandlerSuite) TestUpload_ConcurrencyLimit() {
	router := s.newLimitedRouter(web.FileTransferLimits{IdleTimeout: 0, MaxUploads: 2, MaxUploadsPerClient: 1})

	entered := make(chan struct{}, 2)
	release := make(chan struct{})

	s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, name string, body io.Reader, opts files.CreateOptions) (*domain.File, error) {
			entered <- struct{}{}

			<-release

			return readAllAsUsecase(ctx, name, body, opts)
		}).Times(2)

	fromClient := func(addr string) *http.Request {
		req := uploadRequest(nil, "a.txt", []byte("x"))
		req.RemoteAddr = addr

		return req
	}

	var wg sync.WaitGroup

	start := func(addr string) {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, fromClient(addr))
			s.Equal(http.StatusCreated, rec.Code)
		})
		<-entered
	}

	start("192.0.2.1:1000")

	sameClient := s.serveWith(router, fromClient("192.0.2.1:2000"))
	s.Equal(http.StatusTooManyRequests, sameClient.Code)
	s.Equal("10", sameClient.Header().Get("Retry-After"))
	s.Equal("close", sameClient.Header().Get("Connection"))

	start("192.0.2.2:1000")

	otherClient := s.serveWith(router, fromClient("192.0.2.3:1000"))
	s.Equal(http.StatusServiceUnavailable, otherClient.Code)
	s.Equal("10", otherClient.Header().Get("Retry-After"))

	close(release)
	wg.Wait()

	s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).DoAndReturn(readAllAsUsecase)
	s.Equal(http.StatusCreated, s.serveWith(router, fromClient("192.0.2.1:3000")).Code)
}

func (s *FileHandlerSuite) serveWith(router http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestUpload_IdleTimeout (R-04): an upload that stops sending data gets 408 after the idle
// timeout, not after the total limit. The test uses a real connection, because only a real
// connection has read deadlines.
func (s *FileHandlerSuite) TestUpload_IdleTimeout() {
	router := s.newLimitedRouter(web.FileTransferLimits{
		IdleTimeout: 200 * time.Millisecond, MaxUploads: 0, MaxUploadsPerClient: 0,
	})

	server := httptest.NewServer(router)
	defer server.Close()

	s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).DoAndReturn(readAllAsUsecase)

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	s.Require().NoError(err)

	defer conn.Close()

	const boundary = "XBOUNDARY"

	body := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n\r\nstart"
	request := "POST /files HTTP/1.1\r\nHost: " + server.Listener.Addr().String() + "\r\n" +
		"Content-Type: multipart/form-data; boundary=" + boundary + "\r\n" +
		"Content-Length: 100000\r\n\r\n" + body

	_, err = io.WriteString(conn, request)
	s.Require().NoError(err)
	s.Require().NoError(conn.SetReadDeadline(time.Now().Add(10 * time.Second)))

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	s.Require().NoError(err)

	defer resp.Body.Close()

	s.Equal(http.StatusRequestTimeout, resp.StatusCode)
}

// --- download failures ---

// failingDownload returns a download whose Send writes prefix and then fails.
func failingDownload(name, prefix string) *files.Download {
	return files.NewDownload(fileRecord(name, 10, 0, false), func(_ context.Context, dst io.Writer) error {
		if prefix != "" {
			_, _ = io.WriteString(dst, prefix)
		}

		return errors.New("object storage stopped")
	})
}

// TestDownload_SendFailsBeforeFirstByte (R-09): the client gets an error answer without the
// download headers, not a 200 with an empty body.
func (s *FileHandlerSuite) TestDownload_SendFailsBeforeFirstByte() {
	s.Run("API client", func() {
		s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).Return(failingDownload("a.txt", ""), nil)

		req := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
		req.Header.Set("Accept", "application/octet-stream")
		rec := s.serve(req)

		s.Equal(http.StatusServiceUnavailable, rec.Code)
		s.Empty(rec.Header().Get("Content-Disposition"))
		s.Equal("application/json", rec.Header().Get("Content-Type"))
		s.NotContains(rec.Body.String(), "object storage stopped")
	})

	s.Run("browser", func() {
		s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).Return(failingDownload("a.txt", ""), nil)

		rec := s.serve(downloadForm())

		s.Equal(http.StatusServiceUnavailable, rec.Code)
		s.Empty(rec.Header().Get("Content-Disposition"))
		s.Contains(rec.Body.String(), "File storage is unavailable. Try again later.")
	})
}

// TestDownload_SendFailsAfterFirstByte (R-09): after the first byte the handler aborts the
// connection, so the client sees a broken transfer and not a complete response.
func (s *FileHandlerSuite) TestDownload_SendFailsAfterFirstByte() {
	server := httptest.NewServer(s.router)
	defer server.Close()

	s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).Return(failingDownload("a.txt", "hel"), nil)

	req, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, server.URL+"/files/abc/download", nil)
	s.Require().NoError(err)
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := server.Client().Do(req)
	s.Require().NoError(err)

	defer resp.Body.Close()

	s.Equal(http.StatusOK, resp.StatusCode)

	_, err = io.ReadAll(resp.Body)
	s.ErrorIs(err, io.ErrUnexpectedEOF)
}

// TestDownload_Canceled: a client that goes away during Send gets no error answer.
func (s *FileHandlerSuite) TestDownload_Canceled() {
	ctx, cancel := context.WithCancel(s.T().Context())
	canceled := files.NewDownload(fileRecord("a.txt", 10, 0, false), func(context.Context, io.Writer) error {
		cancel()

		return context.Canceled
	})
	s.files.EXPECT().Download(gomock.Any(), "abc", access(true)).Return(canceled, nil)

	req := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil).WithContext(ctx)
	req.Header.Set("Accept", "application/octet-stream")
	rec := s.serve(req)

	s.Empty(rec.Body.String())
}

// --- trailing JSON ---

// TestUpdate_TrailingData (R-49): data after the JSON value gives 400; white space is accepted.
func (s *FileHandlerSuite) TestUpdate_TrailingData() {
	for _, body := range []string{`{"ttl":60} {"privacy":"owner"}`, `{"ttl":60}x`, `{"ttl":60}]`} {
		s.Run(body, func() {
			s.Equal(http.StatusBadRequest, s.serve(updateRequest(body, "code-1234")).Code)
		})
	}

	s.Run("white space", func() {
		s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), gomock.Any()).
			Return(fileRecord("a.txt", 3, 0, false), nil)

		s.Equal(http.StatusOK, s.serve(updateRequest("{\"ttl\":60}\n \t", "code-1234")).Code)
	})
}

// --- access with accounts (R-59) ---

// testUser returns a user with the given ID.
func testUser(userID uuid.UUID) *domain.User {
	return &domain.User{
		ID: userID, Username: "user-" + userID.String()[:8],
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
}

// TestPrivateFile_ReadDenials: each read path refuses a caller that cannot see the file. A
// browser goes to the sign-in page, or gets 403 when it is signed in; an API client gets 401.
func (s *FileHandlerSuite) TestPrivateFile_ReadDenials() {
	auth := NewMockAuthManager(s.ctrl)
	router := s.newAuthRouter(auth)

	s.Run("file page, anonymous browser", func() {
		s.expectFileKind()
		s.files.EXPECT().Inspect(gomock.Any(), "abc", accountAccess(false)).Return(nil, domain.ErrForbidden)

		rec := s.serveWith(router, browserGet("/abc"))

		s.Equal(http.StatusSeeOther, rec.Code)
		s.Equal("/login?next=%2Fabc", rec.Header().Get("Location"))
	})

	s.Run("file page, other user", func() {
		other := testUser(uuid.New())
		auth.EXPECT().GetSession(gomock.Any(), "other-session").Return(other, nil)
		s.expectFileKind()
		s.files.EXPECT().Inspect(gomock.Any(), "abc", domain.Reader{
			CallerID: other.ID, Authenticated: true, AllowBurn: false, SkipPrivacy: false,
		}).Return(nil, domain.ErrForbidden)

		req := browserGet("/abc")
		req.AddCookie(httptestutil.RequestCookie("padmark_session", "other-session"))
		rec := s.serveWith(router, req)

		s.Equal(http.StatusForbidden, rec.Code)
		s.NotContains(rec.Body.String(), "secret-name.txt")
	})

	s.Run("metadata", func() {
		s.files.EXPECT().Inspect(gomock.Any(), "abc", accountAccess(false)).Return(nil, domain.ErrForbidden)

		s.Equal(http.StatusUnauthorized, s.serveWith(router, httptest.NewRequest(http.MethodGet, "/files/abc", nil)).Code)
	})

	s.Run("download", func() {
		s.files.EXPECT().Download(gomock.Any(), "abc", accountAccess(true)).Return(nil, domain.ErrForbidden)

		req := httptest.NewRequest(http.MethodGet, "/files/abc/download", nil)
		req.Header.Set("Accept", "application/octet-stream")

		s.Equal(http.StatusUnauthorized, s.serveWith(router, req).Code)
	})
}

// TestDelete_InvisibleFileIsUnauthorized: with accounts on, a caller that cannot see a private
// file gets 401: the usecase checks privacy before the edit code and returns ErrForbidden.
func (s *FileHandlerSuite) TestDelete_InvisibleFileIsUnauthorized() {
	s.files.EXPECT().Delete(gomock.Any(), "abc", "code-1234", anonymousReader()).Return(domain.ErrForbidden)

	req := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
	req.Header.Set("X-Edit-Code", "code-1234")

	s.Equal(http.StatusUnauthorized, s.serveWith(s.newAuthRouter(NewMockAuthManager(s.ctrl)), req).Code)
}

// TestDelete_FailedCodesLockOut: wrong edit codes lock the slug, as for PUT.
func (s *FileHandlerSuite) TestDelete_FailedCodesLockOut() {
	s.files.EXPECT().Delete(gomock.Any(), "abc", "wrong", access(false)).Return(domain.ErrInvalidEditCode).Times(10)

	deleteRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
		req.Header.Set("X-Edit-Code", "wrong")

		return req
	}

	for range 10 {
		s.Equal(http.StatusForbidden, s.serve(deleteRequest()).Code)
	}

	s.Equal(http.StatusTooManyRequests, s.serve(deleteRequest()).Code)
}

// TestUpload_OwnerFromCredentials: an upload with a session or an API token records the
// account as the owner.
func (s *FileHandlerSuite) TestUpload_OwnerFromCredentials() {
	auth := NewMockAuthManager(s.ctrl)
	router := s.newAuthRouter(auth)
	user := testUser(uuid.New())

	expectOwner := func() {
		s.files.EXPECT().Create(gomock.Any(), "a.txt", gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, name string, body io.Reader, opts files.CreateOptions) (*domain.File, error) {
				s.Require().NotNil(opts.OwnerID)
				s.Equal(user.ID, *opts.OwnerID)

				return readAllAsUsecase(ctx, name, body, opts)
			})
	}

	s.Run("API token", func() {
		auth.EXPECT().ResolveAPIToken(gomock.Any(), "user-key").Return(user, "token-hash", nil)
		expectOwner()

		req := uploadRequest(nil, "a.txt", []byte("x"))
		req.Header.Set("Authorization", "Bearer user-key")

		s.Equal(http.StatusCreated, s.serveWith(router, req).Code)
	})

	s.Run("session", func() {
		auth.EXPECT().GetSession(gomock.Any(), "user-session").Return(user, nil)
		expectOwner()

		req := uploadRequest(nil, "a.txt", []byte("x"))
		req.AddCookie(httptestutil.RequestCookie("padmark_session", "user-session"))

		s.Equal(http.StatusCreated, s.serveWith(router, req).Code)
	})
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
