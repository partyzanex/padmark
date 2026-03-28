package cmd

// Flag names used in CLI flags.
const (
	FlagAddr      = "addr"
	FlagDSN       = "dsn"
	FlagLogLevel  = "log-level"
	FlagLogFormat = "log-format"
)

// Env vars used to configure the service (prefix: PADMARK_*).
const (
	EnvAddr      = "PADMARK_ADDR"
	EnvDSN       = "PADMARK_DSN"
	EnvLogLevel  = "PADMARK_LOG_LEVEL"
	EnvLogFormat = "PADMARK_LOG_FORMAT"
)

// Default values for all flags.
const (
	DefaultAddr      = ":8080"
	DefaultDSN       = "padmark.db"
	DefaultLogLevel  = "info"
	DefaultLogFormat = "json"
)
