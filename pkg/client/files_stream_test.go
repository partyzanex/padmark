package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/pkg/client"
)

const (
	mib = 1 << 20
	// bigFileSize is the upload and download size of the streaming tests.
	bigFileSize = 20 * mib
	// gateSize is the number of bytes that must arrive before the source gives more.
	gateSize = mib
	// testTimeout stops a test that waits for data that never arrives.
	testTimeout = 30 * time.Second
	testToken   = "test-token"
)

var errSourceStopped = errors.New("source stopped: the test timed out")

type FilesStreamSuite struct {
	suite.Suite
}

func TestFilesStreamSuite(t *testing.T) {
	suite.Run(t, new(FilesStreamSuite))
}

// newClient starts handler on a test server and returns a client that adds a bearer token,
// as the CLI and MCP clients do through WithClient.
func (s *FilesStreamSuite) newClient(handler http.Handler) *client.Client {
	server := httptest.NewServer(handler)
	s.T().Cleanup(server.Close)

	httpClient := &http.Client{Transport: bearerTransport{base: server.Client().Transport}}

	cl, err := client.NewClient(server.URL, client.WithClient(httpClient))
	s.Require().NoError(err)

	return cl
}

func (s *FilesStreamSuite) TestUploadFile_StreamsFieldsBeforeFile() {
	ctx, cancel := context.WithTimeout(s.T().Context(), testTimeout)
	defer cancel()

	gate := make(chan struct{})
	received := make(chan uploadSeen, 1)

	cl := s.newClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := readUpload(r, gate)
		received <- seen

		writeCreated(w, seen.size)
	}))

	source := &patternReader{size: bigFileSize, gateAt: gateSize, gate: gate, abort: ctx.Done()}

	created, err := cl.UploadFile(ctx, "отчёт \"2026\".bin", source, client.UploadFileOptions{
		TTL:               90 * time.Second,
		Privacy:           "authenticated",
		Slug:              "report",
		EditCode:          "MySecretCode1",
		BurnAfterDownload: true,
	})
	s.Require().NoError(err)

	seen := <-received
	s.Require().NoError(seen.err)
	s.Equal([]string{"ttl", "privacy", "burn_after_download", "slug", "edit_code", "file"}, seen.order)
	s.Equal(map[string]string{
		"ttl": "90", "privacy": "authenticated", "burn_after_download": "true",
		"slug": "report", "edit_code": "MySecretCode1",
	}, seen.fields)
	s.Equal("отчёт \"2026\".bin", seen.fileName)
	s.Equal("Bearer "+testToken, seen.auth)
	s.Equal(int64(bigFileSize), seen.size)
	s.True(seen.patternOK, "the server must get the exact bytes of the source")

	s.Equal("abc", created.ID)
	s.Equal(int64(bigFileSize), created.Size)
	s.Equal("secret-code", created.EditCode)
}

func (s *FilesStreamSuite) TestUploadFile_ZeroOptionsSendNoFields() {
	received := make(chan uploadSeen, 1)

	cl := s.newClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := readUpload(r, nil)
		received <- seen

		writeCreated(w, seen.size)
	}))

	source := &patternReader{size: 10, gateAt: 0, gate: nil, abort: nil}

	_, err := cl.UploadFile(s.T().Context(), "a.txt", source, client.UploadFileOptions{})
	s.Require().NoError(err)

	seen := <-received
	s.Require().NoError(seen.err)
	s.Equal([]string{"file"}, seen.order)
	s.Equal(int64(10), seen.size)
}

func (s *FilesStreamSuite) TestUploadFile_TooLarge() {
	cl := s.newClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The server answers before it reads the file, as the real server does with
		// MaxBytesReader. net/http drains the small rest of the body after the handler.
		writeJSON(w, http.StatusRequestEntityTooLarge, `{"message":"file exceeds maximum size (limit 1024 bytes)"}`)
	}))

	source := &patternReader{size: 128 << 10, gateAt: 0, gate: nil, abort: nil}

	created, err := cl.UploadFile(s.T().Context(), "big.bin", source, client.UploadFileOptions{})
	s.Require().Error(err)
	s.Nil(created)

	var apiErr *client.APIError

	s.Require().ErrorAs(err, &apiErr)
	s.Equal(http.StatusRequestEntityTooLarge, apiErr.StatusCode)
	s.Equal("file exceeds maximum size (limit 1024 bytes)", apiErr.Message)
	s.Empty(apiErr.Code)
}

func (s *FilesStreamSuite) TestUploadFile_BrokenConnection() {
	ctx, cancel := context.WithTimeout(s.T().Context(), testTimeout)
	defer cancel()

	cl := s.newClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read a part of the body, then drop the connection without a response.
		_, _ = io.CopyN(io.Discard, r.Body, gateSize)

		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))

	source := &patternReader{size: bigFileSize, gateAt: 0, gate: nil, abort: ctx.Done()}

	created, err := cl.UploadFile(ctx, "big.bin", source, client.UploadFileOptions{})
	s.Require().Error(err)
	s.Nil(created)

	var apiErr *client.APIError

	s.False(errors.As(err, &apiErr), "a dropped connection is a transport error, not a status: %v", err)
	s.NoError(ctx.Err(), "UploadFile must return when the connection drops, not at the timeout")
}

func (s *FilesStreamSuite) TestUploadFile_InvalidTTL() {
	cl := s.newClient(http.NotFoundHandler())

	for _, ttl := range []time.Duration{-time.Second, 500 * time.Millisecond} {
		_, err := cl.UploadFile(s.T().Context(), "a.txt", &patternReader{size: 1, gateAt: 0, gate: nil, abort: nil},
			client.UploadFileOptions{TTL: ttl})
		s.Require().ErrorIs(err, client.ErrInvalidUploadOptions, ttl)
	}
}

func (s *FilesStreamSuite) TestDownloadFile_Streams() {
	ctx, cancel := context.WithTimeout(s.T().Context(), testTimeout)
	defer cancel()

	clientGot := make(chan struct{})

	requests := make(chan *http.Request, 1)

	cl := s.newClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(context.WithoutCancel(r.Context()))

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(bigFileSize))
		w.Header().Set("Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": "отчёт.pdf"}))

		// Send the first part and wait until the client has it. A client that reads the full
		// body before it writes to dst never sends the signal, and the test times out.
		source := &patternReader{size: bigFileSize, gateAt: gateSize, gate: clientGot, abort: r.Context().Done()}
		_, _ = io.Copy(flushWriter{w: w}, source)
	}))

	dst := &patternWriter{signalAt: gateSize, signal: clientGot, once: sync.Once{}}

	downloaded, err := cl.DownloadFile(ctx, "my file", dst)
	s.Require().NoError(err)

	req := <-requests
	s.Equal("application/octet-stream", req.Header.Get("Accept"))
	s.Equal("Bearer "+testToken, req.Header.Get("Authorization"))
	s.Equal("/files/my file/download", req.URL.Path)
	s.Equal("отчёт.pdf", downloaded.Name)
	s.Equal(int64(bigFileSize), downloaded.Size)
	s.Equal(int64(bigFileSize), dst.written)
	s.True(dst.patternOK(), "dst must get the exact bytes of the server")
}

func (s *FilesStreamSuite) TestDownloadFile_BrokenConnection() {
	cl := s.newClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(bigFileSize))

		_, _ = io.CopyN(w, &patternReader{size: gateSize, gateAt: 0, gate: nil, abort: nil}, gateSize)

		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))

	dst := &patternWriter{signalAt: 0, signal: nil, once: sync.Once{}}

	downloaded, err := cl.DownloadFile(s.T().Context(), "abc", dst)
	s.Require().Error(err)
	s.Nil(downloaded)
	s.Less(dst.written, int64(bigFileSize))

	var apiErr *client.APIError

	s.False(errors.As(err, &apiErr), "a dropped connection is a transport error, not a status: %v", err)
}

func (s *FilesStreamSuite) TestDownloadFile_ErrorStatus() {
	cases := []struct {
		name    string
		status  int
		body    string
		message string
		code    string
	}{
		{
			name: "burn file without intent", status: http.StatusConflict,
			body:    `{"message":"burn resource","code":"burn_read_not_explicit"}`,
			message: "burn resource", code: "burn_read_not_explicit",
		},
		{name: "not found", status: http.StatusNotFound, body: `{"message":"not found"}`, message: "not found", code: ""},
		{name: "body is not JSON", status: http.StatusBadGateway, body: "<html>", message: "Bad Gateway", code: ""},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			cl := s.newClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, tc.status, tc.body)
			}))

			dst := &patternWriter{signalAt: 0, signal: nil, once: sync.Once{}}

			_, err := cl.DownloadFile(s.T().Context(), "abc", dst)

			var apiErr *client.APIError

			s.Require().ErrorAs(err, &apiErr)
			s.Equal(tc.status, apiErr.StatusCode)
			s.Equal(tc.message, apiErr.Message)
			s.Equal(tc.code, apiErr.Code)
			s.Zero(dst.written)
		})
	}
}

func (s *FilesStreamSuite) TestDownloadFile_EmptyID() {
	cl := s.newClient(http.NotFoundHandler())

	_, err := cl.DownloadFile(s.T().Context(), "", io.Discard)
	s.Require().Error(err)
}

// --- helpers ---

// bearerTransport adds a bearer token to each request, as the CLI HTTP client does.
type bearerTransport struct {
	base http.RoundTripper
}

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+testToken)

	return b.base.RoundTrip(clone)
}

// patternByte is the byte at offset off of the test content.
func patternByte(off int64) byte {
	return byte(off % 251)
}

// patternReader gives size bytes of the test content. When gate is not nil, it stops after
// gateAt bytes until gate is closed. abort stops the wait when the test times out.
type patternReader struct {
	gate   <-chan struct{}
	abort  <-chan struct{}
	size   int64
	gateAt int64
	read   int64
}

func (p *patternReader) Read(buf []byte) (int, error) {
	if p.read >= p.size {
		return 0, io.EOF
	}

	limit := p.size

	if p.gate != nil {
		if p.read >= p.gateAt {
			select {
			case <-p.gate:
				p.gate = nil
			case <-p.abort:
				return 0, errSourceStopped
			}
		} else {
			limit = p.gateAt
		}
	}

	count := int(min(int64(len(buf)), limit-p.read))
	for idx := range count {
		buf[idx] = patternByte(p.read + int64(idx))
	}

	p.read += int64(count)

	return count, nil
}

// patternWriter checks the test content and closes signal after signalAt bytes.
type patternWriter struct {
	signal   chan struct{}
	signalAt int64
	written  int64
	once     sync.Once
	mismatch bool
}

func (p *patternWriter) Write(buf []byte) (int, error) {
	for idx, value := range buf {
		if value != patternByte(p.written+int64(idx)) {
			p.mismatch = true
		}
	}

	p.written += int64(len(buf))

	if p.signal != nil && p.written >= p.signalAt {
		p.once.Do(func() { close(p.signal) })
	}

	return len(buf), nil
}

func (p *patternWriter) patternOK() bool {
	return !p.mismatch
}

// flushWriter flushes each write, so that the client gets the data at once.
type flushWriter struct {
	w http.ResponseWriter
}

func (f flushWriter) Write(buf []byte) (int, error) {
	count, err := f.w.Write(buf)
	if err == nil {
		err = http.NewResponseController(f.w).Flush()
	}

	return count, err
}

// uploadSeen is what the test server read from an upload.
type uploadSeen struct {
	err       error
	fields    map[string]string
	fileName  string
	auth      string
	order     []string
	size      int64
	patternOK bool
}

// readUpload reads the form as the real server does: fields first, then the file part. It
// closes gate after gateSize bytes of the file.
func readUpload(r *http.Request, gate chan struct{}) uploadSeen {
	seen := uploadSeen{fields: map[string]string{}, auth: r.Header.Get("Authorization"), patternOK: true}

	reader, err := r.MultipartReader()
	if err != nil {
		seen.err = err

		return seen
	}

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return seen
		}

		if err != nil {
			seen.err = err

			return seen
		}

		seen.order = append(seen.order, part.FormName())

		if part.FormName() != "file" {
			value, _ := io.ReadAll(part)
			seen.fields[part.FormName()] = string(value)

			continue
		}

		seen.fileName = part.FileName()
		dst := &patternWriter{signalAt: gateSize, signal: gate, once: sync.Once{}}

		_, err = io.Copy(dst, part)
		if err != nil {
			seen.err = err

			return seen
		}

		seen.size, seen.patternOK = dst.written, dst.patternOK()
	}
}

func writeCreated(w http.ResponseWriter, size int64) {
	body, _ := json.Marshal(map[string]any{
		"id": "abc", "url": "http://example.test/abc", "name": "a.bin", "size": size,
		"privacy": "public", "downloads": 0, "burn_after_download": false,
		"created_at": "2026-11-20T10:00:00Z", "edit_code": "secret-code",
	})

	writeJSON(w, http.StatusCreated, string(body))
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
