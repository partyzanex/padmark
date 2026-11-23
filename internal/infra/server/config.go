package server

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
)

const (
	// minCSRFSecretLen is the minimum accepted length for --csrf-secret; it matches the
	// 32-byte secret NewRouter generates when the flag is empty.
	minCSRFSecretLen = 32
	// minBootstrapTokenLen is the minimum accepted length for PADMARK_BOOTSTRAP_TOKEN: the token
	// alone decides who becomes the single admin, so it must not be guessable.
	minBootstrapTokenLen = 32
)

// errRemovedAuthTokens is returned when the removed --auth-tokens / PADMARK_AUTH_TOKENS is set.
var errRemovedAuthTokens = fmt.Errorf(
	"--%s / %s was removed in this version: static bearer tokens are replaced by accounts; "+
		"use --%s with %s and create the first admin via /setup or `padmark-cli setup` "+
		"(API tokens are issued via /api/tokens); unset the old variable to start",
	FlagAuthTokens, EnvAuthTokens, FlagEnableAccounts, EnvBootstrapToken,
)

// Config is the checked configuration of the serve command. configFromCommand reads each flag
// and environment variable one time and does all checks, before the keyring and the database
// open. The build functions read only this value. Config holds secrets (the DSN, the CSRF
// secret, the bootstrap token and the S3 keys), so do not log it.
//
// The field order is the order that govet fieldalignment requires, not an order of topics.
type Config struct {
	log      logConfig
	storage  storageConfig
	keyring  keyringConfig
	accounts accountsConfig
	http     httpConfig
	router   routerConfig
	files    filesConfig
	// maxContentLength is the note content limit in characters; see maxContentLength.
	maxContentLength int
	shutdownTimeout  time.Duration
	argon2           argon2Config
	// customSlugs permits caller-chosen slugs for notes, links and files.
	customSlugs bool
}

// storageConfig selects the database. serve, migrate and the encryption commands use it.
type storageConfig struct {
	dialect             string
	dsn                 string
	skipMigrationBackup bool
}

// migration returns the options of the SQLite backup before the migrations.
func (c storageConfig) migration() migrationOptions {
	return migrationOptions{dsn: c.dsn, skipBackup: c.skipMigrationBackup}
}

// keyringConfig locates the content keyring. serve and the encryption commands use it.
type keyringConfig struct {
	path        string
	allowShared bool
}

type logConfig struct {
	level  string
	format string
}

// httpConfig holds the listener settings. The timeouts are in seconds, as the flags give them.
type httpConfig struct {
	// redirectHosts is the Host allow-list of the redirect listener; nil turns the check off.
	redirectHosts map[string]struct{}
	addr          string
	tlsCert       string
	tlsKey        string
	redirectAddr  string
	metricsAddr   string
	// allowedHosts is the Host allow-list of the main listener.
	allowedHosts   []string
	readTimeout    int
	writeTimeout   int
	maxHeaderBytes int
}

// routerConfig holds the router settings that do not belong to the account system.
type routerConfig struct {
	publicScheme   string
	trustedProxies []*net.IPNet
	// csrfSecret is nil when the flag is empty: NewRouter then makes a secret for this process.
	csrfSecret     []byte
	maxBodyBytes   int
	rateLimit      int
	rateBurst      int
	disableAPI     bool
	directRedirect bool
}

// accountsConfig holds the account system settings. The router also uses sessionTTL and
// bootstrapToken, so that the session cookie and the server-side session have one lifetime.
type accountsConfig struct {
	totpIssuer string
	// bootstrapToken is empty or at least minBootstrapTokenLen characters; never log it.
	bootstrapToken string
	sessionTTL     time.Duration
	enabled        bool
	// cookieMaxAgeSet and sessionTTLSet record which session flags the operator set, for the
	// startup warning about the deprecated --cookie-max-age.
	cookieMaxAgeSet bool
	sessionTTLSet   bool
}

// argon2Config is the checked argon2id cost and the memory budget of all derivations.
type argon2Config struct {
	params       crypto.Argon2Params
	memoryBudget uint32
}

// filesConfig holds the file settings. An empty s3.Endpoint turns files off.
type filesConfig struct {
	s3 s3store.Config
	// orphans names the other S3 settings that are set while the endpoint is empty. The values
	// stay out of it, because the startup log shows it.
	orphans       []string
	transfer      web.FileTransferLimits
	maxFileSize   int64
	sweepInterval time.Duration
	allowInsecure bool
}

// configFromCommand reads the serve flags and environment variables and checks them. The checks
// run in the order of the earlier startup sequence, so a command line with two errors gives the
// same error as before. Each error stops startup (fail-hard).
func configFromCommand(cmd *cli.Command) (Config, error) {
	var zero Config

	// Fail closed: v0.1.6 gated writes behind these tokens, so ignoring them would bring the
	// server up with anonymous writes. Only whether the value is set is checked; it is never
	// echoed.
	if cmd.String(FlagAuthTokens) != "" {
		return zero, errRemovedAuthTokens
	}

	argon2, err := argon2ConfigFromCommand(cmd)
	if err != nil {
		return zero, err
	}

	accounts, err := accountsConfigFromCommand(cmd)
	if err != nil {
		return zero, err
	}

	files, err := filesConfigFromCommand(cmd)
	if err != nil {
		return zero, err
	}

	router, err := routerConfigFromCommand(cmd)
	if err != nil {
		return zero, err
	}

	listen, err := httpConfigFromCommand(cmd)
	if err != nil {
		return zero, err
	}

	return Config{
		files: files, router: router, http: listen, accounts: accounts,
		storage: storageConfigFromCommand(cmd),
		log:     logConfig{level: cmd.String(FlagLogLevel), format: cmd.String(FlagLogFormat)},
		keyring: keyringConfigFromCommand(cmd), argon2: argon2,
		maxContentLength: maxContentLength(cmd.Int(FlagMaxContentLength), router.maxBodyBytes),
		shutdownTimeout:  cmd.Duration(FlagShutdownTimeout),
		customSlugs:      cmd.Bool(FlagCustomSlugs),
	}, nil
}

// storageConfigFromCommand reads the database flags.
func storageConfigFromCommand(cmd *cli.Command) storageConfig {
	return storageConfig{
		dialect:             cmd.String(FlagStorage),
		dsn:                 cmd.String(FlagDSN),
		skipMigrationBackup: cmd.Bool(FlagSkipMigrationBackup),
	}
}

// keyringConfigFromCommand reads the keyring flags.
func keyringConfigFromCommand(cmd *cli.Command) keyringConfig {
	return keyringConfig{path: cmd.String(FlagContentKeyring), allowShared: cmd.Bool(FlagAllowSharedKeyring)}
}

// argon2ConfigFromCommand reads the argon2id cost and the memory budget and checks the cost
// bounds and that the budget holds at least one derivation (fail-hard). Zero values cannot get
// here: the flag validators accept only positive values.
func argon2ConfigFromCommand(cmd *cli.Command) (argon2Config, error) {
	var zero argon2Config

	memory, memoryErr := uint32Flag(cmd, FlagArgon2Memory)
	timeCost, timeErr := uint32Flag(cmd, FlagArgon2Time)
	threads, threadsErr := uint8Flag(cmd, FlagArgon2Threads)

	flagErr := errors.Join(memoryErr, timeErr, threadsErr)
	if flagErr != nil {
		return zero, fmt.Errorf("argon2 params: read argon2 flags: %w", flagErr)
	}

	params := crypto.Argon2Params{Memory: memory, Time: timeCost, Threads: threads}

	costErr := params.Validate()
	if costErr != nil {
		return zero, fmt.Errorf("validate argon2 configuration: %w", costErr)
	}

	budget, err := uint32Flag(cmd, FlagArgon2MemoryBudget)
	if err != nil {
		return zero, fmt.Errorf("argon2 memory budget: %w", err)
	}

	budgetErr := validateArgon2MemoryBudget(budget, memory)
	if budgetErr != nil {
		return zero, fmt.Errorf("validate argon2 configuration: %w", budgetErr)
	}

	return argon2Config{params: params, memoryBudget: budget}, nil
}

// validateArgon2MemoryBudget checks that the concurrency budget can hold at least one
// derivation at the configured password cost.
func validateArgon2MemoryBudget(budget, memory uint32) error {
	if budget < memory {
		return fmt.Errorf("%w: --%s (%d KiB) must be at least --%s (%d "+
			"KiB)", errInvalidArgon2Budget, FlagArgon2MemoryBudget, budget, FlagArgon2Memory, memory)
	}

	return nil
}

// accountsConfigFromCommand reads the account flags and the bootstrap token.
func accountsConfigFromCommand(cmd *cli.Command) (accountsConfig, error) {
	var zero accountsConfig

	bootstrapToken, err := parseBootstrapToken(os.Getenv(EnvBootstrapToken))
	if err != nil {
		return zero, fmt.Errorf("validate bootstrap token: %w", err)
	}

	// The session lifetime is --session-ttl, or the deprecated v0.1.6 --cookie-max-age when only
	// that one is set, so an upgrade does not silently change the lifetime operators configured.
	// No extra bounds are applied: --session-ttl itself has none, and auth.NewManager rejects an
	// unusable TTL. warnDeprecatedSessionFlags reports the fallback once at startup.
	cookieMaxAgeSet, sessionTTLSet := cmd.IsSet(FlagCookieMaxAge), cmd.IsSet(FlagSessionTTL)

	seconds := cmd.Int(FlagSessionTTL)
	if cookieMaxAgeSet && !sessionTTLSet {
		seconds = cmd.Int(FlagCookieMaxAge)
	}

	return accountsConfig{
		totpIssuer:      cmd.String(FlagTOTPIssuer),
		bootstrapToken:  bootstrapToken,
		sessionTTL:      time.Duration(seconds) * time.Second,
		enabled:         cmd.Bool(FlagEnableAccounts),
		cookieMaxAgeSet: cookieMaxAgeSet,
		sessionTTLSet:   sessionTTLSet,
	}, nil
}

// parseBootstrapToken validates the PADMARK_BOOTSTRAP_TOKEN value. Empty is valid and keeps
// first-admin setup closed (fail-closed: without proof of server ownership the first visitor of
// /setup would become the permanent admin). A configured token shorter than the minimum fails
// startup rather than leaving the bootstrap guessable. The value never appears in errors or logs.
func parseBootstrapToken(raw string) (string, error) {
	if raw != "" && len(raw) < minBootstrapTokenLen {
		return "", fmt.Errorf("%w: %s must be at least %d characters, got %d",
			errInvalidBootstrapToken, EnvBootstrapToken, minBootstrapTokenLen, len(raw))
	}

	return raw, nil
}

// filesConfigFromCommand reads the file flags and the S3 keys. The keys come only from the
// environment, so that ps does not show them. A set endpoint needs the bucket and both keys.
func filesConfigFromCommand(cmd *cli.Command) (filesConfig, error) {
	var zero filesConfig

	cfg := filesConfig{
		s3: s3store.Config{
			Endpoint:  cmd.String(FlagS3Endpoint),
			Bucket:    cmd.String(FlagS3Bucket),
			Region:    cmd.String(FlagS3Region),
			AccessKey: os.Getenv(EnvS3AccessKey),
			SecretKey: os.Getenv(EnvS3SecretKey),
			PathStyle: cmd.Bool(FlagS3PathStyle),
		},
		orphans:       nil,
		transfer:      fileTransferLimitsFromFlags(cmd),
		maxFileSize:   cmd.Int64(FlagMaxFileSize),
		sweepInterval: cmd.Duration(FlagFilesSweepInterval),
		allowInsecure: cmd.Bool(FlagS3AllowInsecure),
	}

	if cfg.s3.Endpoint == "" {
		cfg.orphans = orphanS3Settings(cmd)

		return cfg, nil
	}

	if cfg.s3.Bucket == "" || cfg.s3.AccessKey == "" || cfg.s3.SecretKey == "" {
		return zero, fmt.Errorf("%w: --%s, %s and %s are required with --%s",
			errS3Config, FlagS3Bucket, EnvS3AccessKey, EnvS3SecretKey, FlagS3Endpoint)
	}

	return cfg, nil
}

// orphanS3Settings names the S3 settings that are set without --s3-endpoint. They usually mean
// a misspelled endpoint variable.
func orphanS3Settings(cmd *cli.Command) []string {
	var orphans []string

	for _, name := range []string{FlagS3Bucket, FlagS3Region, FlagS3PathStyle, FlagS3AllowInsecure} {
		if cmd.IsSet(name) {
			orphans = append(orphans, "--"+name)
		}
	}

	for _, name := range []string{EnvS3AccessKey, EnvS3SecretKey} {
		if os.Getenv(name) != "" {
			orphans = append(orphans, name)
		}
	}

	return orphans
}

// routerConfigFromCommand reads the router flags and checks the trusted proxies, the public
// scheme and the CSRF secret.
func routerConfigFromCommand(cmd *cli.Command) (routerConfig, error) {
	var zero routerConfig

	trustedProxies, err := parseTrustedProxies(cmd.String(FlagTrustedProxies))
	if err != nil {
		return zero, err
	}

	publicScheme, err := parsePublicScheme(cmd.String(FlagPublicScheme))
	if err != nil {
		return zero, err
	}

	csrfSecret, err := parseCSRFSecret(cmd.String(FlagCSRFSecret))
	if err != nil {
		return zero, err
	}

	return routerConfig{
		publicScheme:   publicScheme,
		trustedProxies: trustedProxies,
		csrfSecret:     csrfSecret,
		maxBodyBytes:   cmd.Int(FlagMaxBodyBytes),
		rateLimit:      cmd.Int(FlagRateLimit),
		rateBurst:      cmd.Int(FlagRateBurst),
		disableAPI:     cmd.Bool(FlagDisableAPI),
		directRedirect: cmd.Bool(FlagDirectRedirect),
	}, nil
}

// parseCSRFSecret converts the --csrf-secret flag value into an HMAC key. An empty value is
// valid and yields nil, meaning "generate a fresh random secret per process" (the legacy
// behaviour): single-instance deployments rely on it to avoid secret management, and the
// restart-invalidates-tokens tradeoff is acceptable there. A configured secret must be long
// enough to serve as an HMAC-SHA256 key; a short one would make the CSRF token forgeable in
// principle, so this fails at startup rather than weakening protection silently.
func parseCSRFSecret(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}

	if len(raw) < minCSRFSecretLen {
		return nil, fmt.Errorf("%w: csrf secret must be at least %d "+
			"characters, got %d", errInvalidCSRFSecret, minCSRFSecretLen, len(raw))
	}

	return []byte(raw), nil
}

// httpConfigFromCommand reads the listener flags. The TLS certificate and key come as a pair.
func httpConfigFromCommand(cmd *cli.Command) (httpConfig, error) {
	var zero httpConfig

	tlsCert, tlsKey := cmd.String(FlagTLSCert), cmd.String(FlagTLSKey)
	if (tlsCert == "") != (tlsKey == "") {
		return zero, errIncompleteTLSConfig
	}

	// The same allow-list guards the redirect listener and the main one.
	allowedHosts := cmd.String(FlagAllowedHosts)

	return httpConfig{
		redirectHosts:  allowedHostSet(allowedHosts),
		addr:           cmd.String(FlagAddr),
		tlsCert:        tlsCert,
		tlsKey:         tlsKey,
		redirectAddr:   cmd.String(FlagHTTPRedirectAddr),
		metricsAddr:    cmd.String(FlagMetricsAddr),
		allowedHosts:   parseTokens(allowedHosts),
		readTimeout:    cmd.Int(FlagReadTimeout),
		writeTimeout:   cmd.Int(FlagWriteTimeout),
		maxHeaderBytes: cmd.Int(FlagMaxHeaderBytes),
	}, nil
}

// fileTransferLimitsFromFlags reads the file transfer limits. The flag validators already
// rejected negative values; the handler replaces a 0 with its default.
func fileTransferLimitsFromFlags(cmd *cli.Command) web.FileTransferLimits {
	return web.FileTransferLimits{
		IdleTimeout:         cmd.Duration(FlagFileIdleTimeout),
		MaxUploads:          cmd.Int(FlagMaxUploads),
		MaxUploadsPerClient: cmd.Int(FlagMaxUploadsPerClient),
	}
}

// maxContentLength gives the note content limit: --max-content-length (limit), or
// --max-body-bytes when limit is 0. A note cannot be larger than the body that holds it, so one
// setting is enough.
func maxContentLength(limit, maxBodyBytes int) int {
	if limit == 0 {
		return maxBodyBytes
	}

	return limit
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
