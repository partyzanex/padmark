//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=auth -write_package_comment=false

package auth

import (
	"log/slog"
	"time"
)

// UserStore is the union of the user-store roles of the four auth managers. Only NewManager
// needs all of them, because it builds all four managers from one repository. Each manager
// keeps only its own narrow role: SessionUserStore, OnboardingUserStore, UserAdminStore and
// APITokenUserStore.
type UserStore interface {
	SessionUserStore
	OnboardingUserStore
	UserAdminStore
	APITokenUserStore
}

// PasswordHasher hashes and verifies user passwords (argon2id with configurable cost).
// Implemented by infra/crypto.PasswordHasher.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(storedHash, password string) bool
}

// SecretSealer seals a user's TOTP secret at rest under a key derived from their password.
// Implemented by infra/crypto.Sealer.
//
// Key derivation and encryption are one contract rather than two because the derivation
// parameters are recorded inside the sealed value: only the sealer can read them back, so a
// caller holding a bare "derive key, then decrypt" pair could not open a secret sealed under a
// cost different from the one currently configured.
//
// Open reports stale when the secret was sealed under an outdated cost or a legacy scheme. The
// value still opens; stale only asks the caller to re-Seal it and persist the result, which is
// how existing secrets migrate to the current parameters.
type SecretSealer interface {
	GenerateSalt() ([]byte, error)
	Seal(secret string, password, salt []byte) (string, error)
	Open(sealed string, password, salt []byte) (secret string, stale bool, err error)
}

// TOTPManager generates TOTP secrets, validates codes (returning the time-step counter used for
// replay protection), and renders enrollment material: a machine-readable otpauth URI and the
// browser-rendered QR code. Implemented by infra/crypto.TOTP.
type TOTPManager interface {
	GenerateSecret() (string, error)
	ValidateWithCounter(secret, code string) (valid bool, counter int64)
	GenerateQRCode(issuer, account, secret string) (string, error)
	TOTPURI(issuer, account, secret string) (string, error)
}

// Manager composes the four narrower auth managers — SessionManager (session.go),
// OnboardingManager (onboarding.go), UserAdminManager (user_admin.go), and APITokenManager
// (api_token.go) — into the single seam internal/adapters/web.AuthManager consumes. Each
// concern owns its own narrow dependency set and lives in its own file; Manager itself is pure
// composition (Go embedding promotes each sub-manager's methods, so Manager needs no methods
// of its own) so the four concerns stay independently readable and testable.
type Manager struct {
	*SessionManager
	*OnboardingManager
	*UserAdminManager
	*APITokenManager
}

// Stores groups the account repositories that Manager uses. APITokens enables the CLI API-token
// flow (issue/resolve/list/revoke). Set it to nil to disable that flow: the token methods then
// return domain.ErrFeatureNotSupported.
type Stores struct {
	Users     UserStore
	Invites   InviteStore
	Sessions  SessionStore
	APITokens APITokenStore
}

// Deps holds the primitives that the auth managers share. All fields are mandatory.
// PasswordHasher carries the configurable argon2 cost of password hashing and verification.
// Sealer and TOTP give the secret-sealing and TOTP primitives: the usecase owns their contracts
// and does not import infra/crypto. Now is the time source of sessions, invites and API tokens:
// the composition root gives time.Now, tests give a fixed clock.
type Deps struct {
	PasswordHasher PasswordHasher
	Sealer         SecretSealer
	TOTP           TOTPManager
	Log            *slog.Logger
	Now            func() time.Time
}

// NewManager returns a new auth Manager. issuer is the TOTP issuer name shown in the
// authenticator app. sessionTTL controls how long a session remains valid; pass 0 for the
// default (30 days).
//
// Returns an error when NewSessionManager's bootstrap-time crypto precompute fails (see its
// doc); NewManager runs only at the composition root, so the caller should treat this as fatal.
func NewManager(stores Stores, deps *Deps, issuer string, sessionTTL time.Duration) (*Manager, error) {
	sessionMgr, err := NewSessionManager(stores.Users, stores.Sessions, deps, sessionTTL)
	if err != nil {
		return nil, err
	}

	// SessionManager owns the login factors, so it is the step-up verifier for both issuers.
	return &Manager{
		SessionManager:    sessionMgr,
		OnboardingManager: NewOnboardingManager(stores.Users, stores.Invites, sessionMgr, deps, issuer),
		UserAdminManager:  NewUserAdminManager(stores.Users),
		APITokenManager:   NewAPITokenManager(stores.APITokens, stores.Users, sessionMgr, deps.Log, deps.Now),
	}, nil
}
