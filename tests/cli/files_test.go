//go:build integration

package cli_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	adaptercli "github.com/partyzanex/padmark/internal/adapters/cli"
	infraserver "github.com/partyzanex/padmark/internal/infra/server"
	"github.com/partyzanex/padmark/tests/internal/servertest"
)

// Test values for the throwaway S3 container only; s3mock accepts any credentials.
const (
	filesAccessKey = "padmark-test"
	filesSecretKey = "padmark-test-secret"
	filesBucket    = "padmark-files"
	// filesSize is larger than one S3 multipart part (16 MiB), so the scenario streams the file
	// in more than one part in the two directions.
	filesSize       = 17<<20 + 123
	filesReadyWait  = 30 * time.Second
	filesReadyPoll  = 50 * time.Millisecond
	filesProbeLimit = 500 * time.Millisecond
)

// FilesSuite runs the file commands of padmark-cli against a real server with S3-compatible
// object storage: upload → file-info → download → update-file → delete-file.
type FilesSuite struct {
	suite.Suite

	container testcontainers.Container
	endpoint  string
	binary    string
}

func TestFilesSuite(t *testing.T) {
	suite.Run(t, new(FilesSuite))
}

func (s *FilesSuite) SetupSuite() {
	ctx := s.T().Context()

	// adobe/s3mock creates the bucket at startup and does not check signatures, as in the
	// s3store integration tests.
	container, err := testcontainers.Run(ctx, "adobe/s3mock:3.12.0",
		testcontainers.WithEnv(map[string]string{"initialBuckets": filesBucket}),
		testcontainers.WithExposedPorts("9090/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("9090/tcp").WithStartupTimeout(time.Minute)),
	)
	s.Require().NoError(err)

	s.container = container

	hostPort, err := container.PortEndpoint(ctx, "9090/tcp", "")
	s.Require().NoError(err)

	s.endpoint = "http://" + hostPort
	s.binary = servertest.Build(s.T())
}

func (s *FilesSuite) TearDownSuite() {
	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

// SetupTest also moves HOME and XDG_CONFIG_HOME to a new directory. Without that, the CLI reads
// the token file of the developer and sends the token to the test server.
func (s *FilesSuite) SetupTest() {
	isolateCLI(s.T())

	home := s.T().TempDir()
	s.T().Setenv("HOME", home)
	s.T().Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func (s *FilesSuite) TestLifecycle() {
	serverURL := s.startServer()
	dir := s.T().TempDir()

	content := make([]byte, filesSize)
	_, err := rand.Read(content)
	s.Require().NoError(err)

	source := filepath.Join(dir, "archive.bin")
	s.Require().NoError(os.WriteFile(source, content, 0o600))

	out, err := s.cli(serverURL, "upload", source, "--ttl", "3600", "--json")
	s.Require().NoError(err, out)

	var created struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		EditCode string `json:"edit_code"`
	}

	s.Require().NoError(json.Unmarshal([]byte(out), &created))
	s.Require().NotEmpty(created.ID)
	s.Require().NotEmpty(created.EditCode)
	s.Equal("archive.bin", created.Name)
	s.Equal(int64(filesSize), created.Size)

	// file-info: metadata only.
	out, err = s.cli(serverURL, "file-info", created.ID)
	s.Require().NoError(err, out)
	s.Contains(out, "archive.bin")
	s.Contains(out, "downloads:")

	// download: the file name comes from the server; the content must be the same.
	downloadDir := filepath.Join(dir, "downloads")
	s.Require().NoError(os.Mkdir(downloadDir, 0o700))
	s.T().Chdir(downloadDir)

	out, err = s.cli(serverURL, "download", serverURL+"/"+created.ID)
	s.Require().NoError(err, out)

	root, err := os.OpenRoot(downloadDir)
	s.Require().NoError(err)

	defer func() { s.NoError(root.Close()) }()

	downloaded, err := root.ReadFile("archive.bin")
	s.Require().NoError(err)
	s.True(bytes.Equal(content, downloaded), "the downloaded content must match the upload")

	// update-file: remove the expiry with the edit code.
	out, err = s.cli(serverURL, "update-file", created.ID, "--clear-expiry", "--edit-code", created.EditCode)
	s.Require().NoError(err, out)
	s.NotContains(out, "expires:")

	// delete-file, then the metadata is gone.
	out, err = s.cli(serverURL, "delete-file", created.ID, "--edit-code", created.EditCode)
	s.Require().NoError(err, out)
	s.Contains(out, "deleted "+created.ID)

	_, err = s.cli(serverURL, "file-info", created.ID)
	s.Require().ErrorIs(err, adaptercli.ErrFileNotFound)
}

// TestFilesOff checks the message of a server without object storage.
func (s *FilesSuite) TestFilesOff() {
	serverURL, _ := servertest.Start(s.T(), s.binary, s.T().TempDir(), "--rate-limit", "0")

	source := filepath.Join(s.T().TempDir(), "note.txt")
	s.Require().NoError(os.WriteFile(source, []byte("text"), 0o600))

	_, err := s.cli(serverURL, "upload", source)
	s.Require().ErrorIs(err, adaptercli.ErrFilesDisabled)
	s.Require().ErrorContains(err, "file publishing is off on the server")
}

// cli runs padmark-cli in this process and returns its standard output.
func (s *FilesSuite) cli(serverURL string, args ...string) (string, error) {
	var out, errOut bytes.Buffer

	app := adaptercli.NewApp()
	app.Writer, app.ErrWriter = &out, &errOut

	err := app.Run(s.T().Context(), append([]string{"padmark-cli", "--url", serverURL}, args...))
	if errOut.Len() > 0 {
		s.T().Logf("padmark-cli %v stderr: %s", args, errOut.String())
	}

	return out.String(), err
}

// startServer runs the server in this process with the S3 container. servertest.Start removes
// each PADMARK_* variable from the server environment, but the S3 access key and secret key come
// only from the environment. The test context stops the server; the cleanup waits for it.
func (s *FilesSuite) startServer() string {
	listener, err := new(net.ListenConfig).Listen(s.T().Context(), "tcp", "127.0.0.1:0")
	s.Require().NoError(err)

	address := listener.Addr().String()
	s.Require().NoError(listener.Close())

	s.T().Setenv("PADMARK_S3_ACCESS_KEY", filesAccessKey)
	s.T().Setenv("PADMARK_S3_SECRET_KEY", filesSecretKey)

	args := []string{
		"padmark", "--addr", address, "--metrics-addr", "", "--rate-limit", "0", "--log-level", "error",
		"--dsn", filepath.Join(s.T().TempDir(), "padmark.db"),
		"--s3-endpoint", s.endpoint, "--s3-bucket", filesBucket,
	}

	done := make(chan error, 1)

	go func() { done <- infraserver.NewApp().Run(s.T().Context(), args) }()

	s.T().Cleanup(func() {
		err := <-done
		if err != nil && !errors.Is(err, context.Canceled) {
			s.T().Errorf("server stopped with an error: %v", err)
		}
	})

	baseURL := "http://" + address
	s.waitReady(baseURL, done)

	return baseURL
}

// waitReady polls /readyz until it answers 200. It fails the test when the server stops first or
// when the wait limit passes.
func (s *FilesSuite) waitReady(baseURL string, done chan error) {
	probe := &http.Client{Timeout: filesProbeLimit, Transport: nil, CheckRedirect: nil, Jar: nil}
	defer probe.CloseIdleConnections()

	deadline := time.NewTimer(filesReadyWait)
	defer deadline.Stop()

	ticker := time.NewTicker(filesReadyPoll)
	defer ticker.Stop()

	for {
		request, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, baseURL+"/readyz", http.NoBody)
		s.Require().NoError(err)

		response, err := probe.Do(request)
		if err == nil {
			s.Require().NoError(response.Body.Close())

			if response.StatusCode == http.StatusOK {
				return
			}
		}

		select {
		case err := <-done:
			done <- err // the cleanup reads the result again.

			s.FailNow("server stopped before readiness", "%v", err)
		case <-deadline.C:
			s.FailNow("server did not become ready")
		case <-ticker.C:
		}
	}
}
