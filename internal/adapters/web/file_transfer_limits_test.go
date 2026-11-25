package web_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// newLimitedRouter returns a router with files on and the given transfer limits.
func (s *FileHandlerSuite) newLimitedRouter(limits web.FileTransferLimits) http.Handler {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, web.NoPinger{})
	handlerCfg.Files = s.files
	handlerCfg.FileTransfer = limits
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
