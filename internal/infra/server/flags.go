package server

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/logging"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// Flag names used in CLI flags.
const (
	FlagAddr                = "addr"
	FlagStorage             = "storage"
	FlagDSN                 = "dsn"
	FlagLogLevel            = "log-level"
	FlagLogFormat           = "log-format"
	FlagEnableAccounts      = "enable-accounts"
	FlagDisableAPI          = "disable-api"
	FlagReadTimeout         = "read-timeout"
	FlagWriteTimeout        = "write-timeout"
	FlagMaxHeaderBytes      = "max-header-bytes"
	FlagMaxBodyBytes        = "max-body-bytes"
	FlagMaxContentLength    = "max-content-length"
	FlagRateLimit           = "rate-limit"
	FlagRateBurst           = "rate-burst"
	FlagTLSCert             = "tls-cert"
	FlagTLSKey              = "tls-key"
	FlagHTTPRedirectAddr    = "http-redirect-addr"
	FlagAllowedHosts        = "allowed-hosts"
	FlagTrustedProxies      = "trusted-proxies"
	FlagPublicScheme        = "public-scheme"
	FlagCustomSlugs         = "custom-slugs"
	FlagTOTPIssuer          = "totp-issuer"
	FlagSessionTTL          = "session-ttl"
	FlagArgon2Memory        = "argon2-memory"
	FlagArgon2Time          = "argon2-time"
	FlagArgon2Threads       = "argon2-threads"
	FlagArgon2MemoryBudget  = "argon2-memory-budget"
	FlagDirectRedirect      = "direct-redirect"
	FlagMetricsAddr         = "metrics-addr"
	FlagCSRFSecret          = "csrf-secret"
	FlagContentKeyring      = "content-keyring-file"
	FlagAllowSharedKeyring  = "allow-shared-keyring"
	FlagSkipMigrationBackup = "skip-migration-backup"
	FlagS3Endpoint          = "s3-endpoint"
	FlagS3Bucket            = "s3-bucket"
	FlagS3Region            = "s3-region"
	FlagS3PathStyle         = "s3-path-style"
	FlagMaxFileSize         = "max-file-size"
	FlagFilesSweepInterval  = "files-sweep-interval"
	FlagS3AllowInsecure     = "s3-allow-insecure"
	FlagShutdownTimeout     = "shutdown-timeout"
	FlagFileIdleTimeout     = "file-idle-timeout"
	FlagMaxUploads          = "max-uploads"
	FlagMaxUploadsPerClient = "max-uploads-per-client"
	// FlagAuthTokens is the removed v0.1.6 static bearer-token flag; it is still defined (hidden)
	// so that using it fails startup with a migration hint instead of being ignored.
	FlagAuthTokens = "auth-tokens" //nolint:gosec // flag name, not a credential
	// FlagCookieMaxAge is the deprecated v0.1.6 cookie lifetime flag, superseded by --session-ttl.
	FlagCookieMaxAge = "cookie-max-age"
)

// Env vars used to configure the service (prefix: PADMARK_*).
const (
	EnvAddr                = "PADMARK_ADDR"
	EnvStorage             = "PADMARK_STORAGE"
	EnvDSN                 = "PADMARK_DSN"
	EnvLogLevel            = "PADMARK_LOG_LEVEL"
	EnvLogFormat           = "PADMARK_LOG_FORMAT"
	EnvEnableAccounts      = "PADMARK_ENABLE_ACCOUNTS"
	EnvDisableAPI          = "PADMARK_DISABLE_API"
	EnvReadTimeout         = "PADMARK_READ_TIMEOUT"
	EnvWriteTimeout        = "PADMARK_WRITE_TIMEOUT"
	EnvMaxHeaderBytes      = "PADMARK_MAX_HEADER_BYTES"
	EnvMaxBodyBytes        = "PADMARK_MAX_BODY_BYTES"
	EnvMaxContentLength    = "PADMARK_MAX_CONTENT_LENGTH"
	EnvRateLimit           = "PADMARK_RATE_LIMIT"
	EnvRateBurst           = "PADMARK_RATE_BURST"
	EnvTLSCert             = "PADMARK_TLS_CERT"
	EnvTLSKey              = "PADMARK_TLS_KEY"
	EnvHTTPRedirectAddr    = "PADMARK_HTTP_REDIRECT_ADDR"
	EnvAllowedHosts        = "PADMARK_ALLOWED_HOSTS"
	EnvTrustedProxies      = "PADMARK_TRUSTED_PROXIES"
	EnvPublicScheme        = "PADMARK_PUBLIC_SCHEME"
	EnvCustomSlugs         = "PADMARK_CUSTOM_SLUGS"
	EnvTOTPIssuer          = "PADMARK_TOTP_ISSUER"
	EnvSessionTTL          = "PADMARK_SESSION_TTL"
	EnvArgon2Memory        = "PADMARK_ARGON2_MEMORY"
	EnvArgon2Time          = "PADMARK_ARGON2_TIME"
	EnvArgon2Threads       = "PADMARK_ARGON2_THREADS"
	EnvArgon2MemoryBudget  = "PADMARK_ARGON2_MEMORY_BUDGET"
	EnvDirectRedirect      = "PADMARK_DIRECT_REDIRECT"
	EnvMetricsAddr         = "PADMARK_METRICS_ADDR"
	EnvCSRFSecret          = "PADMARK_CSRF_SECRET" //nolint:gosec // G101: env var name, not an inline credential
	EnvContentKeyring      = "PADMARK_CONTENT_KEYRING_FILE"
	EnvAllowSharedKeyring  = "PADMARK_ALLOW_SHARED_KEYRING"
	EnvSkipMigrationBackup = "PADMARK_SKIP_MIGRATION_BACKUP"
	EnvS3Endpoint          = "PADMARK_S3_ENDPOINT"
	EnvS3Bucket            = "PADMARK_S3_BUCKET"
	EnvS3Region            = "PADMARK_S3_REGION"
	EnvS3PathStyle         = "PADMARK_S3_PATH_STYLE"
	EnvMaxFileSize         = "PADMARK_MAX_FILE_SIZE"
	EnvFilesSweepInterval  = "PADMARK_FILES_SWEEP_INTERVAL"
	EnvS3AllowInsecure     = "PADMARK_S3_ALLOW_INSECURE"
	EnvShutdownTimeout     = "PADMARK_SHUTDOWN_TIMEOUT"
	EnvFileIdleTimeout     = "PADMARK_FILE_IDLE_TIMEOUT"
	EnvMaxUploads          = "PADMARK_MAX_UPLOADS"
	EnvMaxUploadsPerClient = "PADMARK_MAX_UPLOADS_PER_CLIENT"
	// EnvS3AccessKey and EnvS3SecretKey are the object storage credentials. They are read from
	// the environment only — never a flag, so they cannot show up in process listings.
	// gosec G101 reports a string literal in a constant whose name has "secret". The names
	// are built from envS3Prefix, so the check needs no nolint directive; a test keeps the
	// full values.
	EnvS3AccessKey  = envS3Prefix + "ACCESS_KEY"
	EnvS3SecretKey  = envS3Prefix + "SECRET_KEY"
	EnvAuthTokens   = "PADMARK_AUTH_TOKENS" //nolint:gosec // env var name, not a credential
	EnvCookieMaxAge = "PADMARK_COOKIE_MAX_AGE"
	// EnvBootstrapToken authorises creating the first admin via /setup without an invite. It is
	// read from the environment only — never a flag, so it cannot show up in process listings.
	EnvBootstrapToken = "PADMARK_BOOTSTRAP_TOKEN" //nolint:gosec // G101: env var name, not an inline credential
)

// envS3Prefix is the common prefix of the S3 key variables.
const envS3Prefix = "PADMARK_S3_"

// Default values for all flags.
const (
	DefaultAddr               = ":8080"
	DefaultStorage            = "sqlite"
	DefaultDSN                = "padmark.db"
	DefaultLogLevel           = "info"
	DefaultLogFormat          = "json"
	DefaultReadTimeout        = 30              // seconds.
	DefaultWriteTimeout       = 60              // seconds.
	DefaultMaxHeaderBytes     = 64 * 1024       // 64 KB.
	DefaultMaxBodyBytes       = 4 * 1024 * 1024 // 4 MB.
	DefaultMaxContentLength   = 0               // characters; 0 = the --max-body-bytes value.
	DefaultRateLimit          = 10              // requests per second per IP.
	DefaultRateBurst          = 20              // max burst size per IP.
	DefaultTOTPIssuer         = "padmark"
	DefaultArgon2Memory       = 24 * 1024  // argon2id memory in KiB (24 MiB).
	DefaultArgon2Time         = 2          // argon2id iterations (OWASP minimum at 64 MiB).
	DefaultArgon2Threads      = 1          // argon2id parallelism.
	DefaultArgon2MemoryBudget = 128 * 1024 // KiB of argon2id workspace all concurrent derivations may hold (128 MiB).
	DefaultAllowSharedKeyring = false      // refuse group/other-readable keyrings outside /run/secrets/.
	// DefaultDirectRedirect is off: an anonymous link must not be a free open redirector.
	DefaultDirectRedirect = false
	DefaultS3Region       = "us-east-1"
	// DefaultS3PathStyle is on because most self-hosted S3 servers do not serve bucket
	// subdomains.
	DefaultS3PathStyle = true
	DefaultMaxFileSize = 100 << 20 // 100 MiB.
	// DefaultFilesSweepInterval is the period of the background sweep of expired files.
	DefaultFilesSweepInterval = time.Hour
	// DefaultS3AllowInsecure is off: a plain http:// endpoint on a remote host sends the signed
	// requests and the object metadata without TLS.
	DefaultS3AllowInsecure = false
	// DefaultShutdownTimeout is the limit of the full shutdown sequence; see newStopLimits. The
	// Docker stop command waits 10 seconds before SIGKILL. With this default, the server closes
	// the open connections after 8 seconds, so a usual stop ends before that signal. A larger
	// value needs a larger stop grace period too.
	DefaultShutdownTimeout = 10 * time.Second
	// DefaultFileIdleTimeout, DefaultMaxUploads and DefaultMaxUploadsPerClient come from the web
	// handler, which applies the same values when a flag is 0.
	DefaultFileIdleTimeout     = web.DefaultFileIdleTimeout
	DefaultMaxUploads          = web.DefaultMaxUploads
	DefaultMaxUploadsPerClient = web.DefaultMaxUploadsPerClient
)

// maxFileSizeLimit is the largest --max-file-size: 48 GiB. s3store.MaxObjectSize is about
// 48.8 GiB, and the encrypted object is a little larger than the file.
const maxFileSizeLimit = 48 << 30

// DefaultSessionTTL is the --session-ttl flag default, in seconds — derived from
// auth.DefaultSessionTTL (the single source of truth for the session lifetime default) instead
// of a separately maintained literal that could silently drift out of sync with it.
const DefaultSessionTTL = int(auth.DefaultSessionTTL / time.Second)

// appFlags returns the full flag set for the serve subcommand. The groups keep the flag order
// of the single list that they replace.
func appFlags() []cli.Flag {
	return slices.Concat(
		storageAndLogFlags(),
		accessFlags(),
		limitFlags(),
		transportFlags(),
		sessionFlags(),
		miscFlags(),
		argon2Flags(),
		fileStorageFlags(),
		fileTransferFlags(),
	)
}

// fileStorageFlags returns the object storage flags for files. The access key and the secret
// key come only from EnvS3AccessKey and EnvS3SecretKey.
func fileStorageFlags() []cli.Flag {
	return []cli.Flag{
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagS3Endpoint
			flag.Sources = cli.EnvVars(EnvS3Endpoint)
			flag.Usage = "S3-compatible object storage URL for files, for example https://s3.example.com:9000. " +
				"Empty (default) turns file publishing off. Set " + EnvS3AccessKey + " and " +
				EnvS3SecretKey + " in the environment"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagS3Bucket
			flag.Sources = cli.EnvVars(EnvS3Bucket)
			flag.Usage = "Bucket for files; created at startup when missing. Required with --" + FlagS3Endpoint
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagS3Region
			flag.Sources = cli.EnvVars(EnvS3Region)
			flag.Value = DefaultS3Region
			flag.Usage = "Region of the bucket for the request signature"
		}),
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagS3PathStyle
			flag.Sources = cli.EnvVars(EnvS3PathStyle)
			flag.Value = DefaultS3PathStyle
			flag.Usage = "Use path-style addresses (https://host/bucket/key). Set false for virtual-host " +
				"addresses (https://bucket.host/key)"
		}),
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagS3AllowInsecure
			flag.Sources = cli.EnvVars(EnvS3AllowInsecure)
			flag.Value = DefaultS3AllowInsecure
			flag.Usage = "Permit a plain http:// --" + FlagS3Endpoint + " on a host that is not localhost or a " +
				"loopback IP, for example a Compose service. Without it, startup stops for such an endpoint"
		}),
		clifactory.Int64Flag(func(flag *cli.Int64Flag) {
			flag.Name = FlagMaxFileSize
			flag.Sources = cli.EnvVars(EnvMaxFileSize)
			flag.Value = DefaultMaxFileSize
			flag.Validator = validateMaxFileSize
			flag.Usage = "Largest file in bytes that POST /files accepts (default: 104857600 = 100 MiB; " +
				"maximum: 51539607552 = 48 GiB)"
		}),
		clifactory.DurationFlag(func(flag *cli.DurationFlag) {
			flag.Name = FlagFilesSweepInterval
			flag.Sources = cli.EnvVars(EnvFilesSweepInterval)
			flag.Value = DefaultFilesSweepInterval
			flag.Validator = validateSweepInterval
			flag.Usage = "Period of the background removal of expired files and their objects, for example 30m. " +
				"0 turns the background removal off; each upload still removes expired files"
		}),
	}
}

// fileTransferFlags returns the limits of file uploads and downloads. A 0 keeps the default of
// the web handler.
func fileTransferFlags() []cli.Flag {
	return []cli.Flag{
		clifactory.DurationFlag(func(flag *cli.DurationFlag) {
			flag.Name = FlagFileIdleTimeout
			flag.Sources = cli.EnvVars(EnvFileIdleTimeout)
			flag.Value = DefaultFileIdleTimeout
			flag.Validator = validateFileIdleTimeout
			flag.Usage = "Stop a file upload or download that moves no data for this time, for example 2m. " +
				"Each transfer also has a total limit of 1 hour. 0 selects the default (1m)"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagMaxUploads
			flag.Sources = cli.EnvVars(EnvMaxUploads)
			flag.Value = DefaultMaxUploads
			flag.Validator = validateUploadLimit
			flag.Usage = "Number of file uploads that the server accepts at the same time; each holds one " +
				"object storage part buffer in memory. More uploads get 503 with Retry-After. " +
				"0 selects the default (32)"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagMaxUploadsPerClient
			flag.Sources = cli.EnvVars(EnvMaxUploadsPerClient)
			flag.Value = DefaultMaxUploadsPerClient
			flag.Validator = validateUploadLimit
			flag.Usage = "Number of file uploads from one client IP address at the same time. More uploads " +
				"get 429 with Retry-After. 0 selects the default (4)"
		}),
	}
}

// fileTransferLimitsFromFlags reads the file transfer limits. The flag validators already
// rejected negative values; the handler replaces a 0 with its default.
func fileTransferLimitsFromFlags(cmd *cli.Command) web.FileTransferLimits {
	return web.FileTransferLimits{
		IdleTimeout:         cmd.Duration(FlagFileIdleTimeout),
		MaxUploads:          cmd.Int(FlagMaxUploads),
		MaxUploadsPerClient: cmd.Int(FlagMaxUploadsPerClient),
	}
}

// storageAndLogFlags returns the content keyring, storage and logging flags.
func storageAndLogFlags() []cli.Flag {
	return []cli.Flag{
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagContentKeyring
			flag.Sources = cli.EnvVars(EnvContentKeyring)
			flag.Usage = "JSON keyring file for content encryption; back up separately from the database. " +
				"Defaults to <sqlite db dir>/padmark-keys.json or ./padmark-keys.json for postgres; " +
				"serve creates a missing default file (container default: /data/padmark-keys.json)"
		}),
		allowSharedKeyringFlag(),
		skipMigrationBackupFlag(),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagAddr
			flag.Sources = cli.EnvVars(EnvAddr)
			flag.Value = DefaultAddr
			flag.Usage = "HTTP listen address"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagStorage
			flag.Sources = cli.EnvVars(EnvStorage)
			flag.Value = DefaultStorage
			flag.Usage = "Storage backend: sqlite, postgres"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagDSN
			flag.Sources = cli.EnvVars(EnvDSN)
			flag.Value = DefaultDSN
			flag.Usage = "Database DSN (file path for sqlite, connection string for postgres). " +
				"A postgres DSN carries the password and a command-line flag is visible to every local " +
				"user in the process listing (ps); set it via PADMARK_DSN instead"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagLogLevel
			flag.Validator = logging.ValidateLevel
			flag.Sources = cli.EnvVars(EnvLogLevel)
			flag.Value = DefaultLogLevel
			flag.Usage = "Log level: debug, info, warn, error"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagLogFormat
			flag.Sources = cli.EnvVars(EnvLogFormat)
			flag.Value = DefaultLogFormat
			flag.Usage = "Log format: json, text"
		}),
	}
}

// accessFlags returns the flags that control accounts, the REST API and public slugs.
func accessFlags() []cli.Flag {
	return []cli.Flag{
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagEnableAccounts
			flag.Sources = cli.EnvVars(EnvEnableAccounts)
			flag.Value = false
			flag.Usage = "Enable the user-account system (TOTP login, /setup, /admin, private-note gating). " +
				"Off by default: the site is fully public unless this is set"
		}),
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagDisableAPI
			flag.Sources = cli.EnvVars(EnvDisableAPI)
			flag.Value = false
			flag.Usage = "Disable the REST/JSON API (/notes*, /links*, /api, /api/openapi.yaml, /llms.txt, " +
				"/api/tokens, /api/invites): every request reaching the disabling middleware gets 503 with " +
				"code=api_disabled. The web UI becomes read-only: viewing /{id} (incl. JSON/raw reads), " +
				"short links, login/admin pages and health checks keep working, but publishing or saving from " +
				"the editor fails with the API-disabled error (it calls POST /notes and PUT /notes/{id})"
		}),
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagCustomSlugs
			flag.Sources = cli.EnvVars(EnvCustomSlugs)
			// On by default like v0.1.6, which always accepted a slug; set false to issue only
			// random slugs, because human-readable public URLs are easier to guess.
			flag.Value = true
			flag.Usage = "Allow user-supplied human-readable slugs at note creation (e.g. project/GUIDE.md) " +
				"at short-link creation (e.g. launch-2026, single segment only) " +
				"and at file upload (single segment only). " +
				"On by default for v0.1.6 compatibility; set false to issue only random slugs, " +
				"because human-readable public URLs are easier to guess. " +
				"New content encryption keys are independent of the slug"
		}),
	}
}

// limitFlags returns the HTTP timeout, request size and rate limit flags.
func limitFlags() []cli.Flag {
	return []cli.Flag{
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagReadTimeout
			flag.Sources = cli.EnvVars(EnvReadTimeout)
			flag.Value = DefaultReadTimeout
			flag.Usage = "HTTP read timeout in seconds"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagWriteTimeout
			flag.Sources = cli.EnvVars(EnvWriteTimeout)
			flag.Value = DefaultWriteTimeout
			flag.Usage = "HTTP write timeout in seconds"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagMaxHeaderBytes
			flag.Sources = cli.EnvVars(EnvMaxHeaderBytes)
			flag.Value = DefaultMaxHeaderBytes
			flag.Usage = "Maximum size of request headers in bytes"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagMaxBodyBytes
			flag.Sources = cli.EnvVars(EnvMaxBodyBytes)
			flag.Value = DefaultMaxBodyBytes
			flag.Usage = "Maximum size of request body in bytes"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagMaxContentLength
			flag.Sources = cli.EnvVars(EnvMaxContentLength)
			flag.Value = DefaultMaxContentLength
			flag.Validator = validateMaxContentLength
			flag.Usage = "Maximum note content length in characters (0 = the --max-body-bytes value). " +
				"Raise --max-body-bytes too: the request body holds the content"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagRateLimit
			flag.Sources = cli.EnvVars(EnvRateLimit)
			flag.Value = DefaultRateLimit
			flag.Usage = "Rate limit: requests per second per IP (0 = disabled)"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagRateBurst
			flag.Sources = cli.EnvVars(EnvRateBurst)
			flag.Value = DefaultRateBurst
			flag.Usage = "Rate limit: max burst size per IP"
		}),
		clifactory.DurationFlag(func(flag *cli.DurationFlag) {
			flag.Name = FlagShutdownTimeout
			flag.Sources = cli.EnvVars(EnvShutdownTimeout)
			flag.Value = DefaultShutdownTimeout
			flag.Validator = validateShutdownTimeout
			flag.Usage = "Limit of the full shutdown sequence, for example 2m. Open requests, such as file " +
				"transfers, get this time less one fifth (at most 2s). Then the server closes their " +
				"connections and stops background work before the database closes. Set the stop grace " +
				"period of the container to a larger value"
		}),
	}
}

// The validator errors stay next to their flags, because only the flag set uses them.
var (
	errMaxFileSizeTooLarge     = errors.New("max file size must not be larger than 48 GiB (51539607552 bytes)")
	errInvalidShutdownTimeout  = errors.New("shutdown timeout must be larger than 0")
	errInvalidMaxContentLength = errors.New("max content length must not be negative; " +
		"use 0 for the --max-body-bytes value")
	errInvalidFileIdleTimeout = errors.New("file idle timeout must not be negative; use 0 for the default")
	errInvalidUploadLimit     = errors.New("upload limit must not be negative; use 0 for the default")
)

// validateMaxFileSize keeps --max-file-size between 1 byte and maxFileSizeLimit.
func validateMaxFileSize(value int64) error {
	if value < 1 {
		return errInvalidMaxFileSize
	}

	if value > maxFileSizeLimit {
		return errMaxFileSizeTooLarge
	}

	return nil
}

// validateSweepInterval rejects a negative --files-sweep-interval; 0 turns the sweep off.
func validateSweepInterval(value time.Duration) error {
	if value < 0 {
		return errInvalidSweepInterval
	}

	return nil
}

// validateShutdownTimeout rejects a --shutdown-timeout that is not positive: a zero limit
// would skip the shutdown of each resource.
func validateShutdownTimeout(value time.Duration) error {
	if value <= 0 {
		return errInvalidShutdownTimeout
	}

	return nil
}

// validateFileIdleTimeout rejects a negative --file-idle-timeout; 0 selects the default.
func validateFileIdleTimeout(value time.Duration) error {
	if value < 0 {
		return errInvalidFileIdleTimeout
	}

	return nil
}

// validateMaxContentLength rejects a negative --max-content-length; 0 selects the
// --max-body-bytes value.
func validateMaxContentLength(value int) error {
	if value < 0 {
		return errInvalidMaxContentLength
	}

	return nil
}

// maxContentLength gives the note content limit: --max-content-length, or --max-body-bytes when
// the flag is 0. A note cannot be larger than the body that holds it, so one setting is enough.
func maxContentLength(cmd *cli.Command) int {
	limit := cmd.Int(FlagMaxContentLength)
	if limit == 0 {
		return cmd.Int(FlagMaxBodyBytes)
	}

	return limit
}

// validateUploadLimit rejects a negative --max-uploads or --max-uploads-per-client; 0 selects
// the default.
func validateUploadLimit(value int) error {
	if value < 0 {
		return errInvalidUploadLimit
	}

	return nil
}

// transportFlags returns the TLS, host, proxy and scheme flags.
func transportFlags() []cli.Flag {
	return []cli.Flag{
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagTLSCert
			flag.Sources = cli.EnvVars(EnvTLSCert)
			flag.Usage = "Path to TLS certificate file (PEM); enables HTTPS when set together with --tls-key"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagTLSKey
			flag.Sources = cli.EnvVars(EnvTLSKey)
			flag.Usage = "Path to TLS private key file (PEM); enables HTTPS when set together with --tls-cert"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagHTTPRedirectAddr
			flag.Sources = cli.EnvVars(EnvHTTPRedirectAddr)
			flag.Usage = "Address for the HTTP→HTTPS redirect listener (e.g. :80); only active when TLS is enabled"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagAllowedHosts
			flag.Sources = cli.EnvVars(EnvAllowedHosts)
			flag.Usage = "Comma-separated Host allowlist (e.g. example.com,www.example.com), " +
				"compared case-insensitively without the port; applies to the main listener " +
				"(other Hosts get 421 Misdirected Request, /healthz and /readyz exempt) and to " +
				"the HTTP→HTTPS redirect listener (400); empty = no Host check (legacy behaviour)"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagTrustedProxies
			flag.Sources = cli.EnvVars(EnvTrustedProxies)
			flag.Usage = "Comma-separated list of trusted proxy CIDRs or IPs (e.g. 10.0.0.0/8,127.0.0.1); " +
				"X-Forwarded-For and X-Real-IP are only trusted from these addresses"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagPublicScheme
			flag.Sources = cli.EnvVars(EnvPublicScheme)
			flag.Usage = "Force the scheme (http or https) used when building absolute links back to this " +
				"server (note URLs, invite links, API-token envelopes), instead of auto-detecting it " +
				"from TLS/X-Forwarded-Proto; https also marks session/CSRF cookies Secure and enables " +
				"HSTS; set this when the reverse proxy's IP is not stable enough for --trusted-proxies " +
				"(e.g. cloud load balancers, k8s ingress)"
		}),
	}
}

// sessionFlags returns the TOTP, session and removed-flag hint flags.
func sessionFlags() []cli.Flag {
	return []cli.Flag{
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagTOTPIssuer
			flag.Sources = cli.EnvVars(EnvTOTPIssuer)
			flag.Value = DefaultTOTPIssuer
			flag.Usage = "TOTP issuer name shown in the authenticator app (default: padmark)"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagAuthTokens
			flag.Sources = cli.EnvVars(EnvAuthTokens)
			flag.Hidden = true
			flag.Usage = "REMOVED: static bearer tokens were replaced by the account system; setting this " +
				"fails startup (use --enable-accounts, " + EnvBootstrapToken + " and /setup)"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagCookieMaxAge
			flag.Sources = cli.EnvVars(EnvCookieMaxAge)
			flag.Hidden = true
			flag.Usage = "DEPRECATED: use --session-ttl; honoured as the session TTL (seconds) " +
				"when --session-ttl is unset"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagSessionTTL
			flag.Sources = cli.EnvVars(EnvSessionTTL)
			flag.Value = DefaultSessionTTL
			flag.Usage = "TOTP session TTL in seconds (default: 30 days)"
		}),
	}
}

// miscFlags returns the redirect, metrics and CSRF flags.
func miscFlags() []cli.Flag {
	return []cli.Flag{
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagDirectRedirect
			flag.Sources = cli.EnvVars(EnvDirectRedirect)
			flag.Value = DefaultDirectRedirect
			flag.Usage = "Redirect behaviour for short links: true issues an immediate 301 to the " +
				"target URL for every link; false (default) renders an interstitial page (meta-refresh + " +
				"noscript fallback) showing the destination for links created anonymously, while links " +
				"created by a signed-in owner still redirect directly (open-redirect/phishing hardening)"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagMetricsAddr
			flag.Sources = cli.EnvVars(EnvMetricsAddr)
			flag.Usage = "Address for a separate internal Prometheus /metrics listener (e.g. :9090); " +
				"empty (default) disables it entirely. Bind this to a private interface only — " +
				"metrics are never served on the main, public-facing listener"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagCSRFSecret
			flag.Sources = cli.EnvVars(EnvCSRFSecret)
			flag.Usage = "HMAC key for signing CSRF tokens (raw bytes, at least 32 characters). " +
				"Flag values are visible in process listings (ps); prefer PADMARK_CSRF_SECRET. " +
				"Empty (default) generates a fresh random secret per process: forms survive only " +
				"within one process lifetime, so restarts invalidate outstanding tokens and " +
				"multi-replica deployments 403 on every form. Set to a fixed random value " +
				"(e.g. `openssl rand -base64 32`) shared across replicas to avoid that"
		}),
	}
}

// argon2Flags is shared by serve and the encryption commands: encryption migrate hashes the
// caller-chosen edit codes of converted legacy notes with the same argon2id cost as serve.
func argon2Flags() []cli.Flag {
	return []cli.Flag{
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagArgon2Memory
			flag.Validator = boundedCost(int(crypto.MaxArgon2Memory))
			flag.Sources = cli.EnvVars(EnvArgon2Memory)
			flag.Value = DefaultArgon2Memory
			flag.Usage = "argon2id memory cost in KiB for account passwords, caller-chosen edit codes and the " +
				"per-user TOTP key (default: 24576 = 24 MiB); every derivation allocates this much, bounded " +
				"overall by --argon2-memory-budget. Server-generated edit codes use a fast hash instead"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagArgon2Time
			flag.Validator = boundedCost(int(crypto.MaxArgon2Time))
			flag.Sources = cli.EnvVars(EnvArgon2Time)
			flag.Value = DefaultArgon2Time
			flag.Usage = "argon2id time cost (iterations); raise when lowering memory to keep strength"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagArgon2Threads
			flag.Validator = boundedCost(int(crypto.MaxArgon2Threads))
			flag.Sources = cli.EnvVars(EnvArgon2Threads)
			flag.Value = DefaultArgon2Threads
			flag.Usage = "argon2id parallelism (CPU threads per hash)"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagArgon2MemoryBudget
			flag.Validator = boundedCost(maxArgon2MemoryBudget)
			flag.Sources = cli.EnvVars(EnvArgon2MemoryBudget)
			flag.Value = DefaultArgon2MemoryBudget
			flag.Usage = "total argon2id memory in KiB that concurrent password, edit-code and TOTP-key " +
				"derivations may hold at once (default: 131072 = 128 MiB); further requests queue. " +
				"Must be at least --argon2-memory. Size it below the container memory limit and set " +
				"GOMEMLIMIT to about 75-80% of that limit"
		}),
	}
}

// skipMigrationBackupFlag is shared by serve and migrate, which both apply migrations.
func skipMigrationBackupFlag() *cli.BoolFlag {
	return clifactory.BoolFlag(func(flag *cli.BoolFlag) {
		flag.Name = FlagSkipMigrationBackup
		flag.Sources = cli.EnvVars(EnvSkipMigrationBackup)
		flag.Value = false
		flag.Usage = "Do not back up the SQLite database before applying pending migrations. By default, when " +
			"migrations are pending and the database is a file, a copy is written next to it as " +
			"<db>.bak-<YYYYMMDD-HHMMSS> (mode 0600) first, because some migrations are irreversible. " +
			"Set only if backups are managed elsewhere; ignored for postgres (use pg_dump)"
	})
}

// allowSharedKeyringFlag is shared by serve and the encryption commands, which load the same keyring.
func allowSharedKeyringFlag() *cli.BoolFlag {
	return clifactory.BoolFlag(func(flag *cli.BoolFlag) {
		flag.Name = FlagAllowSharedKeyring
		flag.Sources = cli.EnvVars(EnvAllowSharedKeyring)
		flag.Value = DefaultAllowSharedKeyring
		flag.Usage = "Accept a content keyring file readable by group or other users (logged as a warning). " +
			"Startup otherwise refuses such a file; restrict it to 0600 or 0400 instead whenever possible. " +
			"Only for secret managers that cannot provide an owner-only file; keyrings under /run/secrets/ " +
			"(Docker/Compose secrets) are already exempt"
	})
}

// maxArgon2MemoryBudget caps --argon2-memory-budget at 64 GiB (in KiB): it rejects typos and keeps
// the value inside uint32 on every platform's int.
const maxArgon2MemoryBudget = 64 * 1024 * 1024

// boundedCost rejects negative and overflowing flag values before narrowing to uint32/uint8.
func boundedCost(maximum int) func(int) error {
	return func(value int) error {
		if value < 1 || value > maximum {
			return fmt.Errorf("%w 1 and %d", errInvalidArgon2Cost, maximum)
		}

		return nil
	}
}

// uint32Flag narrows an integer flag to uint32. The flag Validator already bounds the value;
// the repeated range check keeps the conversion provably safe where it happens.
func uint32Flag(cmd *cli.Command, name string) (uint32, error) {
	value := cmd.Int(name)
	if value < 0 || value > math.MaxUint32 {
		return 0, fmt.Errorf("--%s=%d: %w", name, value, errFlagOutOfRange)
	}

	return uint32(value), nil
}

// uint8Flag narrows an integer flag to uint8, with the same range check as uint32Flag.
func uint8Flag(cmd *cli.Command, name string) (uint8, error) {
	value := cmd.Int(name)
	if value < 0 || value > math.MaxUint8 {
		return 0, fmt.Errorf("--%s=%d: %w", name, value, errFlagOutOfRange)
	}

	return uint8(value), nil
}
