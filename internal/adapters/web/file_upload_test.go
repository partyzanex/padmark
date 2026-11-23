package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
	"github.com/partyzanex/padmark/tests/httptestutil"
)

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

// TestUpload_OwnerFromCredentials: an upload with a session or an API token records the
// account as the owner.
func (s *FileHandlerSuite) TestUpload_OwnerFromCredentials() {
	router := s.newAuthRouter(s.auth)
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
		s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "user-key").Return(user, "token-hash", nil)
		expectOwner()

		req := uploadRequest(nil, "a.txt", []byte("x"))
		req.Header.Set("Authorization", "Bearer user-key")

		s.Equal(http.StatusCreated, s.serveWith(router, req).Code)
	})

	s.Run("session", func() {
		s.auth.EXPECT().GetSession(gomock.Any(), "user-session").Return(user, nil)
		expectOwner()

		req := uploadRequest(nil, "a.txt", []byte("x"))
		req.AddCookie(httptestutil.RequestCookie("padmark_session", "user-session"))

		s.Equal(http.StatusCreated, s.serveWith(router, req).Code)
	})
}
