package web_test

import (
	"net/http"
	"net/http/httptest"
)

func (s *FileHandlerSuite) TestUpload_FilesOff() {
	router := s.newRouter(false)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, uploadRequest(nil, "a.txt", []byte("x")))

	s.Equal(http.StatusServiceUnavailable, rec.Code)
}

// TestAPIRoutes_FilesOff: without object storage each file API route answers 503 in JSON, also
// to a browser, and the success page of a file shows no file details.
func (s *FileHandlerSuite) TestAPIRoutes_FilesOff() {
	router := s.newRouter(false)

	deleteReq := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
	deleteReq.Header.Set("X-Edit-Code", "code-1234")

	for name, req := range map[string]*http.Request{
		"metadata":         httptest.NewRequest(http.MethodGet, "/files/abc", nil),
		"download":         httptest.NewRequest(http.MethodGet, "/files/abc/download", nil),
		"download head":    httptest.NewRequest(http.MethodHead, "/files/abc/download", nil),
		"browser download": browserGet("/files/abc/download"),
		"delete":           deleteReq,
	} {
		s.Run(name, func() {
			rec := s.serveWith(router, req)

			s.Equal(http.StatusServiceUnavailable, rec.Code)
			s.Equal("application/json", rec.Header().Get("Content-Type"))
		})
	}

	s.Run("success page", func() {
		rec := s.serveWith(router, browserGet("/success?kind=file&id=abc"))

		s.Equal(http.StatusOK, rec.Code)
		s.Contains(rec.Body.String(), "Your file is live")
		s.NotContains(rec.Body.String(), "<strong>")
	})
}

func (s *FileHandlerSuite) TestUpdate_FilesOff() {
	rec := httptest.NewRecorder()
	s.newRouter(false).ServeHTTP(rec, updateRequest(`{"ttl":60}`, "code-1234"))

	s.Equal(http.StatusServiceUnavailable, rec.Code)
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
