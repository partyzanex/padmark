package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

const cmdDownload = "download"

type DownloadSuite struct {
	suite.Suite

	dir       string
	name      string
	content   string
	truncate  bool
	metaHits  atomic.Int32
	downloads atomic.Int32
	server    *httptest.Server
}

func TestDownloadSuite(t *testing.T) {
	suite.Run(t, new(DownloadSuite))
}

func (s *DownloadSuite) SetupTest() {
	isolateEnv(s.T())

	s.dir = s.T().TempDir()
	s.T().Chdir(s.dir)

	s.name, s.content, s.truncate = "report.txt", "file body", false
	s.metaHits.Store(0)
	s.downloads.Store(0)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /files/{id}", s.handleMetadata)
	mux.HandleFunc("GET /files/{id}/download", s.handleDownload)
	s.server = httptest.NewServer(mux)
}

func (s *DownloadSuite) TearDownTest() {
	s.server.Close()
}

func (s *DownloadSuite) handleMetadata(w http.ResponseWriter, r *http.Request) {
	s.metaHits.Add(1)

	if r.PathValue("id") != testFileID {
		writeTestResponse(s.T(), w, http.StatusNotFound, `{"message":"not found"}`)

		return
	}

	writeTestResponse(s.T(), w, http.StatusOK, fileInfoJSON(s.T(), testFileID, s.name))
}

// handleDownload sends the content. With truncate, it promises more bytes than it sends and
// then closes the connection, as a broken transfer does.
func (s *DownloadSuite) handleDownload(w http.ResponseWriter, r *http.Request) {
	s.downloads.Add(1)

	if r.Header.Get("Accept") != "application/octet-stream" {
		s.T().Errorf("download Accept = %q", r.Header.Get("Accept"))
	}

	w.Header().Set("Content-Disposition", `attachment; filename="`+s.name+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")

	length := len(s.content)
	if s.truncate {
		length += 100
	}

	w.Header().Set("Content-Length", strconv.Itoa(length))
	w.WriteHeader(http.StatusOK)

	_, err := io.WriteString(w, s.content)
	if err != nil {
		s.T().Errorf("write download: %v", err)
	}
}

func (s *DownloadSuite) download(args ...string) (string, error) {
	return runCLIOutput(s.T().Context(), append([]string{"--url", s.server.URL, cmdDownload}, args...)...)
}

// dirEntries returns the names in the test directory, so a test can see a temporary file that
// was not removed.
func (s *DownloadSuite) dirEntries() []string {
	entries, err := os.ReadDir(s.dir)
	s.Require().NoError(err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

// readFile returns the content of a file in the test directory. os.Root keeps the read in it.
func (s *DownloadSuite) readFile(name string) string {
	root, err := os.OpenRoot(s.dir)
	s.Require().NoError(err)

	defer func() { s.NoError(root.Close()) }()

	data, err := root.ReadFile(name)
	s.Require().NoError(err)

	return string(data)
}

func (s *DownloadSuite) TestNameFromServer() {
	out, err := s.download(testFileID)
	s.Require().NoError(err)

	s.Equal("file body", s.readFile("report.txt"))
	s.Equal("saved report.txt (9 bytes)\n", out)
	s.Equal([]string{"report.txt"}, s.dirEntries(), "no temporary file stays")

	info, err := os.Stat(filepath.Join(s.dir, "report.txt"))
	s.Require().NoError(err)
	s.Equal(os.FileMode(downloadFileMode), info.Mode().Perm())
}

func (s *DownloadSuite) TestURLArgument() {
	_, err := s.download(s.server.URL + "/files/" + testFileID + "/download")
	s.Require().NoError(err)
	s.FileExists(filepath.Join(s.dir, "report.txt"))
}

func (s *DownloadSuite) TestUnsafeName() {
	for _, name := range []string{"../evil.txt", `dir\evil.txt`, "..", "bad\x01name"} {
		s.Run(name, func() {
			s.name = name

			_, err := s.download(testFileID)
			s.Require().ErrorIs(err, errUnsafeFileName)
			s.Equal(int32(0), s.downloads.Load(), "an unsafe name must stop the command before the download")
			s.Empty(s.dirEntries())
		})
	}
}

func (s *DownloadSuite) TestDestinationExists() {
	target := filepath.Join(s.dir, "report.txt")
	s.Require().NoError(os.WriteFile(target, []byte("keep me"), 0o600))

	_, err := s.download(testFileID)
	s.Require().ErrorIs(err, errOutputExists)
	s.Equal(int32(0), s.downloads.Load(), "an existing file must stop the command before the download")

	s.Equal("keep me", s.readFile(filepath.Base(target)))
}

func (s *DownloadSuite) TestForce() {
	target := filepath.Join(s.dir, "report.txt")
	s.Require().NoError(os.WriteFile(target, []byte("old content"), 0o600))

	_, err := s.download(testFileID, "--force")
	s.Require().NoError(err)

	s.Equal("file body", s.readFile(filepath.Base(target)))
	s.Equal([]string{"report.txt"}, s.dirEntries())
}

func (s *DownloadSuite) TestOutputFlag() {
	target := filepath.Join(s.dir, "chosen.bin")

	_, err := s.download(testFileID, "-o", target)
	s.Require().NoError(err)
	s.Equal(int32(0), s.metaHits.Load(), "--output needs no metadata request")

	s.Equal("file body", s.readFile(filepath.Base(target)))
}

func (s *DownloadSuite) TestStdout() {
	out, err := s.download(testFileID, "--output", "-")
	s.Require().NoError(err)
	s.Equal("file body", out)
	s.Empty(s.dirEntries())
}

func (s *DownloadSuite) TestBrokenTransfer() {
	s.truncate = true

	_, err := s.download(testFileID)
	s.Require().Error(err)
	s.Empty(s.dirEntries(), "a broken transfer leaves no destination and no temporary file")
}

func (s *DownloadSuite) TestNotFound() {
	_, err := s.download("missing")
	s.Require().ErrorIs(err, ErrFileNotFound)
	s.Empty(s.dirEntries())
}

func TestSafeFileName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"report.txt", ".hidden", "a..b", "имя файла.pdf"} {
		got, err := safeFileName(name)
		require.NoError(t, err, name)
		assert.Equal(t, name, got)
	}

	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "a\nb", "a\x7fb"} {
		_, err := safeFileName(name)
		require.ErrorIs(t, err, errUnsafeFileName, "%q", name)
	}
}

func TestFileTransferError_TransportErrorKeepsIdentity(t *testing.T) {
	t.Parallel()

	err := fileTransferError(padmark.ErrIncompleteDownload, ErrBurnReadRefused)
	require.ErrorIs(t, err, padmark.ErrIncompleteDownload)
}
