package mcp

import "time"

// Binary flag names. Flag/Env values intentionally shadow padmark-cli's: both adapters accept
// the same credential and the same timeout semantics, and adapters never reach into each
// other's packages, so the constants are duplicated here on purpose.
const (
	FlagToken   = "token" // same flag name and semantics as padmark-cli
	FlagBaseURL = "base-url"
	FlagTimeout = "timeout" // same flag name and semantics as padmark-cli
)

// Environment variable names, shared with padmark-cli: one .env (PADMARK_TOKEN, PADMARK_URL,
// PADMARK_TIMEOUT) configures both binaries.
const (
	EnvToken   = "PADMARK_TOKEN"
	EnvBaseURL = "PADMARK_URL"
	EnvTimeout = "PADMARK_TIMEOUT"
)

// DefaultBaseURL is the default padmark server address (same default as padmark-cli).
const DefaultBaseURL = "http://localhost:8080"

// DefaultTimeout bounds each request so a tool call fails fast instead of hanging on a slow or
// unresponsive server. Override with --timeout (0 disables the bound).
const DefaultTimeout = 30 * time.Second
