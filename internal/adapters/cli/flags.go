package cli

import "time"

// CLI flag names.
const (
	FlagURL      = "url"
	FlagToken    = "token"
	FlagTimeout  = "timeout"
	FlagTitle    = "title"
	FlagContent  = "content"
	FlagFile     = "file"
	FlagSlug     = "slug"
	FlagPlain    = "plain"
	FlagPrivacy  = "privacy"
	FlagBurn     = "burn"
	FlagTTL      = "ttl"
	FlagEditCode = "edit-code"
	FlagRaw      = "raw"
	FlagJSON     = "json"
	// FlagTargetURL is the short link's destination. Named "target-url", not "url" — the global
	// --url flag already means the padmark server address; reusing it for the link destination
	// would collide (see the CLI section in TODO.md for this deviation from the original draft).
	FlagTargetURL = "target-url"
	// FlagDirectRedirect is a per-link override of the server's --direct-redirect default. A
	// plain BoolFlag: cmd.IsSet distinguishes "not passed" (inherit the server default, omit the
	// field entirely) from an explicit --direct-redirect=false (force the interstitial).
	FlagDirectRedirect = "direct-redirect"
	// FlagUsername (and the account-provisioning flags below) name setup-command inputs.
	FlagUsername = "username"
	// FlagPassword is the account password for `setup`. Deliberately no echo-hiding terminal
	// machinery (that would pull in a TTY dependency): pass the flag, or pipe the password to
	// stdin — a TTY stdin without the flag is an error, not a visible-echo prompt.
	FlagPassword = "password"
	FlagInvite   = "invite"
)

// Environment variable names.
const (
	EnvURL      = "PADMARK_URL"
	EnvToken    = "PADMARK_TOKEN"
	EnvTimeout  = "PADMARK_TIMEOUT"
	EnvEditCode = "PADMARK_EDIT_CODE"
)

// DefaultURL is the default padmark server address.
const DefaultURL = "http://localhost:8080"

// DefaultTimeout bounds each request so commands fail fast instead of hanging on a slow or
// unresponsive server. Override with --timeout (0 disables the bound).
const DefaultTimeout = 30 * time.Second
