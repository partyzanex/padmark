//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=auth

package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// UserStore persists registered users.
type UserStore interface {
	Create(ctx context.Context, u *domain.User) error
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	List(ctx context.Context) ([]*domain.User, error)
	UpdateLastLogin(ctx context.Context, id uuid.UUID, t time.Time) error
	UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string, kdfSalt []byte, totpSecret string) error
	// UpdateTOTPCounter atomically advances the user's last accepted TOTP counter,
	// returning false when counter is not strictly greater (replay). The conditional
	// update is the cross-instance, restart-safe guard against TOTP code reuse.
	UpdateTOTPCounter(ctx context.Context, id uuid.UUID, counter int64) (bool, error)
	Revoke(ctx context.Context, id uuid.UUID) error
}

// InviteStore persists single-use invite links.
type InviteStore interface {
	Issue(ctx context.Context, createdByID uuid.UUID) (string, error)
	// RedeemInvite atomically consumes the invite and inserts usr in one transaction,
	// so a failed user creation (e.g. a username race) never burns the token.
	RedeemInvite(ctx context.Context, token, username string, usr *domain.User) error
}

// SessionStore persists authenticated browser sessions.
type SessionStore interface {
	Create(ctx context.Context, s *domain.Session) error
	Get(ctx context.Context, sessionID string) (*domain.Session, error)
	Delete(ctx context.Context, sessionID string) error
	// DeleteByUserID removes all sessions for a given user (e.g. after password change).
	DeleteByUserID(ctx context.Context, userID uuid.UUID) error
	// DeleteByUserIDExcept removes all sessions for the user except the one with the given session ID.
	DeleteByUserIDExcept(ctx context.Context, userID uuid.UUID, exceptSessionID string) error
}

// Encryptor encrypts and decrypts TOTP secrets at rest.
type Encryptor interface {
	Encrypt(plaintext, key string) (string, error)
	Decrypt(ciphertext, key string) (string, error)
}

// PasswordHasher hashes and verifies user passwords (argon2id with configurable cost).
// Implemented by infra/crypto.PasswordHasher.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(storedHash, password string) bool
}

// KeyDeriver generates a KDF salt and derives the AES key used to encrypt a user's TOTP secret.
// Implemented by infra/crypto.KDF.
type KeyDeriver interface {
	GenerateSalt() ([]byte, error)
	DeriveKey(password, salt []byte) (string, error)
}

// TOTPManager generates TOTP secrets, validates codes (returning the time-step counter used for
// replay protection), and renders the enrollment QR code. Implemented by infra/crypto.TOTP.
type TOTPManager interface {
	GenerateSecret() (string, error)
	ValidateWithCounter(secret, code string) (valid bool, counter int64)
	GenerateQRCode(issuer, account, secret string) (string, error)
}

// APITokenStore persists CLI API tokens keyed by their SHA-256 hash. The plain key is never stored.
type APITokenStore interface {
	// CreateIfUnderLimit atomically counts t.UserID's existing tokens and inserts t only if that
	// count is below limit, reporting false (no error) when the limit is already reached. The
	// count-then-insert must be a single atomic operation — two concurrent issuance requests each
	// doing a separate count check followed by an unconditional insert could both pass the check
	// before either insert lands, letting the user exceed the limit.
	CreateIfUnderLimit(ctx context.Context, t *domain.APIToken, limit int) (bool, error)
	// GetByHash resolves a token by its SHA-256 hash. Returns domain.ErrNotFound when absent.
	GetByHash(ctx context.Context, tokenHash string) (*domain.APIToken, error)
	// List returns all tokens for the admin panel.
	List(ctx context.Context) ([]*domain.APIToken, error)
	// RevokeByHash deletes a token by its SHA-256 hash (the public identifier). Returns
	// domain.ErrNotFound when absent.
	RevokeByHash(ctx context.Context, tokenHash string) error
	// UpdateLastUsed records the time of the most recent successful token check, keyed by hash.
	UpdateLastUsed(ctx context.Context, tokenHash string, t time.Time) error
}

// Manager composes the four narrower auth managers — SessionManager (session.go),
// OnboardingManager (onboarding.go), UserAdminManager (user_admin.go), and APITokenManager
// (api_token.go) — into the single seam internal/adapters/http.AuthManager consumes. Each
// concern owns its own narrow dependency set and lives in its own file; Manager itself is pure
// composition (Go embedding promotes each sub-manager's methods, so Manager needs no methods
// of its own) so the four concerns stay independently readable and testable.
type Manager struct {
	*SessionManager
	*OnboardingManager
	*UserAdminManager
	*APITokenManager
}

// NewManager returns a new auth Manager.
// passwordHasher carries the (configurable) argon2 cost used for password hashing/verification.
// keyDeriver and totp provide the KDF and TOTP primitives (injected so the usecase owns its
// contracts instead of importing infra/crypto). issuer is the TOTP issuer name shown in the
// authenticator app. sessionTTL controls how long a session remains valid; pass 0 for the
// default (30 days).
// apiTokens enables the CLI API-token flow (issue/resolve/list/revoke). Pass nil to disable it:
// the token methods then return domain.ErrFeatureNotSupported.
//
// Returns an error when NewSessionManager's bootstrap-time crypto precompute fails (see its
// doc); NewManager runs only at the composition root, so the caller should treat this as fatal.
func NewManager(
	users UserStore,
	invites InviteStore,
	sessions SessionStore,
	apiTokens APITokenStore,
	enc Encryptor,
	passwordHasher PasswordHasher,
	keyDeriver KeyDeriver,
	totp TOTPManager,
	log *slog.Logger,
	issuer string,
	sessionTTL time.Duration,
) (*Manager, error) {
	sessionMgr, err := NewSessionManager(
		users, sessions, passwordHasher, keyDeriver, enc, totp, log, sessionTTL,
	)
	if err != nil {
		return nil, fmt.Errorf("new session manager: %w", err)
	}

	return &Manager{
		SessionManager: sessionMgr,
		OnboardingManager: NewOnboardingManager(
			users, invites, passwordHasher, keyDeriver, enc, totp, issuer,
		),
		UserAdminManager: NewUserAdminManager(users),
		APITokenManager:  NewAPITokenManager(apiTokens, users, log),
	}, nil
}
