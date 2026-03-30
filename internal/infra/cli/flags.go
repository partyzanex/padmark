package cli

// CLI flag names.
const (
	FlagURL      = "url"
	FlagToken    = "token"
	FlagTitle    = "title"
	FlagContent  = "content"
	FlagFile     = "file"
	FlagSlug     = "slug"
	FlagPlain    = "plain"
	FlagBurn     = "burn"
	FlagTTL      = "ttl"
	FlagEditCode = "edit-code"
	FlagRaw      = "raw"
	FlagJSON     = "json"
)

// Environment variable names.
const (
	EnvURL      = "PADMARK_URL"
	EnvToken    = "PADMARK_TOKEN"
	EnvEditCode = "PADMARK_EDIT_CODE"
)

// DefaultURL is the default padmark server address.
const DefaultURL = "http://localhost:8080"
