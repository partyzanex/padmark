package server

import "github.com/urfave/cli/v3"

// Flag names used in CLI flags.
const (
	FlagAddr      = "addr"
	FlagStorage   = "storage"
	FlagDSN       = "dsn"
	FlagLogLevel  = "log-level"
	FlagLogFormat = "log-format"
	// Deprecated: the PADMARK_AUTH_TOKENS bearer-token write auth is legacy and superseded by
	// the TOTP account system (--enable-accounts). It will be removed in a future release.
	FlagAuthTokens       = "auth-tokens" //nolint:gosec // flag name, not a credential
	FlagEnableAccounts   = "enable-accounts"
	FlagCookieMaxAge     = "cookie-max-age"
	FlagReadTimeout      = "read-timeout"
	FlagWriteTimeout     = "write-timeout"
	FlagMaxHeaderBytes   = "max-header-bytes"
	FlagMaxBodyBytes     = "max-body-bytes"
	FlagRateLimit        = "rate-limit"
	FlagRateBurst        = "rate-burst"
	FlagTLSCert          = "tls-cert"
	FlagTLSKey           = "tls-key"
	FlagHTTPRedirectAddr = "http-redirect-addr"
	FlagAllowedHosts     = "allowed-hosts"
	FlagTrustedProxies   = "trusted-proxies"
	FlagTOTPIssuer       = "totp-issuer"
	FlagSessionTTL       = "session-ttl"
	FlagArgon2Memory     = "argon2-memory"
	FlagArgon2Time       = "argon2-time"
	FlagArgon2Threads    = "argon2-threads"
)

// Env vars used to configure the service (prefix: PADMARK_*).
const (
	EnvAddr      = "PADMARK_ADDR"
	EnvStorage   = "PADMARK_STORAGE"
	EnvDSN       = "PADMARK_DSN"
	EnvLogLevel  = "PADMARK_LOG_LEVEL"
	EnvLogFormat = "PADMARK_LOG_FORMAT"
	// Deprecated: bearer-token write auth is legacy; use the TOTP account system
	// (PADMARK_ENABLE_ACCOUNTS) instead. PADMARK_AUTH_TOKENS will be removed in a future release.
	EnvAuthTokens       = "PADMARK_AUTH_TOKENS" //nolint:gosec // env var name, not a credential
	EnvEnableAccounts   = "PADMARK_ENABLE_ACCOUNTS"
	EnvCookieMaxAge     = "PADMARK_COOKIE_MAX_AGE"
	EnvReadTimeout      = "PADMARK_READ_TIMEOUT"
	EnvWriteTimeout     = "PADMARK_WRITE_TIMEOUT"
	EnvMaxHeaderBytes   = "PADMARK_MAX_HEADER_BYTES"
	EnvMaxBodyBytes     = "PADMARK_MAX_BODY_BYTES"
	EnvRateLimit        = "PADMARK_RATE_LIMIT"
	EnvRateBurst        = "PADMARK_RATE_BURST"
	EnvTLSCert          = "PADMARK_TLS_CERT"
	EnvTLSKey           = "PADMARK_TLS_KEY"
	EnvHTTPRedirectAddr = "PADMARK_HTTP_REDIRECT_ADDR"
	EnvAllowedHosts     = "PADMARK_ALLOWED_HOSTS"
	EnvTrustedProxies   = "PADMARK_TRUSTED_PROXIES"
	EnvTOTPIssuer       = "PADMARK_TOTP_ISSUER"
	EnvSessionTTL       = "PADMARK_SESSION_TTL"
	EnvArgon2Memory     = "PADMARK_ARGON2_MEMORY"
	EnvArgon2Time       = "PADMARK_ARGON2_TIME"
	EnvArgon2Threads    = "PADMARK_ARGON2_THREADS"
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
	DefaultWriteTimeout   = 60                // seconds
	DefaultMaxHeaderBytes = 64 * 1024         // 64 KB
	DefaultMaxBodyBytes   = 4 * 1024 * 1024   // 4 MB
	DefaultRateLimit      = 10                // requests per second per IP
	DefaultRateBurst      = 20                // max burst size per IP
	DefaultTOTPIssuer     = "padmark"
	DefaultSessionTTL     = 30 * 24 * 60 * 60 // 30 days in seconds
	DefaultArgon2Memory   = 24 * 1024         // argon2id memory in KiB (24 MiB)
	DefaultArgon2Time     = 2                 // argon2id iterations (OWASP minimum at 64 MiB)
	DefaultArgon2Threads  = 1                 // argon2id parallelism
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
			Usage: "DEPRECATED (use --enable-accounts / TOTP instead): comma-separated Bearer " +
				"tokens for write endpoints (empty = no auth). Will be removed in a future release",
		},
		&cli.BoolFlag{
			Name:    FlagEnableAccounts,
			Sources: cli.EnvVars(EnvEnableAccounts),
			Value:   false,
			Usage: "Enable the user-account system (TOTP login, /setup, /admin, private-note gating). " +
				"Off by default: the site is fully public unless this is set",
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
			Name:    FlagWriteTimeout,
			Sources: cli.EnvVars(EnvWriteTimeout),
			Value:   DefaultWriteTimeout,
			Usage:   "HTTP write timeout in seconds",
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
			Name:    FlagAllowedHosts,
			Sources: cli.EnvVars(EnvAllowedHosts),
			Usage: "Comma-separated host allowlist for the HTTP→HTTPS redirect (e.g. example.com,www.example.com); " +
				"when set, requests with any other Host get 400 instead of a redirect (anti Host-header injection); " +
				"empty = redirect to the request Host (legacy behaviour)",
		},
		&cli.StringFlag{
			Name:    FlagTrustedProxies,
			Sources: cli.EnvVars(EnvTrustedProxies),
			Usage: "Comma-separated list of trusted proxy CIDRs or IPs (e.g. 10.0.0.0/8,127.0.0.1); " +
				"X-Forwarded-For and X-Real-IP are only trusted from these addresses",
		},
		&cli.StringFlag{
			Name:    FlagTOTPIssuer,
			Sources: cli.EnvVars(EnvTOTPIssuer),
			Value:   DefaultTOTPIssuer,
			Usage:   "TOTP issuer name shown in the authenticator app (default: padmark)",
		},
		&cli.IntFlag{
			Name:    FlagSessionTTL,
			Sources: cli.EnvVars(EnvSessionTTL),
			Value:   DefaultSessionTTL,
			Usage:   "TOTP session TTL in seconds (default: 30 days)",
		},
		&cli.IntFlag{
			Name:    FlagArgon2Memory,
			Sources: cli.EnvVars(EnvArgon2Memory),
			Value:   DefaultArgon2Memory,
			Usage:   "argon2id memory cost in KiB for password/edit-code hashing (default: 65536 = 64 MiB)",
		},
		&cli.IntFlag{
			Name:    FlagArgon2Time,
			Sources: cli.EnvVars(EnvArgon2Time),
			Value:   DefaultArgon2Time,
			Usage:   "argon2id time cost (iterations); raise when lowering memory to keep strength",
		},
		&cli.IntFlag{
			Name:    FlagArgon2Threads,
			Sources: cli.EnvVars(EnvArgon2Threads),
			Value:   DefaultArgon2Threads,
			Usage:   "argon2id parallelism (CPU threads per hash)",
		},
	}
}
