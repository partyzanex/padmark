package domain

import "time"

// APIToken is a long-lived bearer key issued through the browser-based CLI login flow.
// The plain key is shown to the user exactly once; only its SHA-256 hash is persisted.
// TokenHash is both the primary key and the public identifier used in admin URLs, since the
// CLI flow does not issue a separate opaque ID (see docs/plan-padmark-cli-improvements.md,
// option b). It is looked up on every authenticated API request.
type APIToken struct {
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	UserID     string
	TokenHash  string
}
