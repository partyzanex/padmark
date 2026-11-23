package server

import (
	"context"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
)

// ConfigSuite runs configFromCommand with the real serve flag set. The flag and environment
// names are the operator-facing contract, so the tests spell them out.
type ConfigSuite struct {
	suite.Suite
}

func TestConfigSuite(t *testing.T) {
	suite.Run(t, new(ConfigSuite))
}

func (s *ConfigSuite) SetupTest() {
	// The environment-only values have no flag that could reset them.
	s.T().Setenv("PADMARK_BOOTSTRAP_TOKEN", "")
	s.T().Setenv("PADMARK_S3_ACCESS_KEY", "")
	s.T().Setenv("PADMARK_S3_SECRET_KEY", "")
}

func (s *ConfigSuite) TestDefaults() {
	cfg, err := s.parse()

	s.Require().NoError(err)
	s.Equal(Config{
		files: filesConfig{
			s3: s3store.Config{
				Endpoint: "", Bucket: "", Region: "us-east-1", AccessKey: "", SecretKey: "", PathStyle: true,
			},
			orphans:       nil,
			transfer:      web.FileTransferLimits{IdleTimeout: time.Minute, MaxUploads: 8, MaxUploadsPerClient: 4},
			maxFileSize:   100 << 20,
			sweepInterval: time.Hour,
			allowInsecure: false,
		},
		router: routerConfig{
			publicScheme: "", trustedProxies: nil, csrfSecret: nil,
			maxBodyBytes: 4 << 20, rateLimit: 10, rateBurst: 20, disableAPI: false, directRedirect: false,
		},
		http: httpConfig{
			redirectHosts: nil, addr: ":8080", tlsCert: "", tlsKey: "", redirectAddr: "", metricsAddr: "",
			allowedHosts: nil, readTimeout: 30, writeTimeout: 60, maxHeaderBytes: 64 << 10,
		},
		accounts: accountsConfig{
			totpIssuer: "padmark", bootstrapToken: "", sessionTTL: 30 * 24 * time.Hour,
			enabled: false, cookieMaxAgeSet: false, sessionTTLSet: false,
		},
		storage: storageConfig{dialect: "sqlite", dsn: "padmark.db", skipMigrationBackup: false},
		log:     logConfig{level: "info", format: "json"},
		keyring: keyringConfig{path: "", allowShared: false},
		argon2: argon2Config{
			params: crypto.Argon2Params{Memory: 24 << 10, Time: 2, Threads: 1}, memoryBudget: 128 << 10,
		},
		maxContentLength: 4 << 20,
		shutdownTimeout:  10 * time.Second,
		customSlugs:      true,
	}, cfg)
}

func (s *ConfigSuite) TestEnvironment() {
	csrfSecret := strings.Repeat("c", 32)
	bootstrapToken := strings.Repeat("b", 32)

	for name, value := range fullEnvironment(csrfSecret, bootstrapToken) {
		s.T().Setenv(name, value)
	}

	cfg, err := s.parse()

	s.Require().NoError(err)
	s.Equal(storageConfig{dialect: "postgres", dsn: "postgres://padmark@db/padmark", skipMigrationBackup: true},
		cfg.storage)
	s.Equal(keyringConfig{path: "/run/secrets/keys.json", allowShared: true}, cfg.keyring)
	s.Equal(logConfig{level: "debug", format: "text"}, cfg.log)
	s.Equal(httpConfig{
		redirectHosts: map[string]struct{}{"a.example": {}, "b.example": {}},
		addr:          ":9000", tlsCert: "cert.pem", tlsKey: "key.pem", redirectAddr: ":80",
		metricsAddr: "127.0.0.1:9090", allowedHosts: []string{"a.example", "b.example"},
		readTimeout: 5, writeTimeout: 6, maxHeaderBytes: 4096,
	}, cfg.http)
	s.Equal(routerConfig{
		publicScheme: "https", trustedProxies: []*net.IPNet{mustCIDR(s.T(), "10.0.0.0/8")},
		csrfSecret: []byte(csrfSecret), maxBodyBytes: 1024, rateLimit: 3, rateBurst: 4,
		disableAPI: true, directRedirect: true,
	}, cfg.router)
	s.Equal(accountsConfig{
		totpIssuer: "notes", bootstrapToken: bootstrapToken, sessionTTL: 10 * time.Minute,
		enabled: true, cookieMaxAgeSet: false, sessionTTLSet: true,
	}, cfg.accounts)
	s.Equal(argon2Config{params: crypto.Argon2Params{Memory: 32768, Time: 3, Threads: 2}, memoryBudget: 65536},
		cfg.argon2)
	s.Equal(filesConfig{
		s3: s3store.Config{
			Endpoint: "https://s3.example.com", Bucket: "files", Region: "eu-west-1",
			AccessKey: "access-0b1c", SecretKey: "secret-9f2d", PathStyle: false,
		},
		orphans:     nil,
		transfer:    web.FileTransferLimits{IdleTimeout: 45 * time.Second, MaxUploads: 10, MaxUploadsPerClient: 2},
		maxFileSize: 2048, sweepInterval: 30 * time.Minute, allowInsecure: true,
	}, cfg.files)
	s.Equal(512, cfg.maxContentLength)
	s.Equal(time.Minute, cfg.shutdownTimeout)
	s.False(cfg.customSlugs)
}

func (s *ConfigSuite) TestFlagsWinOverEnvironment() {
	s.T().Setenv("PADMARK_ADDR", ":9000")
	s.T().Setenv("PADMARK_STORAGE", "postgres")
	s.T().Setenv("PADMARK_ARGON2_TIME", "3")
	s.T().Setenv("PADMARK_SHUTDOWN_TIMEOUT", "1m")

	cfg, err := s.parse("--addr", ":9001", "--storage", "sqlite", "--argon2-time", "4",
		"--shutdown-timeout", "30s", "--max-body-bytes", "2048")

	s.Require().NoError(err)
	s.Equal(":9001", cfg.http.addr)
	s.Equal("sqlite", cfg.storage.dialect)
	s.Equal(uint32(4), cfg.argon2.params.Time)
	s.Equal(30*time.Second, cfg.shutdownTimeout)
	s.Equal(2048, cfg.maxContentLength, "a 0 content limit follows --max-body-bytes")
}

func (s *ConfigSuite) TestDeprecatedCookieMaxAge() {
	cfg, err := s.parse("--cookie-max-age", "3600")

	s.Require().NoError(err)
	s.Equal(time.Hour, cfg.accounts.sessionTTL)
	s.True(cfg.accounts.cookieMaxAgeSet)
	s.False(cfg.accounts.sessionTTLSet)
}

func (s *ConfigSuite) TestFilesOffNamesOtherSettings() {
	s.T().Setenv("PADMARK_S3_ACCESS_KEY", "access-0b1c")
	s.T().Setenv("PADMARK_S3_SECRET_KEY", "secret-9f2d")

	cfg, err := s.parse("--s3-bucket", "padmark-files", "--s3-allow-insecure")

	s.Require().NoError(err)
	s.Equal([]string{"--s3-bucket", "--s3-allow-insecure", "PADMARK_S3_ACCESS_KEY", "PADMARK_S3_SECRET_KEY"},
		cfg.files.orphans, "the names of the settings, not their values")
}

// TestValidationErrors gives each check of configFromCommand with its full error text. The
// texts are the startup errors that an operator sees.
func (s *ConfigSuite) TestValidationErrors() {
	for _, testCase := range configErrorCases() {
		s.Run(testCase.name, func() {
			for name, value := range testCase.env {
				s.T().Setenv(name, value)
			}

			_, err := s.parse(testCase.args...)

			s.Require().EqualError(err, testCase.text)

			if testCase.want != nil {
				s.Require().ErrorIs(err, testCase.want)
			}
		})
	}
}

// TestValidationOrder keeps the order of the checks of the earlier startup sequence, so a
// command line with two errors gives the same error as before.
func (s *ConfigSuite) TestValidationOrder() {
	tests := []struct {
		want error
		args []string
	}{
		{args: []string{"--public-scheme", "ftp", "--auth-tokens", "x"}, want: errRemovedAuthTokens},
		{args: []string{"--trusted-proxies", "x", "--argon2-memory-budget", "1"}, want: errInvalidArgon2Budget},
		{args: []string{"--trusted-proxies", "x", "--s3-endpoint", "https://s3.example.com"}, want: errS3Config},
		{args: []string{"--tls-cert", "cert.pem", "--csrf-secret", "short"}, want: errInvalidCSRFSecret},
		{args: []string{"--csrf-secret", "short", "--public-scheme", "ftp"}, want: errInvalidPublicScheme},
		{args: []string{"--public-scheme", "ftp", "--trusted-proxies", "x"}, want: errInvalidTrustedProxy},
	}

	for _, testCase := range tests {
		_, err := s.parse(testCase.args...)

		s.Require().ErrorIs(err, testCase.want, testCase.args)
	}
}

// --- helpers ---

// parse runs the serve flag set with args and returns the result of configFromCommand.
func (s *ConfigSuite) parse(args ...string) (Config, error) {
	var (
		cfg    Config
		cfgErr error
	)

	cmd := clifactory.Command(func(command *cli.Command) {
		command.Name = "padmark"
		command.Flags = appFlags()
		command.Action = func(_ context.Context, cmd *cli.Command) error {
			cfg, cfgErr = configFromCommand(cmd)

			return nil
		}
	})

	s.Require().NoError(cmd.Run(s.T().Context(), append([]string{"padmark"}, args...)))

	return cfg, cfgErr
}

// fullEnvironment sets each serve setting through its PADMARK_* variable.
func fullEnvironment(csrfSecret, bootstrapToken string) map[string]string {
	return map[string]string{
		"PADMARK_STORAGE": "postgres", "PADMARK_DSN": "postgres://padmark@db/padmark",
		"PADMARK_SKIP_MIGRATION_BACKUP": "true",
		"PADMARK_CONTENT_KEYRING_FILE":  "/run/secrets/keys.json", "PADMARK_ALLOW_SHARED_KEYRING": "true",
		"PADMARK_LOG_LEVEL": "debug", "PADMARK_LOG_FORMAT": "text",
		"PADMARK_ADDR": ":9000", "PADMARK_TLS_CERT": "cert.pem", "PADMARK_TLS_KEY": "key.pem",
		"PADMARK_HTTP_REDIRECT_ADDR": ":80", "PADMARK_METRICS_ADDR": "127.0.0.1:9090",
		"PADMARK_ALLOWED_HOSTS": "a.example, b.example", "PADMARK_READ_TIMEOUT": "5",
		"PADMARK_WRITE_TIMEOUT": "6", "PADMARK_MAX_HEADER_BYTES": "4096",
		"PADMARK_TRUSTED_PROXIES": "10.0.0.0/8", "PADMARK_PUBLIC_SCHEME": "https",
		"PADMARK_CSRF_SECRET": csrfSecret, "PADMARK_MAX_BODY_BYTES": "1024", "PADMARK_RATE_LIMIT": "3",
		"PADMARK_RATE_BURST": "4", "PADMARK_DISABLE_API": "true", "PADMARK_DIRECT_REDIRECT": "true",
		"PADMARK_ENABLE_ACCOUNTS": "true", "PADMARK_TOTP_ISSUER": "notes", "PADMARK_SESSION_TTL": "600",
		"PADMARK_BOOTSTRAP_TOKEN": bootstrapToken,
		"PADMARK_ARGON2_MEMORY":   "32768", "PADMARK_ARGON2_TIME": "3", "PADMARK_ARGON2_THREADS": "2",
		"PADMARK_ARGON2_MEMORY_BUDGET": "65536",
		"PADMARK_S3_ENDPOINT":          "https://s3.example.com", "PADMARK_S3_BUCKET": "files",
		"PADMARK_S3_REGION": "eu-west-1", "PADMARK_S3_PATH_STYLE": "false", "PADMARK_S3_ALLOW_INSECURE": "true",
		"PADMARK_S3_ACCESS_KEY": "access-0b1c", "PADMARK_S3_SECRET_KEY": "secret-9f2d",
		"PADMARK_MAX_FILE_SIZE": "2048", "PADMARK_FILES_SWEEP_INTERVAL": "30m",
		"PADMARK_FILE_IDLE_TIMEOUT": "45s", "PADMARK_MAX_UPLOADS": "10", "PADMARK_MAX_UPLOADS_PER_CLIENT": "2",
		"PADMARK_MAX_CONTENT_LENGTH": "512", "PADMARK_SHUTDOWN_TIMEOUT": "1m", "PADMARK_CUSTOM_SLUGS": "false",
	}
}

// configErrorCase is one check of configFromCommand: the input and the full error text.
type configErrorCase struct {
	env  map[string]string
	want error
	name string
	text string
	args []string
}

// configErrorCases gives each check of configFromCommand with the text that the operator sees.
func configErrorCases() []configErrorCase {
	return slices.Concat(secretErrorCases(), listenerErrorCases())
}

// secretErrorCases are the checks that ran before the keyring opened also before Config existed.
func secretErrorCases() []configErrorCase {
	return []configErrorCase{
		{
			name: "removed auth tokens flag", args: []string{"--auth-tokens", "legacy-0b1c"}, env: nil,
			want: errRemovedAuthTokens, text: errRemovedAuthTokens.Error(),
		},
		{
			name: "removed auth tokens environment", args: nil, env: map[string]string{"PADMARK_AUTH_TOKENS": "x"},
			want: errRemovedAuthTokens, text: errRemovedAuthTokens.Error(),
		},
		{
			name: "argon2 memory below 8 KiB per thread", args: []string{"--argon2-memory", "1", "--argon2-threads", "2"},
			env: nil, want: nil,
			text: "validate argon2 configuration: argon2 cost outside supported bounds " +
				"(memory: 8*threads..262144 KiB, time: 1..10, threads: 1..16)",
		},
		{
			name: "argon2 budget below memory", args: []string{"--argon2-memory", "65536", "--argon2-memory-budget", "32768"},
			env: nil, want: errInvalidArgon2Budget,
			text: "validate argon2 configuration: insufficient argon2 memory budget: --argon2-memory-budget " +
				"(32768 KiB) must be at least --argon2-memory (65536 KiB)",
		},
		{
			name: "short bootstrap token", args: nil, env: map[string]string{"PADMARK_BOOTSTRAP_TOKEN": "short"},
			want: errInvalidBootstrapToken,
			text: "validate bootstrap token: invalid bootstrap token length: PADMARK_BOOTSTRAP_TOKEN must be at " +
				"least 32 characters, got 5",
		},
	}
}

// listenerErrorCases are the checks that ran after the database opened until Config existed.
func listenerErrorCases() []configErrorCase {
	return []configErrorCase{
		{
			name: "s3 endpoint without bucket", args: []string{"--s3-endpoint", "https://s3.example.com"},
			env: map[string]string{"PADMARK_S3_ACCESS_KEY": "a", "PADMARK_S3_SECRET_KEY": "b"}, want: errS3Config,
			text: "invalid s3 configuration: --s3-bucket, PADMARK_S3_ACCESS_KEY and PADMARK_S3_SECRET_KEY " +
				"are required with --s3-endpoint",
		},
		{
			name: "s3 endpoint without secret key",
			args: []string{"--s3-endpoint", "https://s3.example.com", "--s3-bucket", "files"},
			env:  map[string]string{"PADMARK_S3_ACCESS_KEY": "a"}, want: errS3Config,
			text: "invalid s3 configuration: --s3-bucket, PADMARK_S3_ACCESS_KEY and PADMARK_S3_SECRET_KEY " +
				"are required with --s3-endpoint",
		},
		{
			name: "trusted proxy that is not an address", args: []string{"--trusted-proxies", "not-an-ip"}, env: nil,
			want: errInvalidTrustedProxy, text: `invalid trusted proxy address: "not-an-ip"`,
		},
		{
			name: "trusted proxy with a wrong prefix", args: []string{"--trusted-proxies", "10.0.0.0/99"}, env: nil,
			want: nil, text: `invalid trusted proxy CIDR "10.0.0.0/99": invalid CIDR address: 10.0.0.0/99`,
		},
		{
			name: "public scheme", args: []string{"--public-scheme", "ftp"}, env: nil,
			want: errInvalidPublicScheme, text: `invalid public scheme "ftp": must be "http" or "https"`,
		},
		{
			name: "short csrf secret", args: nil, env: map[string]string{"PADMARK_CSRF_SECRET": "short"},
			want: errInvalidCSRFSecret,
			text: "invalid csrf secret length: csrf secret must be at least 32 characters, got 5",
		},
		{
			name: "tls certificate without key", args: []string{"--tls-cert", "cert.pem"}, env: nil,
			want: errIncompleteTLSConfig, text: "--tls-cert and --tls-key must both be set to enable TLS",
		},
		{
			name: "tls key without certificate", args: nil, env: map[string]string{"PADMARK_TLS_KEY": "key.pem"},
			want: errIncompleteTLSConfig, text: "--tls-cert and --tls-key must both be set to enable TLS",
		},
	}
}

func mustCIDR(t *testing.T, raw string) *net.IPNet {
	t.Helper()

	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}

	return network
}
