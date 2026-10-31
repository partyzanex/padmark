package server

import (
	"fmt"
	"math"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/logging"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// Flag names used in CLI flags.
const (
	FlagAddr                = "addr"
	FlagStorage             = "storage"
	FlagDSN                 = "dsn"
	FlagLogLevel            = "log-level"
	FlagLogFormat           = "log-format"
	FlagEnableAccounts      = "enable-accounts"
	FlagDisableAPI          = "disable-api"
	FlagReadTimeout         = "read-timeout"
	FlagWriteTimeout        = "write-timeout"
	FlagMaxHeaderBytes      = "max-header-bytes"
	FlagMaxBodyBytes        = "max-body-bytes"
	FlagRateLimit           = "rate-limit"
	FlagRateBurst           = "rate-burst"
	FlagTLSCert             = "tls-cert"
	FlagTLSKey              = "tls-key"
	FlagHTTPRedirectAddr    = "http-redirect-addr"
	FlagAllowedHosts        = "allowed-hosts"
	FlagTrustedProxies      = "trusted-proxies"
	FlagPublicScheme        = "public-scheme"
	FlagCustomSlugs         = "custom-slugs"
	FlagTOTPIssuer          = "totp-issuer"
	FlagSessionTTL          = "session-ttl"
	FlagArgon2Memory        = "argon2-memory"
	FlagArgon2Time          = "argon2-time"
	FlagArgon2Threads       = "argon2-threads"
	FlagArgon2MemoryBudget  = "argon2-memory-budget"
	FlagDirectRedirect      = "direct-redirect"
	FlagMetricsAddr         = "metrics-addr"
	FlagCSRFSecret          = "csrf-secret"
	FlagContentKeyring      = "content-keyring-file"
	FlagAllowSharedKeyring  = "allow-shared-keyring"
	FlagSkipMigrationBackup = "skip-migration-backup"
	// FlagAuthTokens is the removed v0.1.6 static bearer-token flag; it is still defined (hidden)
	// so that using it fails startup with a migration hint instead of being ignored.
	FlagAuthTokens = "auth-tokens" //nolint:gosec // flag name, not a credential
	// FlagCookieMaxAge is the deprecated v0.1.6 cookie lifetime flag, superseded by --session-ttl.
	FlagCookieMaxAge = "cookie-max-age"
)

// Env vars used to configure the service (prefix: PADMARK_*).
const (
	EnvAddr                = "PADMARK_ADDR"
	EnvStorage             = "PADMARK_STORAGE"
	EnvDSN                 = "PADMARK_DSN"
	EnvLogLevel            = "PADMARK_LOG_LEVEL"
	EnvLogFormat           = "PADMARK_LOG_FORMAT"
	EnvEnableAccounts      = "PADMARK_ENABLE_ACCOUNTS"
	EnvDisableAPI          = "PADMARK_DISABLE_API"
	EnvReadTimeout         = "PADMARK_READ_TIMEOUT"
	EnvWriteTimeout        = "PADMARK_WRITE_TIMEOUT"
	EnvMaxHeaderBytes      = "PADMARK_MAX_HEADER_BYTES"
	EnvMaxBodyBytes        = "PADMARK_MAX_BODY_BYTES"
	EnvRateLimit           = "PADMARK_RATE_LIMIT"
	EnvRateBurst           = "PADMARK_RATE_BURST"
	EnvTLSCert             = "PADMARK_TLS_CERT"
	EnvTLSKey              = "PADMARK_TLS_KEY"
	EnvHTTPRedirectAddr    = "PADMARK_HTTP_REDIRECT_ADDR"
	EnvAllowedHosts        = "PADMARK_ALLOWED_HOSTS"
	EnvTrustedProxies      = "PADMARK_TRUSTED_PROXIES"
	EnvPublicScheme        = "PADMARK_PUBLIC_SCHEME"
	EnvCustomSlugs         = "PADMARK_CUSTOM_SLUGS"
	EnvTOTPIssuer          = "PADMARK_TOTP_ISSUER"
	EnvSessionTTL          = "PADMARK_SESSION_TTL"
	EnvArgon2Memory        = "PADMARK_ARGON2_MEMORY"
	EnvArgon2Time          = "PADMARK_ARGON2_TIME"
	EnvArgon2Threads       = "PADMARK_ARGON2_THREADS"
	EnvArgon2MemoryBudget  = "PADMARK_ARGON2_MEMORY_BUDGET"
	EnvDirectRedirect      = "PADMARK_DIRECT_REDIRECT"
	EnvMetricsAddr         = "PADMARK_METRICS_ADDR"
	EnvCSRFSecret          = "PADMARK_CSRF_SECRET" //nolint:gosec // G101: env var name, not an inline credential
	EnvContentKeyring      = "PADMARK_CONTENT_KEYRING_FILE"
	EnvAllowSharedKeyring  = "PADMARK_ALLOW_SHARED_KEYRING"
	EnvSkipMigrationBackup = "PADMARK_SKIP_MIGRATION_BACKUP"
	EnvAuthTokens          = "PADMARK_AUTH_TOKENS" //nolint:gosec // env var name, not a credential
	EnvCookieMaxAge        = "PADMARK_COOKIE_MAX_AGE"
	// EnvBootstrapToken authorises creating the first admin via /setup without an invite. It is
	// read from the environment only — never a flag, so it cannot show up in process listings.
	EnvBootstrapToken = "PADMARK_BOOTSTRAP_TOKEN" //nolint:gosec // G101: env var name, not an inline credential
)

// Default values for all flags.
const (
	DefaultAddr               = ":8080"
	DefaultStorage            = "sqlite"
	DefaultDSN                = "padmark.db"
	DefaultLogLevel           = "info"
	DefaultLogFormat          = "json"
	DefaultReadTimeout        = 30              // seconds
	DefaultWriteTimeout       = 60              // seconds
	DefaultMaxHeaderBytes     = 64 * 1024       // 64 KB
	DefaultMaxBodyBytes       = 4 * 1024 * 1024 // 4 MB
	DefaultRateLimit          = 10              // requests per second per IP
	DefaultRateBurst          = 20              // max burst size per IP
	DefaultTOTPIssuer         = "padmark"
	DefaultArgon2Memory       = 24 * 1024  // argon2id memory in KiB (24 MiB)
	DefaultArgon2Time         = 2          // argon2id iterations (OWASP minimum at 64 MiB)
	DefaultArgon2Threads      = 1          // argon2id parallelism
	DefaultArgon2MemoryBudget = 128 * 1024 // KiB of argon2id workspace all concurrent derivations may hold (128 MiB)
	DefaultAllowSharedKeyring = false      // refuse group/other-readable keyrings outside /run/secrets/
	// DefaultDirectRedirect is off: an anonymous link must not be a free open redirector.
	DefaultDirectRedirect = false
)

// DefaultSessionTTL is the --session-ttl flag default, in seconds — derived from
// auth.DefaultSessionTTL (the single source of truth for the session lifetime default) instead
// of a separately maintained literal that could silently drift out of sync with it.
const DefaultSessionTTL = int(auth.DefaultSessionTTL / time.Second)

// appFlags returns the full flag set for the serve subcommand.
func appFlags() []cli.Flag { //nolint:funlen // declarative flag list
	return append([]cli.Flag{
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagContentKeyring
			flag.Sources = cli.EnvVars(EnvContentKeyring)
			flag.Usage = "JSON keyring file for content encryption; back up separately from the database. " +
				"Defaults to <sqlite db dir>/padmark-keys.json or ./padmark-keys.json for postgres; " +
				"serve creates a missing default file (container default: /data/padmark-keys.json)"
		}),
		allowSharedKeyringFlag(),
		skipMigrationBackupFlag(),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagAddr
			flag.Sources = cli.EnvVars(EnvAddr)
			flag.Value = DefaultAddr
			flag.Usage = "HTTP listen address"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagStorage
			flag.Sources = cli.EnvVars(EnvStorage)
			flag.Value = DefaultStorage
			flag.Usage = "Storage backend: sqlite, postgres"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagDSN
			flag.Sources = cli.EnvVars(EnvDSN)
			flag.Value = DefaultDSN
			flag.Usage = "Database DSN (file path for sqlite, connection string for postgres). " +
				"A postgres DSN carries the password and a command-line flag is visible to every local " +
				"user in the process listing (ps); set it via PADMARK_DSN instead"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagLogLevel
			flag.Validator = logging.ValidateLevel
			flag.Sources = cli.EnvVars(EnvLogLevel)
			flag.Value = DefaultLogLevel
			flag.Usage = "Log level: debug, info, warn, error"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagLogFormat
			flag.Sources = cli.EnvVars(EnvLogFormat)
			flag.Value = DefaultLogFormat
			flag.Usage = "Log format: json, text"
		}),
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagEnableAccounts
			flag.Sources = cli.EnvVars(EnvEnableAccounts)
			flag.Value = false
			flag.Usage = "Enable the user-account system (TOTP login, /setup, /admin, private-note gating). " +
				"Off by default: the site is fully public unless this is set"
		}),
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagDisableAPI
			flag.Sources = cli.EnvVars(EnvDisableAPI)
			flag.Value = false
			flag.Usage = "Disable the REST/JSON API (/notes*, /links*, /api, /api/openapi.yaml, /llms.txt, " +
				"/api/tokens, /api/invites): every request reaching the disabling middleware gets 503 with " +
				"code=api_disabled. The web UI becomes read-only: viewing /{id} (incl. JSON/raw reads), " +
				"/r/{id}, login/admin pages and health checks keep working, but publishing or saving from " +
				"the editor fails with the API-disabled error (it calls POST /notes and PUT /notes/{id})"
		}),
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagCustomSlugs
			flag.Sources = cli.EnvVars(EnvCustomSlugs)
			// On by default like v0.1.6, which always accepted a slug; set false to issue only
			// random slugs, because human-readable public URLs are easier to guess.
			flag.Value = true
			flag.Usage = "Allow user-supplied human-readable slugs at note creation (e.g. project/GUIDE.md) " +
				"and at short-link creation (e.g. launch-2026, single segment only). " +
				"On by default for v0.1.6 compatibility; set false to issue only random slugs, " +
				"because human-readable public URLs are easier to guess. " +
				"New content encryption keys are independent of the slug"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagReadTimeout
			flag.Sources = cli.EnvVars(EnvReadTimeout)
			flag.Value = DefaultReadTimeout
			flag.Usage = "HTTP read timeout in seconds"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagWriteTimeout
			flag.Sources = cli.EnvVars(EnvWriteTimeout)
			flag.Value = DefaultWriteTimeout
			flag.Usage = "HTTP write timeout in seconds"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagMaxHeaderBytes
			flag.Sources = cli.EnvVars(EnvMaxHeaderBytes)
			flag.Value = DefaultMaxHeaderBytes
			flag.Usage = "Maximum size of request headers in bytes"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagMaxBodyBytes
			flag.Sources = cli.EnvVars(EnvMaxBodyBytes)
			flag.Value = DefaultMaxBodyBytes
			flag.Usage = "Maximum size of request body in bytes"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagRateLimit
			flag.Sources = cli.EnvVars(EnvRateLimit)
			flag.Value = DefaultRateLimit
			flag.Usage = "Rate limit: requests per second per IP (0 = disabled)"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagRateBurst
			flag.Sources = cli.EnvVars(EnvRateBurst)
			flag.Value = DefaultRateBurst
			flag.Usage = "Rate limit: max burst size per IP"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagTLSCert
			flag.Sources = cli.EnvVars(EnvTLSCert)
			flag.Usage = "Path to TLS certificate file (PEM); enables HTTPS when set together with --tls-key"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagTLSKey
			flag.Sources = cli.EnvVars(EnvTLSKey)
			flag.Usage = "Path to TLS private key file (PEM); enables HTTPS when set together with --tls-cert"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagHTTPRedirectAddr
			flag.Sources = cli.EnvVars(EnvHTTPRedirectAddr)
			flag.Usage = "Address for the HTTP→HTTPS redirect listener (e.g. :80); only active when TLS is enabled"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagAllowedHosts
			flag.Sources = cli.EnvVars(EnvAllowedHosts)
			flag.Usage = "Comma-separated Host allowlist (e.g. example.com,www.example.com), compared case-insensitively " +
				"without the port; applies to the main listener (other Hosts get 421 Misdirected Request, " +
				"/healthz and /readyz exempt) and to the HTTP→HTTPS redirect listener (400); " +
				"empty = no Host check (legacy behaviour)"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagTrustedProxies
			flag.Sources = cli.EnvVars(EnvTrustedProxies)
			flag.Usage = "Comma-separated list of trusted proxy CIDRs or IPs (e.g. 10.0.0.0/8,127.0.0.1); " +
				"X-Forwarded-For and X-Real-IP are only trusted from these addresses"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagPublicScheme
			flag.Sources = cli.EnvVars(EnvPublicScheme)
			flag.Usage = "Force the scheme (http or https) used when building absolute links back to this " +
				"server (note URLs, invite links, API-token envelopes), instead of auto-detecting it " +
				"from TLS/X-Forwarded-Proto; https also marks session/CSRF cookies Secure and enables " +
				"HSTS; set this when the reverse proxy's IP is not stable enough for --trusted-proxies " +
				"(e.g. cloud load balancers, k8s ingress)"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagTOTPIssuer
			flag.Sources = cli.EnvVars(EnvTOTPIssuer)
			flag.Value = DefaultTOTPIssuer
			flag.Usage = "TOTP issuer name shown in the authenticator app (default: padmark)"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagAuthTokens
			flag.Sources = cli.EnvVars(EnvAuthTokens)
			flag.Hidden = true
			flag.Usage = "REMOVED: static bearer tokens were replaced by the account system; setting this " +
				"fails startup (use --enable-accounts, " + EnvBootstrapToken + " and /setup)"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagCookieMaxAge
			flag.Sources = cli.EnvVars(EnvCookieMaxAge)
			flag.Hidden = true
			flag.Usage = "DEPRECATED: use --session-ttl; honoured as the session TTL (seconds) when --session-ttl is unset"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagSessionTTL
			flag.Sources = cli.EnvVars(EnvSessionTTL)
			flag.Value = DefaultSessionTTL
			flag.Usage = "TOTP session TTL in seconds (default: 30 days)"
		}),
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = FlagDirectRedirect
			flag.Sources = cli.EnvVars(EnvDirectRedirect)
			flag.Value = DefaultDirectRedirect
			flag.Usage = "GET /r/{hash} behaviour for short links: true issues an immediate 301 to the " +
				"target URL for every link; false (default) renders an interstitial page (meta-refresh + " +
				"noscript fallback) showing the destination for links created anonymously, while links " +
				"created by a signed-in owner still redirect directly (open-redirect/phishing hardening)"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagMetricsAddr
			flag.Sources = cli.EnvVars(EnvMetricsAddr)
			flag.Usage = "Address for a separate internal Prometheus /metrics listener (e.g. :9090); " +
				"empty (default) disables it entirely. Bind this to a private interface only — " +
				"metrics are never served on the main, public-facing listener"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagCSRFSecret
			flag.Sources = cli.EnvVars(EnvCSRFSecret)
			flag.Usage = "HMAC key for signing CSRF tokens (raw bytes, at least 32 characters). " +
				"Flag values are visible in process listings (ps); prefer PADMARK_CSRF_SECRET. " +
				"Empty (default) generates a fresh random secret per process: forms survive only " +
				"within one process lifetime, so restarts invalidate outstanding tokens and " +
				"multi-replica deployments 403 on every form. Set to a fixed random value " +
				"(e.g. `openssl rand -base64 32`) shared across replicas to avoid that"
		}),
	}, argon2Flags()...)
}

// argon2Flags is shared by serve and the encryption commands: encryption migrate hashes the
// caller-chosen edit codes of converted legacy notes with the same argon2id cost as serve.
func argon2Flags() []cli.Flag {
	return []cli.Flag{
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagArgon2Memory
			flag.Validator = boundedCost(int(crypto.MaxArgon2Memory))
			flag.Sources = cli.EnvVars(EnvArgon2Memory)
			flag.Value = DefaultArgon2Memory
			flag.Usage = "argon2id memory cost in KiB for account passwords, caller-chosen edit codes and the " +
				"per-user TOTP key (default: 24576 = 24 MiB); every derivation allocates this much, bounded " +
				"overall by --argon2-memory-budget. Server-generated edit codes use a fast hash instead"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagArgon2Time
			flag.Validator = boundedCost(int(crypto.MaxArgon2Time))
			flag.Sources = cli.EnvVars(EnvArgon2Time)
			flag.Value = DefaultArgon2Time
			flag.Usage = "argon2id time cost (iterations); raise when lowering memory to keep strength"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagArgon2Threads
			flag.Validator = boundedCost(int(crypto.MaxArgon2Threads))
			flag.Sources = cli.EnvVars(EnvArgon2Threads)
			flag.Value = DefaultArgon2Threads
			flag.Usage = "argon2id parallelism (CPU threads per hash)"
		}),
		clifactory.IntFlag(func(flag *cli.IntFlag) {
			flag.Name = FlagArgon2MemoryBudget
			flag.Validator = boundedCost(maxArgon2MemoryBudget)
			flag.Sources = cli.EnvVars(EnvArgon2MemoryBudget)
			flag.Value = DefaultArgon2MemoryBudget
			flag.Usage = "total argon2id memory in KiB that concurrent password, edit-code and TOTP-key " +
				"derivations may hold at once (default: 131072 = 128 MiB); further requests queue. " +
				"Must be at least --argon2-memory. Size it below the container memory limit and set " +
				"GOMEMLIMIT to about 75-80% of that limit"
		}),
	}
}

// skipMigrationBackupFlag is shared by serve and migrate, which both apply migrations.
func skipMigrationBackupFlag() *cli.BoolFlag {
	return clifactory.BoolFlag(func(flag *cli.BoolFlag) {
		flag.Name = FlagSkipMigrationBackup
		flag.Sources = cli.EnvVars(EnvSkipMigrationBackup)
		flag.Value = false
		flag.Usage = "Do not back up the SQLite database before applying pending migrations. By default, when " +
			"migrations are pending and the database is a file, a copy is written next to it as " +
			"<db>.bak-<YYYYMMDD-HHMMSS> (mode 0600) first, because some migrations are irreversible. " +
			"Set only if backups are managed elsewhere; ignored for postgres (use pg_dump)"
	})
}

// allowSharedKeyringFlag is shared by serve and the encryption commands, which load the same keyring.
func allowSharedKeyringFlag() *cli.BoolFlag {
	return clifactory.BoolFlag(func(flag *cli.BoolFlag) {
		flag.Name = FlagAllowSharedKeyring
		flag.Sources = cli.EnvVars(EnvAllowSharedKeyring)
		flag.Value = DefaultAllowSharedKeyring
		flag.Usage = "Accept a content keyring file readable by group or other users (logged as a warning). " +
			"Startup otherwise refuses such a file; restrict it to 0600 or 0400 instead whenever possible. " +
			"Only for secret managers that cannot provide an owner-only file; keyrings under /run/secrets/ " +
			"(Docker/Compose secrets) are already exempt"
	})
}

// maxArgon2MemoryBudget caps --argon2-memory-budget at 64 GiB (in KiB): it rejects typos and keeps
// the value inside uint32 on every platform's int.
const maxArgon2MemoryBudget = 64 * 1024 * 1024

// boundedCost rejects negative and overflowing flag values before narrowing to uint32/uint8.
func boundedCost(maximum int) func(int) error {
	return func(value int) error {
		if value < 1 || value > maximum {
			return fmt.Errorf("%w 1 and %d", errInvalidArgon2Cost, maximum)
		}

		return nil
	}
}

// uint32Flag narrows an integer flag to uint32. The flag Validator already bounds the value;
// the repeated range check keeps the conversion provably safe where it happens.
func uint32Flag(cmd *cli.Command, name string) (uint32, error) {
	value := cmd.Int(name)
	if value < 0 || value > math.MaxUint32 {
		return 0, fmt.Errorf("--%s=%d: %w", name, value, errFlagOutOfRange)
	}

	return uint32(value), nil
}

// uint8Flag narrows an integer flag to uint8, with the same range check as uint32Flag.
func uint8Flag(cmd *cli.Command, name string) (uint8, error) {
	value := cmd.Int(name)
	if value < 0 || value > math.MaxUint8 {
		return 0, fmt.Errorf("--%s=%d: %w", name, value, errFlagOutOfRange)
	}

	return uint8(value), nil
}
