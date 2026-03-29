package cmd

// Flag names used in CLI flags.
const (
	FlagAddr           = "addr"
	FlagStorage        = "storage"
	FlagDSN            = "dsn"
	FlagLogLevel       = "log-level"
	FlagLogFormat      = "log-format"
	FlagAuthTokens     = "auth-tokens" //nolint:gosec // flag name, not a credential
	FlagCookieMaxAge   = "cookie-max-age"
	FlagReadTimeout    = "read-timeout"
	FlagMaxHeaderBytes = "max-header-bytes"
	FlagMaxBodyBytes   = "max-body-bytes"
	FlagRateLimit      = "rate-limit"
	FlagRateBurst      = "rate-burst"
	FlagTLSCert        = "tls-cert"
	FlagTLSKey         = "tls-key"
)

// Env vars used to configure the service (prefix: PADMARK_*).
const (
	EnvAddr           = "PADMARK_ADDR"
	EnvStorage        = "PADMARK_STORAGE"
	EnvDSN            = "PADMARK_DSN"
	EnvLogLevel       = "PADMARK_LOG_LEVEL"
	EnvLogFormat      = "PADMARK_LOG_FORMAT"
	EnvAuthTokens     = "PADMARK_AUTH_TOKENS" //nolint:gosec // env var name, not a credential
	EnvCookieMaxAge   = "PADMARK_COOKIE_MAX_AGE"
	EnvReadTimeout    = "PADMARK_READ_TIMEOUT"
	EnvMaxHeaderBytes = "PADMARK_MAX_HEADER_BYTES"
	EnvMaxBodyBytes   = "PADMARK_MAX_BODY_BYTES"
	EnvRateLimit      = "PADMARK_RATE_LIMIT"
	EnvRateBurst      = "PADMARK_RATE_BURST"
	EnvTLSCert        = "PADMARK_TLS_CERT"
	EnvTLSKey         = "PADMARK_TLS_KEY"
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
