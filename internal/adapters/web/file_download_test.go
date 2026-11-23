package web_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

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

// TestDownload_BrowserErrorPage: a second click on the download button of a burn file gets a
// page with the file text, not JSON.
func (s *FileHandlerSuite) TestDownload_BrowserErrorPage() {
	s.files.EXPECT().Download(gomock.Any(), "abc", access(false)).Return(nil, domain.ErrNotFound)

	rec := s.serve(browserGet("/files/abc/download"))

	s.Equal(http.StatusNotFound, rec.Code)
	s.Contains(rec.Body.String(), "The file was deleted or its lifetime ended.")
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
