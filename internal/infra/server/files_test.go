package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
)

// Throwaway S3 key values for the tests; no test server checks them.
const (
	testS3KeyID    = "test-access-0b1c"
	testS3KeyValue = "test-value-9f2d"
)

// AttachFilesSuite runs attachFiles with the real flag set. Each case stops before the handler
// and the keyring, so the suite gives nil for them.
type AttachFilesSuite struct {
	suite.Suite

	logs       *syncBuffer
	background *shutdown.Lifo
}

func TestAttachFilesSuite(t *testing.T) {
	suite.Run(t, new(AttachFilesSuite))
}

func (s *AttachFilesSuite) SetupTest() {
	s.logs = new(syncBuffer)
	s.background = shutdown.NewLIFO()
	s.T().Setenv(EnvS3AccessKey, "")
	s.T().Setenv(EnvS3SecretKey, "")
}

func (s *AttachFilesSuite) TearDownTest() {
	s.Require().NoError(s.background.CloseContext(s.T().Context()))
}

func (s *AttachFilesSuite) TestNoEndpointLogsFilesOff() {
	s.Require().NoError(s.attach())

	s.Contains(s.logs.String(), "level=INFO")
	s.Contains(s.logs.String(), "file publishing is off")
	s.NotContains(s.logs.String(), "level=WARN")
}

func (s *AttachFilesSuite) TestNoEndpointWarnsAboutOtherSettings() {
	s.setKeys()

	s.Require().NoError(s.attach("--s3-bucket", "padmark-files"))

	logs := s.logs.String()
	s.Contains(logs, "level=WARN")
	s.Contains(logs, "--s3-bucket")
	s.Contains(logs, EnvS3AccessKey)
	s.Contains(logs, EnvS3SecretKey)
	s.NotContains(logs, testS3KeyID)
	s.NotContains(logs, testS3KeyValue)
	s.NotContains(logs, "padmark-files", "the log has the names of the settings, not the values")
}

func (s *AttachFilesSuite) TestEndpointWithoutBucket() {
	s.setKeys()

	err := s.attach("--s3-endpoint", "https://s3.example.com")

	s.Require().ErrorIs(err, errS3Config)
}

func (s *AttachFilesSuite) TestEndpointWithoutKeys() {
	for _, env := range []string{EnvS3AccessKey, EnvS3SecretKey} {
		s.setKeys()
		s.T().Setenv(env, "")

		err := s.attach("--s3-endpoint", "https://s3.example.com", "--s3-bucket", "padmark-files")

		s.Require().ErrorIs(err, errS3Config, env)
	}
}

func (s *AttachFilesSuite) TestBadEndpointScheme() {
	s.setKeys()

	err := s.attach("--s3-endpoint", "ftp://s3.example.com", "--s3-bucket", "padmark-files")

	s.Require().ErrorContains(err, "must be an http:// or https:// URL")
}

func (s *AttachFilesSuite) TestEndpointWithUserInfo() {
	s.setKeys()

	err := s.attach("--s3-endpoint", "https://user:pw-71aa@s3.example.com", "--s3-bucket", "padmark-files")

	s.Require().ErrorContains(err, "must not have user info")
	s.NotContains(err.Error(), "pw-71aa")
}

func (s *AttachFilesSuite) TestRemotePlainHTTPIsRefused() {
	s.setKeys()

	err := s.attach("--s3-endpoint", "http://s3:9000", "--s3-bucket", "padmark-files")

	s.Require().ErrorContains(err, "plain http://")
}

func (s *AttachFilesSuite) TestPingFailureStopsStartup() {
	tests := []struct {
		status int
		want   string
	}{
		{status: http.StatusForbidden, want: "s3 store: s3 bucket check"},
		{status: http.StatusNotFound, want: "s3 store: s3 bucket does not exist and the server cannot create it"},
	}

	for _, testCase := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(testCase.status)
		}))

		s.setKeys()

		err := s.attach("--s3-endpoint", server.URL, "--s3-bucket", "padmark-files")

		server.Close()
		s.Require().ErrorContains(err, testCase.want)
		s.NotContains(s.logs.String(), "file publishing is on")
	}
}

// --- helpers ---

func (s *AttachFilesSuite) setKeys() {
	s.T().Setenv(EnvS3AccessKey, testS3KeyID)
	s.T().Setenv(EnvS3SecretKey, testS3KeyValue)
}

// attach parses args with the serve flags and calls attachFiles. configFromCommand checks the
// S3 settings, so its error counts as the error of the file wiring.
func (s *AttachFilesSuite) attach(args ...string) error {
	var attachErr error

	log := slog.New(slog.NewTextHandler(s.logs, nil))
	cmd := clifactory.Command(func(command *cli.Command) {
		command.Name = "padmark"
		command.Flags = appFlags()
		command.Action = func(ctx context.Context, cmd *cli.Command) error {
			var cfg Config

			cfg, attachErr = configFromCommand(cmd)
			if attachErr == nil {
				shared := contentServices{keyring: nil, hasher: nil, background: s.background}
				attachErr = attachFiles(ctx, &cfg, nil, nil, shared, new(fileDeps), log)
			}

			return nil
		}
	})

	s.Require().NoError(cmd.Run(s.T().Context(), append([]string{"padmark"}, args...)))

	return attachErr
}

// FileFlagsSuite checks the file and shutdown flags of the serve flag set.
type FileFlagsSuite struct {
	suite.Suite
}

func TestFileFlagsSuite(t *testing.T) {
	suite.Run(t, new(FileFlagsSuite))
}

func (s *FileFlagsSuite) TestEnvironmentNames() {
	s.Equal("PADMARK_S3_ACCESS_KEY", EnvS3AccessKey)
	s.Equal("PADMARK_S3_SECRET_KEY", EnvS3SecretKey)
	s.Equal("PADMARK_S3_ALLOW_INSECURE", EnvS3AllowInsecure)
	s.Equal("PADMARK_SHUTDOWN_TIMEOUT", EnvShutdownTimeout)
	s.Equal("PADMARK_FILE_IDLE_TIMEOUT", EnvFileIdleTimeout)
	s.Equal("PADMARK_MAX_UPLOADS", EnvMaxUploads)
	s.Equal("PADMARK_MAX_UPLOADS_PER_CLIENT", EnvMaxUploadsPerClient)
}

func (s *FileFlagsSuite) TestDefaults() {
	cmd, err := s.parse()

	s.Require().NoError(err)
	s.Equal(DefaultFilesSweepInterval, cmd.Duration(FlagFilesSweepInterval))
	s.Equal(int64(DefaultMaxFileSize), cmd.Int64(FlagMaxFileSize))
	s.Equal(DefaultShutdownTimeout, cmd.Duration(FlagShutdownTimeout))
	s.False(cmd.Bool(FlagS3AllowInsecure))
	s.Equal(web.FileTransferLimits{
		IdleTimeout: time.Minute, MaxUploads: 8, MaxUploadsPerClient: 4,
	}, fileTransferLimitsFromFlags(cmd))
}

func (s *FileFlagsSuite) TestTransferLimits() {
	cmd, err := s.parse("--file-idle-timeout", "2m30s", "--max-uploads", "64", "--max-uploads-per-client", "8")

	s.Require().NoError(err)
	s.Equal(web.FileTransferLimits{
		IdleTimeout: 150 * time.Second, MaxUploads: 64, MaxUploadsPerClient: 8,
	}, fileTransferLimitsFromFlags(cmd))
}

func (s *FileFlagsSuite) TestTransferLimitsZeroKeepsDefaults() {
	cmd, err := s.parse("--file-idle-timeout", "0", "--max-uploads", "0", "--max-uploads-per-client", "0")

	s.Require().NoError(err)
	s.Equal(web.FileTransferLimits{IdleTimeout: 0, MaxUploads: 0, MaxUploadsPerClient: 0},
		fileTransferLimitsFromFlags(cmd), "0 goes to the handler, which selects its default")
}

func (s *FileFlagsSuite) TestTransferLimitsFromEnvironment() {
	s.T().Setenv(EnvFileIdleTimeout, "45s")
	s.T().Setenv(EnvMaxUploads, "10")
	s.T().Setenv(EnvMaxUploadsPerClient, "2")

	cmd, err := s.parse()

	s.Require().NoError(err)
	s.Equal(web.FileTransferLimits{
		IdleTimeout: 45 * time.Second, MaxUploads: 10, MaxUploadsPerClient: 2,
	}, fileTransferLimitsFromFlags(cmd))
}

func (s *FileFlagsSuite) TestValidValues() {
	cmd, err := s.parse("--files-sweep-interval", "0", "--max-file-size", "51539607552",
		"--shutdown-timeout", "2m", "--s3-allow-insecure")

	s.Require().NoError(err)
	s.Equal(time.Duration(0), cmd.Duration(FlagFilesSweepInterval))
	s.Equal(int64(maxFileSizeLimit), cmd.Int64(FlagMaxFileSize))
	s.Equal(2*time.Minute, cmd.Duration(FlagShutdownTimeout))
	s.True(cmd.Bool(FlagS3AllowInsecure))
}

func (s *FileFlagsSuite) TestAllowInsecureFromEnvironment() {
	s.T().Setenv(EnvS3AllowInsecure, "true")

	cmd, err := s.parse()

	s.Require().NoError(err)
	s.True(cmd.Bool(FlagS3AllowInsecure))
}

func (s *FileFlagsSuite) TestInvalidValues() {
	tests := []struct {
		args []string
		want error
	}{
		{args: []string{"--files-sweep-interval", "-1s"}, want: errInvalidSweepInterval},
		{args: []string{"--max-file-size", "0"}, want: errInvalidMaxFileSize},
		{args: []string{"--max-file-size", "-5"}, want: errInvalidMaxFileSize},
		{args: []string{"--max-file-size", "51539607553"}, want: errMaxFileSizeTooLarge},
		{args: []string{"--shutdown-timeout", "0"}, want: errInvalidShutdownTimeout},
		{args: []string{"--shutdown-timeout", "-1s"}, want: errInvalidShutdownTimeout},
		{args: []string{"--file-idle-timeout", "-1s"}, want: errInvalidFileIdleTimeout},
		{args: []string{"--max-uploads", "-1"}, want: errInvalidUploadLimit},
		{args: []string{"--max-uploads-per-client", "-1"}, want: errInvalidUploadLimit},
	}

	for _, testCase := range tests {
		_, err := s.parse(testCase.args...)

		// urfave/cli puts the validator text into its own error without %w.
		s.ErrorContains(err, testCase.want.Error(), testCase.args)
	}
}

func (s *FileFlagsSuite) TestMaxFileSizeLimitFitsTheObjectLimit() {
	// pmf1 adds less than 0.1 % to the file size; the limit keeps a larger margin.
	s.Less(int64(maxFileSizeLimit)*1001/1000, s3store.MaxObjectSize)
}

// --- helpers ---

// parse runs the serve flag set with args and returns the parsed command.
func (s *FileFlagsSuite) parse(args ...string) (*cli.Command, error) {
	var parsed *cli.Command

	cmd := clifactory.Command(func(command *cli.Command) {
		command.Name = "padmark"
		command.Flags = appFlags()
		command.Action = func(_ context.Context, cmd *cli.Command) error {
			parsed = cmd

			return nil
		}
	})

	err := cmd.Run(s.T().Context(), append([]string{"padmark"}, args...))

	return parsed, err
}
