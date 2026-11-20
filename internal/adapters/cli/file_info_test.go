package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

const cmdFileInfo = "file-info"

type FileInfoSuite struct {
	suite.Suite

	status  int
	body    string
	methods []string
	server  *httptest.Server
}

func TestFileInfoSuite(t *testing.T) {
	suite.Run(t, new(FileInfoSuite))
}

func (s *FileInfoSuite) SetupTest() {
	isolateEnv(s.T())

	s.status, s.body, s.methods = http.StatusOK, fileInfoJSON(s.T(), testFileID, "notes.txt"), nil
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.methods = append(s.methods, r.Method+" "+r.URL.Path)
		writeTestResponse(s.T(), w, s.status, s.body)
	}))
}

func (s *FileInfoSuite) TearDownTest() {
	s.server.Close()
}

func (s *FileInfoSuite) TestText() {
	out, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdFileInfo, testFileID)
	s.Require().NoError(err)

	s.Equal([]string{"GET /files/f1le-id"}, s.methods, "file-info reads only the metadata")
	s.Equal(map[string]string{
		"id": testFileID, "url": "https://pad.example/f1le-id", "download": s.server.URL + "/files/f1le-id/download",
		"name": "notes.txt", "size": "5", "privacy": "public", "downloads": "0", "created": "2026-11-20T10:00:00Z",
	}, outputFields(out))
}

func (s *FileInfoSuite) TestJSON() {
	out, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdFileInfo, "https://pad.example/"+testFileID,
		"--json")
	s.Require().NoError(err)
	s.Equal([]string{"GET /files/f1le-id"}, s.methods)

	var decoded map[string]any

	s.Require().NoError(json.Unmarshal([]byte(out), &decoded))
	s.Equal(testFileID, decoded["id"])
	s.Equal("notes.txt", decoded["name"])
}

func (s *FileInfoSuite) TestNotFound() {
	s.status, s.body = http.StatusNotFound, `{"message":"not found"}`

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdFileInfo, testFileID)
	s.Require().ErrorIs(err, ErrFileNotFound)
	s.Require().ErrorIs(err, ErrNotFound)
	s.Require().ErrorContains(err, "file not found")
}

func (s *FileInfoSuite) TestFilesOff() {
	s.status, s.body = http.StatusServiceUnavailable, `{"message":"feature is not enabled"}`

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdFileInfo, testFileID)
	s.Require().ErrorIs(err, ErrFilesDisabled)
}

func (s *FileInfoSuite) TestIDRequired() {
	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdFileInfo)
	s.Require().ErrorIs(err, errFileIDRequired)
	s.Empty(s.methods)
}

func TestFileIDFromURL(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"https://pad.example/abc":                "abc",
		"https://pad.example/files/abc":          "abc",
		"https://pad.example/files/abc/download": "abc",
		"http://localhost:8080/abc/":             "abc",
	} {
		got, err := fileIDFromURL(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}

	for _, raw := range []string{
		"https://pad.example/", "https://pad.example/files", "https://pad.example/a/b",
		"https://pad.example/files/abc/edit", "://bad",
	} {
		_, err := fileIDFromURL(raw)
		require.ErrorIs(t, err, errFileURL, raw)
	}
}
