package mcp

import (
	"time"

	"github.com/partyzanex/padmark/internal/clientauth"
)

// Binary flag names. Flag/Env values intentionally shadow padmark-cli's: both adapters accept
// the same credential and the same timeout semantics, and adapters never reach into each
// other's packages, so the constants are duplicated here on purpose.
const (
	FlagToken   = "token" // same flag name and semantics as padmark-cli.
	FlagBaseURL = "base-url"
	FlagTimeout = "timeout" // same flag name and semantics as padmark-cli.
	// FlagAllowInsecureHTTP permits sending the token over plain HTTP to a non-loopback host.
	FlagAllowInsecureHTTP = clientauth.FlagAllowInsecureHTTP
	// FlagLogLevel selects the minimum slog level; logs go to stderr only (stdout is the protocol).
	FlagLogLevel = "log-level"
	// FlagUploadRoot is the only local directory from which upload_file reads files.
	FlagUploadRoot = "upload-root"
)

// Environment variable names, shared with padmark-cli: one .env (PADMARK_TOKEN, PADMARK_URL,
// PADMARK_TIMEOUT) configures both binaries.
const (
	EnvToken   = "PADMARK_TOKEN"
	EnvBaseURL = "PADMARK_URL"
	EnvTimeout = "PADMARK_TIMEOUT"
	// EnvAllowInsecureHTTP is the environment source of --allow-insecure-http.
	EnvAllowInsecureHTTP = clientauth.EnvAllowInsecureHTTP
	// EnvLogLevel is the same variable the padmark server reads for its log level.
	EnvLogLevel = "PADMARK_LOG_LEVEL"
	// EnvUploadRoot is the environment source of --upload-root. Only padmark-mcp reads it.
	EnvUploadRoot = "PADMARK_MCP_UPLOAD_ROOT"
)

// DefaultUploadRoot is empty: upload_file refuses all paths until the operator sets a root.
// The agent host selects the working directory of a stdio server, often the home directory.
// A working-directory default could then send ~/.ssh or ~/.aws to a public URL.
const DefaultUploadRoot = ""

// DefaultLogLevel keeps per-call debug records out of stderr unless asked for.
const DefaultLogLevel = "info"

// DefaultBaseURL is the default padmark server address (same default as padmark-cli).
const DefaultBaseURL = "http://localhost:8080"

// DefaultTimeout limits each tool call, so the call fails fast on a slow or unresponsive
// server. For upload_file, it limits only the wait for the response headers. Override with
// --timeout (0 disables the limit).
const DefaultTimeout = 30 * time.Second
