package mcp

import "time"

// Binary flag names. Flag/Env values intentionally shadow padmark-cli's: both adapters accept
// the same credential and the same timeout semantics, and adapters never reach into each
// other's packages, so the constants are duplicated here on purpose.
const (
	FlagToken   = "token" // same flag name and semantics as padmark-cli.
	FlagBaseURL = "base-url"
	FlagTimeout = "timeout" // same flag name and semantics as padmark-cli.
	// FlagAllowInsecureHTTP permits sending the token over plain HTTP to a non-loopback host.
	FlagAllowInsecureHTTP = "allow-insecure-http"
	// FlagLogLevel selects the minimum slog level; logs go to stderr only (stdout is the protocol).
	FlagLogLevel = "log-level"
	// FlagUploadRoot is the only local directory from which upload_file reads files.
	FlagUploadRoot = "upload-root"
	// FlagUploadAllowHidden lets upload_file read a path with a name that starts with a dot.
	FlagUploadAllowHidden = "upload-allow-hidden"
	// FlagUploadAllowSlug lets upload_file send the slug that the agent gives.
	FlagUploadAllowSlug = "upload-allow-slug"
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
	// EnvUploadRoot is the environment source of --upload-root. Only padmark-mcp reads it.
	EnvUploadRoot = "PADMARK_MCP_UPLOAD_ROOT"
	// EnvUploadAllowHidden is the environment source of --upload-allow-hidden.
	EnvUploadAllowHidden = "PADMARK_MCP_UPLOAD_ALLOW_HIDDEN"
	// EnvUploadAllowSlug is the environment source of --upload-allow-slug.
	EnvUploadAllowSlug = "PADMARK_MCP_UPLOAD_ALLOW_SLUG"
)

// DefaultUploadRoot is empty: upload_file refuses all paths until the operator sets a root.
// The agent host selects the working directory of a stdio server, often the home directory.
// A working-directory default could then send ~/.ssh or ~/.aws to a public URL.
const DefaultUploadRoot = ""

// DefaultUploadAllowHidden refuses names that start with a dot, such as .env, .git or .ssh.
// These names often hold secrets, and an agent that follows injected text can ask for them.
const DefaultUploadAllowHidden = false

// DefaultUploadAllowSlug refuses an agent slug. With a known slug, injected text could make the
// agent publish a file at an address that the attacker knows before the upload.
const DefaultUploadAllowSlug = false

// DefaultLogLevel keeps per-call debug records out of stderr unless asked for.
const DefaultLogLevel = "info"

// DefaultBaseURL is the default padmark server address (same default as padmark-cli).
const DefaultBaseURL = "http://localhost:8080"

// DefaultTimeout bounds each request so a tool call fails fast instead of hanging on a slow or
// unresponsive server. Override with --timeout (0 disables the bound).
const DefaultTimeout = 30 * time.Second
