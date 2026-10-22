package mcp

import "time"

// Binary flag names. Flag/Env values intentionally shadow padmark-cli's: both adapters accept
// the same credential and the same timeout semantics, and adapters never reach into each
// other's packages, so the constants are duplicated here on purpose.
const (
	FlagToken   = "token" // same flag name and semantics as padmark-cli
	FlagBaseURL = "base-url"
	FlagTimeout = "timeout" // same flag name and semantics as padmark-cli
	// FlagAllowInsecureHTTP permits sending the token over plain HTTP to a non-loopback host.
	FlagAllowInsecureHTTP = "allow-insecure-http"
	// FlagLogLevel selects the minimum slog level; logs go to stderr only (stdout is the protocol).
	FlagLogLevel = "log-level"
)

// Environment variable names, shared with padmark-cli: one .env (PADMARK_TOKEN, PADMARK_URL,
// PADMARK_TIMEOUT) configures both binaries.
const (
	EnvToken   = "PADMARK_TOKEN"
	EnvBaseURL = "PADMARK_URL"
	EnvTimeout = "PADMARK_TIMEOUT"
	// EnvAllowInsecureHTTP is the environment source of --allow-insecure-http.
	EnvAllowInsecureHTTP = "PADMARK_ALLOW_INSECURE_HTTP"
	// EnvLogLevel is the same variable the padmark server reads for its log level.
	EnvLogLevel = "PADMARK_LOG_LEVEL"
)

// DefaultLogLevel keeps per-call debug records out of stderr unless asked for.
const DefaultLogLevel = "info"

// DefaultBaseURL is the default padmark server address (same default as padmark-cli).
const DefaultBaseURL = "http://localhost:8080"

// DefaultTimeout bounds each request so a tool call fails fast instead of hanging on a slow or
// unresponsive server. Override with --timeout (0 disables the bound).
const DefaultTimeout = 30 * time.Second
