package cmd

// Flag names used in CLI flags.
const (
	FlagAddr       = "addr"
	FlagStorage    = "storage"
	FlagDSN        = "dsn"
	FlagLogLevel   = "log-level"
	FlagLogFormat  = "log-format"
	FlagAuthTokens = "auth-tokens" //nolint:gosec // flag name, not a credential
)

// Env vars used to configure the service (prefix: PADMARK_*).
const (
	EnvAddr       = "PADMARK_ADDR"
	EnvStorage    = "PADMARK_STORAGE"
	EnvDSN        = "PADMARK_DSN"
	EnvLogLevel   = "PADMARK_LOG_LEVEL"
	EnvLogFormat  = "PADMARK_LOG_FORMAT"
	EnvAuthTokens = "PADMARK_AUTH_TOKENS" //nolint:gosec // env var name, not a credential
)

// Default values for all flags.
const (
	DefaultAddr      = ":8080"
	DefaultStorage   = "sqlite"
	DefaultDSN       = "padmark.db"
	DefaultLogLevel  = "info"
	DefaultLogFormat = "json"
)
