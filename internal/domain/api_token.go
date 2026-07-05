package domain

import "time"

// APIToken is a long-lived bearer key issued through the browser-based CLI login flow.
// The plain key is shown to the user exactly once; only its SHA-256 hash is persisted.
// ID identifies the token in the admin UI and revoke URLs so the raw hash never appears
// in a URL or a log line. TokenHash is looked up on every authenticated API request.
type APIToken struct {
	ID         string
	UserID     string
	TokenHash  string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}
