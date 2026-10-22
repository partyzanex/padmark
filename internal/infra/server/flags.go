package server

import (
	"fmt"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/logging"

	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// Flag names used in CLI flags.
const (
	FlagAddr             = "addr"
	FlagStorage          = "storage"
	FlagDSN              = "dsn"
	FlagLogLevel         = "log-level"
	FlagLogFormat        = "log-format"
	FlagEnableAccounts   = "enable-accounts"
	FlagDisableAPI       = "disable-api"
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
	FlagPublicScheme     = "public-scheme"
	FlagCustomSlugs      = "custom-slugs"
	FlagTOTPIssuer       = "totp-issuer"
	FlagSessionTTL       = "session-ttl"
	FlagArgon2Memory     = "argon2-memory"
	FlagArgon2Time       = "argon2-time"
	FlagArgon2Threads    = "argon2-threads"
	FlagDirectRedirect   = "direct-redirect"
	FlagMetricsAddr      = "metrics-addr"
	FlagCSRFSecret       = "csrf-secret"
	FlagContentKeyring   = "content-keyring-file"
)

// Env vars used to configure the service (prefix: PADMARK_*).
const (
	EnvAddr             = "PADMARK_ADDR"
	EnvStorage          = "PADMARK_STORAGE"
	EnvDSN              = "PADMARK_DSN"
	EnvLogLevel         = "PADMARK_LOG_LEVEL"
	EnvLogFormat        = "PADMARK_LOG_FORMAT"
	EnvEnableAccounts   = "PADMARK_ENABLE_ACCOUNTS"
	EnvDisableAPI       = "PADMARK_DISABLE_API"
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
	EnvPublicScheme     = "PADMARK_PUBLIC_SCHEME"
	EnvCustomSlugs      = "PADMARK_CUSTOM_SLUGS"
	EnvTOTPIssuer       = "PADMARK_TOTP_ISSUER"
	EnvSessionTTL       = "PADMARK_SESSION_TTL"
	EnvArgon2Memory     = "PADMARK_ARGON2_MEMORY"
	EnvArgon2Time       = "PADMARK_ARGON2_TIME"
	EnvArgon2Threads    = "PADMARK_ARGON2_THREADS"
	EnvDirectRedirect   = "PADMARK_DIRECT_REDIRECT"
	EnvMetricsAddr      = "PADMARK_METRICS_ADDR"
	EnvCSRFSecret       = "PADMARK_CSRF_SECRET" //nolint:gosec // G101: env var name, not an inline credential
	EnvContentKeyring   = "PADMARK_CONTENT_KEYRING_FILE"
)

// Default values for all flags.
const (
	DefaultAddr           = ":8080"
	DefaultStorage        = "sqlite"
	DefaultDSN            = "padmark.db"
	DefaultLogLevel       = "info"
	DefaultLogFormat      = "json"
	DefaultReadTimeout    = 30              // seconds
	DefaultWriteTimeout   = 60              // seconds
	DefaultMaxHeaderBytes = 64 * 1024       // 64 KB
	DefaultMaxBodyBytes   = 4 * 1024 * 1024 // 4 MB
	DefaultRateLimit      = 10              // requests per second per IP
	DefaultRateBurst      = 20              // max burst size per IP
	DefaultTOTPIssuer     = "padmark"
	DefaultArgon2Memory   = 24 * 1024 // argon2id memory in KiB (24 MiB)
	DefaultArgon2Time     = 2         // argon2id iterations (OWASP minimum at 64 MiB)
	DefaultArgon2Threads  = 1         // argon2id parallelism
)

// DefaultSessionTTL is the --session-ttl flag default, in seconds — derived from
// auth.DefaultSessionTTL (the single source of truth for the session lifetime default) instead
// of a separately maintained literal that could silently drift out of sync with it.
//
//nolint:gochecknoglobals // computed flag default, effectively constant
var DefaultSessionTTL = int(auth.DefaultSessionTTL.Seconds())

// appFlags returns the full flag set for the serve subcommand.
func appFlags() []cli.Flag { //nolint:funlen // declarative flag list
	return []cli.Flag{
		&cli.StringFlag{
			Name:    FlagContentKeyring,
			Sources: cli.EnvVars(EnvContentKeyring),
			Usage:   "Required JSON keyring file for content encryption; persist and back up separately from the database",
		},
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
			Usage: "Database DSN (file path for sqlite, connection string for postgres). " +
				"A postgres DSN carries the password and a command-line flag is visible to every local " +
				"user in the process listing (ps); set it via PADMARK_DSN instead",
		},
		&cli.StringFlag{
			Name:      FlagLogLevel,
			Validator: logging.ValidateLevel,
			Sources:   cli.EnvVars(EnvLogLevel),
			Value:     DefaultLogLevel,
			Usage:     "Log level: debug, info, warn, error",
		},
		&cli.StringFlag{
			Name:    FlagLogFormat,
			Sources: cli.EnvVars(EnvLogFormat),
			Value:   DefaultLogFormat,
			Usage:   "Log format: json, text",
		},
		&cli.BoolFlag{
			Name:    FlagEnableAccounts,
			Sources: cli.EnvVars(EnvEnableAccounts),
			Value:   false,
			Usage: "Enable the user-account system (TOTP login, /setup, /admin, private-note gating). " +
				"Off by default: the site is fully public unless this is set",
		},
		&cli.BoolFlag{
			Name:    FlagDisableAPI,
			Sources: cli.EnvVars(EnvDisableAPI),
			Value:   false,
			Usage: "Disable the REST/JSON API (/notes*, /links*, /api, /api/openapi.yaml, /llms.txt, " +
				"/api/tokens, /api/invites): every request reaching the disabling middleware gets 503 with " +
				"code=api_disabled. The web UI becomes read-only: viewing /{id} (incl. JSON/raw reads), " +
				"/r/{id}, login/admin pages and health checks keep working, but publishing or saving from " +
				"the editor fails with the API-disabled error (it calls POST /notes and PUT /notes/{id})",
		},
		&cli.BoolFlag{
			Name:    FlagCustomSlugs,
			Sources: cli.EnvVars(EnvCustomSlugs),
			Value:   false,
			Usage: "Allow user-supplied human-readable slugs at note creation (e.g. project/GUIDE.md) " +
				"and at short-link creation (e.g. launch-2026, single segment only). " +
				"Off by default because human-readable public URLs are easier to guess. " +
				"New content encryption keys are independent of the slug",
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
			Name:    FlagPublicScheme,
			Sources: cli.EnvVars(EnvPublicScheme),
			Usage: "Force the scheme (http or https) used when building absolute links back to this " +
				"server (note URLs, invite links, API-token envelopes), instead of auto-detecting it " +
				"from TLS/X-Forwarded-Proto; https also marks session/CSRF cookies Secure and enables " +
				"HSTS; set this when the reverse proxy's IP is not stable enough for --trusted-proxies " +
				"(e.g. cloud load balancers, k8s ingress)",
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
			Name:      FlagArgon2Memory,
			Validator: boundedCost(int(crypto.MaxArgon2Memory)),
			Sources:   cli.EnvVars(EnvArgon2Memory),
			Value:     DefaultArgon2Memory,
			Usage: "argon2id memory cost in KiB for account password hashing (default: 24576 = 24 MiB); " +
				"edit codes use a fast hash and are unaffected",
		},
		&cli.IntFlag{
			Name:      FlagArgon2Time,
			Validator: boundedCost(int(crypto.MaxArgon2Time)),
			Sources:   cli.EnvVars(EnvArgon2Time),
			Value:     DefaultArgon2Time,
			Usage:     "argon2id time cost (iterations); raise when lowering memory to keep strength",
		},
		&cli.IntFlag{
			Name:      FlagArgon2Threads,
			Validator: boundedCost(int(crypto.MaxArgon2Threads)),
			Sources:   cli.EnvVars(EnvArgon2Threads),
			Value:     DefaultArgon2Threads,
			Usage:     "argon2id parallelism (CPU threads per hash)",
		},
		&cli.BoolFlag{
			Name:    FlagDirectRedirect,
			Sources: cli.EnvVars(EnvDirectRedirect),
			Value:   true,
			Usage: "GET /r/{hash} behaviour for short links: true issues an immediate 301 to the " +
				"target URL; false renders an interstitial page (meta-refresh + noscript fallback) " +
				"showing the destination before redirecting",
		},
		&cli.StringFlag{
			Name:    FlagMetricsAddr,
			Sources: cli.EnvVars(EnvMetricsAddr),
			Usage: "Address for a separate internal Prometheus /metrics listener (e.g. :9090); " +
				"empty (default) disables it entirely. Bind this to a private interface only — " +
				"metrics are never served on the main, public-facing listener",
		},
		&cli.StringFlag{
			Name:    FlagCSRFSecret,
			Sources: cli.EnvVars(EnvCSRFSecret),
			Usage: "HMAC key for signing CSRF tokens (raw bytes, at least 32 characters). " +
				"Flag values are visible in process listings (ps); prefer PADMARK_CSRF_SECRET. " +
				"Empty (default) generates a fresh random secret per process: forms survive only " +
				"within one process lifetime, so restarts invalidate outstanding tokens and " +
				"multi-replica deployments 403 on every form. Set to a fixed random value " +
				"(e.g. `openssl rand -base64 32`) shared across replicas to avoid that",
		},
	}
}

// boundedCost rejects negative and overflowing flag values before narrowing to uint32/uint8.
func boundedCost(maximum int) func(int) error {
	return func(value int) error {
		if value < 1 || value > maximum {
			return fmt.Errorf("cost must be between 1 and %d", maximum)
		}

		return nil
	}
}
