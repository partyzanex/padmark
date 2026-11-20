package cli

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/suite"
)

const (
	cmdUpload       = "upload"
	testFileID      = "f1le-id"
	testFileContent = "hello"
	createdFileJSON = `{"id":"f1le-id","url":"https://pad.example/f1le-id","name":"notes.txt","size":5,` +
		`"privacy":"public","downloads":0,"burn_after_download":true,"expires_at":"2026-11-21T10:00:00Z",` +
		`"created_at":"2026-11-20T10:00:00Z","edit_code":"edit-code-secret"}`
)

// uploadRequest is what the fake server read from one upload.
type uploadRequest struct {
	fields   map[string]string
	fileName string
	content  string
}

type UploadSuite struct {
	suite.Suite

	mu       sync.Mutex
	requests []uploadRequest
	status   int
	body     string
	server   *httptest.Server
}

func TestUploadSuite(t *testing.T) {
	suite.Run(t, new(UploadSuite))
}

func (s *UploadSuite) SetupTest() {
	isolateEnv(s.T())

	s.requests = nil
	s.status, s.body = http.StatusCreated, createdFileJSON
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
}

func (s *UploadSuite) TearDownTest() {
	s.server.Close()
}

// handle reads the multipart form in order, as the real server does, and answers the
// configured status and body.
func (s *UploadSuite) handle(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		writeTestResponse(s.T(), w, http.StatusBadRequest, `{"message":"not multipart"}`)

		return
	}

	req := uploadRequest{fields: map[string]string{}, fileName: "", content: ""}

	for part, partErr := reader.NextPart(); partErr == nil; part, partErr = reader.NextPart() {
		data, readErr := io.ReadAll(part)
		if readErr != nil {
			s.T().Errorf("read part: %v", readErr)
		}

		if part.FormName() == "file" {
			req.fileName, req.content = part.FileName(), string(data)

			continue
		}

		req.fields[part.FormName()] = string(data)
	}

	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	writeTestResponse(s.T(), w, s.status, s.body)
}

// tempFile writes testFileContent to a new file with the given name.
func (s *UploadSuite) tempFile(name string) string {
	path := filepath.Join(s.T().TempDir(), name)
	s.Require().NoError(os.WriteFile(path, []byte(testFileContent), 0o600))

	return path
}

func (s *UploadSuite) TestSuccess() {
	path := s.tempFile("notes.txt")

	out, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, path,
		"--ttl", "60", "--privacy", "public", "--burn", "--slug", "my-file", "--edit-code", "my-edit-code")
	s.Require().NoError(err)

	s.Require().Len(s.requests, 1)
	s.Equal(map[string]string{
		"ttl": "60", "privacy": "public", "burn_after_download": "true", "slug": "my-file",
		"edit_code": "my-edit-code",
	}, s.requests[0].fields)
	s.Equal("notes.txt", s.requests[0].fileName)
	s.Equal(testFileContent, s.requests[0].content)

	s.Equal(map[string]string{
		"id": testFileID, "url": "https://pad.example/f1le-id", "download": s.server.URL + "/files/f1le-id/download",
		"name": "notes.txt", "size": "5", "edit-code": "edit-code-secret", "burn": "yes",
		"expires": "2026-11-21T10:00:00Z",
	}, outputFields(out))
}

func (s *UploadSuite) TestNameFlagAndJSON() {
	path := s.tempFile("local.bin")

	out, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, path, "--name", "remote.bin", "--json")
	s.Require().NoError(err)

	s.Require().Len(s.requests, 1)
	s.Equal("remote.bin", s.requests[0].fileName)
	s.Empty(s.requests[0].fields, "unset flags send no fields")

	var decoded map[string]any

	s.Require().NoError(json.Unmarshal([]byte(out), &decoded))
	s.Equal(testFileID, decoded["id"])
	s.Equal("edit-code-secret", decoded["edit_code"])
}

func (s *UploadSuite) TestStdin() {
	reader, writer, err := os.Pipe()
	s.Require().NoError(err)

	_, err = writer.WriteString("from stdin")
	s.Require().NoError(err)
	s.Require().NoError(writer.Close())

	saved := os.Stdin
	os.Stdin = reader

	s.T().Cleanup(func() {
		os.Stdin = saved
		_ = reader.Close()
	})

	_, err = runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, "-", "--name", "piped.txt")
	s.Require().NoError(err)

	s.Require().Len(s.requests, 1)
	s.Equal("piped.txt", s.requests[0].fileName)
	s.Equal("from stdin", s.requests[0].content)
}

func (s *UploadSuite) TestStdinWithoutName() {
	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, "-")
	s.Require().ErrorIs(err, errStdinNameRequired)
	s.Empty(s.requests)
}

func (s *UploadSuite) TestPathRequired() {
	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload)
	s.Require().ErrorIs(err, errFilePathRequired)
}

func (s *UploadSuite) TestMissingFile() {
	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload,
		filepath.Join(s.T().TempDir(), "missing.txt"))
	s.Require().ErrorIs(err, fs.ErrNotExist)
	s.Empty(s.requests)
}

func (s *UploadSuite) TestDirectory() {
	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, s.T().TempDir())
	s.Require().ErrorIs(err, errNotRegularFile)
	s.Empty(s.requests)
}

func (s *UploadSuite) TestNonPositiveTTL() {
	path := s.tempFile("notes.txt")

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, path, "--ttl", "0")
	s.Require().ErrorIs(err, errNonPositiveTTL)
	s.Empty(s.requests)
}

func (s *UploadSuite) TestTooLarge() {
	s.status, s.body = http.StatusRequestEntityTooLarge, `{"message":"file exceeds maximum size (limit 4 bytes)"}`
	path := s.tempFile("notes.txt")

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, path)
	s.Require().ErrorIs(err, ErrFileTooLarge)
	s.Require().ErrorContains(err, "file is larger than the server limit: file exceeds maximum size (limit 4 bytes)")
}

func (s *UploadSuite) TestFilesOff() {
	s.status, s.body = http.StatusServiceUnavailable, `{"message":"feature is not enabled"}`
	path := s.tempFile("notes.txt")

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, path)
	s.Require().ErrorIs(err, ErrFilesDisabled)
	s.Require().ErrorIs(err, ErrServiceUnavailable)
	s.Require().ErrorContains(err, "file publishing is off on the server")
}

func (s *UploadSuite) TestStorageUnavailable() {
	s.status, s.body = http.StatusServiceUnavailable, `{"message":"file storage is unavailable"}`
	path := s.tempFile("notes.txt")

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, path)
	s.Require().ErrorIs(err, ErrServiceUnavailable)
	s.Require().NotErrorIs(err, ErrFilesDisabled)
	s.Require().ErrorContains(err, "file storage is unavailable")
}

func (s *UploadSuite) TestAPIDisabled() {
	s.status, s.body = http.StatusServiceUnavailable, `{"message":"API is disabled","code":"api_disabled"}`
	path := s.tempFile("notes.txt")

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, path)
	s.Require().ErrorIs(err, ErrAPIDisabled)
}

func (s *UploadSuite) TestSlugConflict() {
	s.status, s.body = http.StatusConflict, `{"message":"slug is taken"}`
	path := s.tempFile("notes.txt")

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdUpload, path, "--slug", "taken")
	s.Require().ErrorIs(err, ErrSlugConflict)
}
