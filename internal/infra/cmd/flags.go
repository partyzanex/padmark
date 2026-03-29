package cmd

import "github.com/urfave/cli/v3"

// Flag names used in CLI flags.
const (
	FlagAddr             = "addr"
	FlagStorage          = "storage"
	FlagDSN              = "dsn"
	FlagLogLevel         = "log-level"
	FlagLogFormat        = "log-format"
	FlagAuthTokens       = "auth-tokens" //nolint:gosec // flag name, not a credential
	FlagCookieMaxAge     = "cookie-max-age"
	FlagReadTimeout      = "read-timeout"
	FlagMaxHeaderBytes   = "max-header-bytes"
	FlagMaxBodyBytes     = "max-body-bytes"
	FlagRateLimit        = "rate-limit"
	FlagRateBurst        = "rate-burst"
	FlagTLSCert          = "tls-cert"
	FlagTLSKey           = "tls-key"
	FlagHTTPRedirectAddr = "http-redirect-addr"
	FlagTrustedProxies   = "trusted-proxies"
)

// Env vars used to configure the service (prefix: PADMARK_*).
const (
	EnvAddr             = "PADMARK_ADDR"
	EnvStorage          = "PADMARK_STORAGE"
	EnvDSN              = "PADMARK_DSN"
	EnvLogLevel         = "PADMARK_LOG_LEVEL"
	EnvLogFormat        = "PADMARK_LOG_FORMAT"
	EnvAuthTokens       = "PADMARK_AUTH_TOKENS" //nolint:gosec // env var name, not a credential
	EnvCookieMaxAge     = "PADMARK_COOKIE_MAX_AGE"
	EnvReadTimeout      = "PADMARK_READ_TIMEOUT"
	EnvMaxHeaderBytes   = "PADMARK_MAX_HEADER_BYTES"
	EnvMaxBodyBytes     = "PADMARK_MAX_BODY_BYTES"
	EnvRateLimit        = "PADMARK_RATE_LIMIT"
	EnvRateBurst        = "PADMARK_RATE_BURST"
	EnvTLSCert          = "PADMARK_TLS_CERT"
	EnvTLSKey           = "PADMARK_TLS_KEY"
	EnvHTTPRedirectAddr = "PADMARK_HTTP_REDIRECT_ADDR"
	EnvTrustedProxies   = "PADMARK_TRUSTED_PROXIES"
)

// Default values for all flags.
const (
	DefaultAddr           = ":8080"
	DefaultStorage        = "sqlite"
	DefaultDSN            = "padmark.db"
	DefaultLogLevel       = "info"
	DefaultLogFormat      = "json"
	DefaultCookieMaxAge   = 90 * 24 * 60 * 60 // 3 months in seconds
	DefaultReadTimeout    = 30                // seconds
	DefaultMaxHeaderBytes = 64 * 1024         // 64 KB
	DefaultMaxBodyBytes   = 256 * 1024        // 256 KB
	DefaultRateLimit      = 10                // requests per second per IP
	DefaultRateBurst      = 20                // max burst size per IP
)

// appFlags returns the full flag set for the serve subcommand.
func appFlags() []cli.Flag { //nolint:funlen // declarative flag list
	return []cli.Flag{
		&cli.StringFlag{
			Name:    FlagAddr,
			Sources: cli.EnvVars(EnvAddr),
			Value:   DefaultAddr,
			Usage:   "HTTP listen address",
		},
		&cli.StringFlag{
			Name:    FlagStorage,
			Sources: cli.EnvVars(EnvStorage),
			Value:   DefaultStorage,
			Usage:   "Storage backend: sqlite, postgres",
		},
		&cli.StringFlag{
			Name:    FlagDSN,
			Sources: cli.EnvVars(EnvDSN),
			Value:   DefaultDSN,
			Usage:   "Database DSN (file path for sqlite, connection string for postgres)",
		},
		&cli.StringFlag{
			Name:    FlagLogLevel,
			Sources: cli.EnvVars(EnvLogLevel),
			Value:   DefaultLogLevel,
			Usage:   "Log level: debug, info, warn, error",
		},
		&cli.StringFlag{
			Name:    FlagLogFormat,
			Sources: cli.EnvVars(EnvLogFormat),
			Value:   DefaultLogFormat,
			Usage:   "Log format: json, text",
		},
		&cli.StringFlag{
			Name:    FlagAuthTokens,
			Sources: cli.EnvVars(EnvAuthTokens),
			Usage:   "Comma-separated Bearer tokens for write endpoints (empty = no auth)",
		},
		&cli.IntFlag{
			Name:    FlagCookieMaxAge,
			Sources: cli.EnvVars(EnvCookieMaxAge),
			Value:   DefaultCookieMaxAge,
			Usage:   "Auth cookie max-age in seconds (default: 3 months)",
		},
		&cli.IntFlag{
			Name:    FlagReadTimeout,
			Sources: cli.EnvVars(EnvReadTimeout),
			Value:   DefaultReadTimeout,
			Usage:   "HTTP read timeout in seconds",
		},
		&cli.IntFlag{
			Name:    FlagMaxHeaderBytes,
			Sources: cli.EnvVars(EnvMaxHeaderBytes),
			Value:   DefaultMaxHeaderBytes,
			Usage:   "Maximum size of request headers in bytes",
		},
		&cli.IntFlag{
			Name:    FlagMaxBodyBytes,
			Sources: cli.EnvVars(EnvMaxBodyBytes),
			Value:   DefaultMaxBodyBytes,
			Usage:   "Maximum size of request body in bytes",
		},
		&cli.IntFlag{
			Name:    FlagRateLimit,
			Sources: cli.EnvVars(EnvRateLimit),
			Value:   DefaultRateLimit,
			Usage:   "Rate limit: requests per second per IP (0 = disabled)",
		},
		&cli.IntFlag{
			Name:    FlagRateBurst,
			Sources: cli.EnvVars(EnvRateBurst),
			Value:   DefaultRateBurst,
			Usage:   "Rate limit: max burst size per IP",
		},
		&cli.StringFlag{
			Name:    FlagTLSCert,
			Sources: cli.EnvVars(EnvTLSCert),
			Usage:   "Path to TLS certificate file (PEM); enables HTTPS when set together with --tls-key",
		},
		&cli.StringFlag{
			Name:    FlagTLSKey,
			Sources: cli.EnvVars(EnvTLSKey),
			Usage:   "Path to TLS private key file (PEM); enables HTTPS when set together with --tls-cert",
		},
		&cli.StringFlag{
			Name:    FlagHTTPRedirectAddr,
			Sources: cli.EnvVars(EnvHTTPRedirectAddr),
			Usage:   "Address for the HTTP→HTTPS redirect listener (e.g. :80); only active when TLS is enabled",
		},
		&cli.StringFlag{
			Name:    FlagTrustedProxies,
			Sources: cli.EnvVars(EnvTrustedProxies),
			Usage: "Comma-separated list of trusted proxy CIDRs or IPs (e.g. 10.0.0.0/8,127.0.0.1); " +
				"X-Forwarded-For and X-Real-IP are only trusted from these addresses",
		},
	}
}
