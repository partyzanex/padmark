//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=auth

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	sessionIDBytes = 32
	defaultTTL     = 30 * 24 * time.Hour
)

// UserStore persists registered users.
type UserStore interface {
	Create(ctx context.Context, u *domain.User) error
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	GetByID(ctx context.Context, id string) (*domain.User, error)
	List(ctx context.Context) ([]*domain.User, error)
	UpdateLastLogin(ctx context.Context, id string, t time.Time) error
	UpdatePassword(ctx context.Context, id, passwordHash string, kdfSalt []byte, totpSecret string) error
	// UpdateTOTPCounter atomically advances the user's last accepted TOTP counter,
	// returning false when counter is not strictly greater (replay). The conditional
	// update is the cross-instance, restart-safe guard against TOTP code reuse.
	UpdateTOTPCounter(ctx context.Context, id string, counter int64) (bool, error)
	Revoke(ctx context.Context, id string) error
}

// InviteStore persists single-use invite links.
type InviteStore interface {
	Issue(ctx context.Context, createdByID string) (string, error)
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
	DeleteByUserID(ctx context.Context, userID string) error
	// DeleteByUserIDExcept removes all sessions for the user except the one with the given session ID.
	DeleteByUserIDExcept(ctx context.Context, userID, exceptSessionID string) error
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
	// Create persists a newly issued API token.
	Create(ctx context.Context, t *domain.APIToken) error
	// CountByUser returns how many tokens the given user already holds, so issuance can be capped.
	CountByUser(ctx context.Context, userID string) (int, error)
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

// Manager orchestrates TOTP-based authentication and invite-link onboarding.
type Manager struct {
	totp        TOTPManager
	invites     InviteStore
	sessions    SessionStore
	enc         Encryptor
	pw          PasswordHasher
	kdf         KeyDeriver
	users       UserStore
	apiTokens   APITokenStore
	log         *slog.Logger
	issuer      string
	dummyPwHash string
	ttl         time.Duration
}

// NewManager returns a new auth Manager.
// passwordHasher carries the (configurable) argon2 cost used for password hashing/verification.
// keyDeriver and totp provide the KDF and TOTP primitives (injected so the usecase owns its
// contracts instead of importing infra/crypto). issuer is the TOTP issuer name shown in the
// authenticator app. sessionTTL controls how long a session remains valid; pass 0 for the
// default (30 days).
// apiTokens enables the CLI API-token flow (issue/resolve/list/revoke). Pass nil to disable it:
// the token methods then return domain.ErrFeatureNotSupported.
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
) *Manager {
	ttl := sessionTTL
	if ttl <= 0 {
		ttl = defaultTTL
	}

	// Pre-compute once at startup so the first login attempt is not slower.
	// A failure here means crypto/rand is unavailable at boot (fatal), so fail hard rather
	// than ship a fallback hash whose frozen argon2 params would diverge from the configured
	// cost and reintroduce a login timing asymmetry. NewManager runs only at the composition
	// root, so a bootstrap panic is appropriate.
	dummyHash, err := passwordHasher.Hash("padmark-dummy-auth-sentinel")
	if err != nil {
		panic("auth: cannot precompute dummy password hash (crypto/rand unavailable): " + err.Error())
	}

	return &Manager{
		users:       users,
		invites:     invites,
		sessions:    sessions,
		apiTokens:   apiTokens,
		enc:         enc,
		pw:          passwordHasher,
		kdf:         keyDeriver,
		totp:        totp,
		log:         log,
		issuer:      issuer,
		ttl:         ttl,
		dummyPwHash: dummyHash,
	}
}

// Login authenticates username+password+totpCode and creates a new session.
// Returns the opaque session ID on success.
// All authentication failures return ErrInvalidTOTP to avoid leaking which factor failed.
func (m *Manager) Login(
	ctx context.Context, username, password, totpCode, userAgent, clientIP string,
) (string, error) {
	usr, err := m.users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Run the same argon2id work as a real attempt to prevent timing-based
			// username enumeration. The result is always discarded.
			m.pw.Verify(m.dummyPwHash, password)

			return "", domain.ErrInvalidTOTP
		}

		return "", fmt.Errorf("get user: %w", err)
	}

	factorErr := m.verifyLoginFactors(ctx, usr, password, totpCode)
	if factorErr != nil {
		return "", factorErr
	}

	sessionID, err := newSessionID()
	if err != nil {
		return "", err
	}

	now := time.Now()
	sess := &domain.Session{
		SessionID: sessionID,
		UserID:    usr.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
		UserAgent: userAgent,
		IP:        clientIP,
	}

	createErr := m.sessions.Create(ctx, sess)
	if createErr != nil {
		return "", fmt.Errorf("create session: %w", createErr)
	}

	updateErr := m.users.UpdateLastLogin(ctx, usr.ID, now)
	if updateErr != nil {
		m.log.WarnContext(ctx, "update last login failed", "user_id", usr.ID, "err", updateErr)
	}

	return sessionID, nil
}

// Logout deletes the session identified by sessionID.
func (m *Manager) Logout(ctx context.Context, sessionID string) error {
	err := m.sessions.Delete(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

// GetSession resolves sessionID to the owning User.
// Returns domain.ErrSessionExpired when the session is absent or expired.
func (m *Manager) GetSession(ctx context.Context, sessionID string) (*domain.User, error) {
	sess, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err //nolint:wrapcheck // ErrSessionExpired sentinel passes through unwrapped for errors.Is
	}

	usr, err := m.users.GetByID(ctx, sess.UserID)
	if err != nil {
		return nil, fmt.Errorf("get session user: %w", err)
	}

	return usr, nil
}

// GenerateInvite creates a single-use invite link for adminUserID.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *Manager) GenerateInvite(ctx context.Context, adminUserID string) (string, error) {
	admin, err := m.users.GetByID(ctx, adminUserID)
	if err != nil {
		return "", fmt.Errorf("get admin user: %w", err)
	}

	if !admin.IsAdmin {
		return "", domain.ErrForbidden
	}

	tok, err := m.invites.Issue(ctx, adminUserID)
	if err != nil {
		return "", fmt.Errorf("issue invite: %w", err)
	}

	return tok, nil
}

// AcceptInvite consumes the invite token, creates a new user with a fresh TOTP secret,
// and returns the QR code data URL the user should scan. The consume and the user
// insert happen in a single transaction, so a failed registration never burns the token.
func (m *Manager) AcceptInvite(ctx context.Context, token, username, password string) (string, error) {
	err := domain.ValidatePassword(password)
	if err != nil {
		return "", err //nolint:wrapcheck // ErrWeakPassword sentinel passes through for errors.Is
	}

	// Fast pre-check to reject an already-taken username before the expensive buildUser work
	// (argon2 hash + TOTP secret + QR). The authoritative guard is the unique constraint inside
	// RedeemInvite's transaction. Note: revealing "username taken" is an inherent UX trade-off
	// (the same signal is observable via RedeemInvite, which rolls back without burning the
	// invite on a collision); the pre-check does not widen that, it only avoids needless argon2.
	_, lookupErr := m.users.GetByUsername(ctx, username)
	if lookupErr == nil {
		return "", domain.ErrUserExists
	}

	if !errors.Is(lookupErr, domain.ErrNotFound) {
		return "", fmt.Errorf("check username: %w", lookupErr)
	}

	usr, qrURL, err := m.buildUser(username, password, false)
	if err != nil {
		return "", err
	}

	err = m.invites.RedeemInvite(ctx, token, username, usr)
	if err != nil {
		return "", err //nolint:wrapcheck // domain sentinels pass through for errors.Is
	}

	return qrURL, nil
}

// AcceptFirstAdmin creates the first admin user without requiring an invite token.
// Returns domain.ErrForbidden when users already exist (not empty DB).
//
// The List check is a best-effort gate; the hard guarantee against a concurrent
// double-bootstrap (e.g. two instances racing on an empty DB) is the partial
// unique index on users.is_admin: the second writer loses with domain.ErrUserExists.
func (m *Manager) AcceptFirstAdmin(ctx context.Context, username, password string) (string, error) {
	err := domain.ValidatePassword(password)
	if err != nil {
		return "", err //nolint:wrapcheck // ErrWeakPassword sentinel passes through for errors.Is
	}

	users, err := m.users.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list users: %w", err)
	}

	if len(users) > 0 {
		return "", domain.ErrForbidden
	}

	usr, qrURL, err := m.buildUser(username, password, true)
	if err != nil {
		return "", err
	}

	err = m.users.Create(ctx, usr)
	if err != nil {
		return "", err //nolint:wrapcheck // ErrUserExists sentinel passes through for errors.Is
	}

	return qrURL, nil
}

// ChangePassword verifies the current password and TOTP code, then re-encrypts the
// TOTP secret under a new derived key. All existing sessions are invalidated after the
// new session is safely persisted, so the caller never loses access on a transient error.
// Returns the new session ID on success.
func (m *Manager) ChangePassword(
	ctx context.Context, sessionID, oldPassword, newPassword, totpCode string,
) (string, error) {
	sess, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		return "", err //nolint:wrapcheck // ErrSessionExpired passes through for errors.Is
	}

	usr, err := m.users.GetByID(ctx, sess.UserID)
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}

	if !m.pw.Verify(usr.PasswordHash, oldPassword) {
		return "", domain.ErrInvalidPassword
	}

	// TOTP step-up: changing a password is account-critical — require the second factor
	// so a stolen session + known password alone cannot lock out the legitimate owner.
	// The decrypted secret is reused below, so it is decrypted only once.
	rawSecret, err := m.verifyTOTPStepUp(ctx, oldPassword, usr, totpCode)
	if err != nil {
		return "", err
	}

	newHash, newSalt, encSecret, err := m.buildNewCredentials(newPassword, rawSecret)
	if err != nil {
		return "", err
	}

	err = m.users.UpdatePassword(ctx, usr.ID, newHash, newSalt, encSecret)
	if err != nil {
		return "", fmt.Errorf("update password: %w", err)
	}

	// Create the replacement session BEFORE invalidating old ones.
	// If Create fails, old sessions remain intact and the user can retry.
	// If DeleteByUserID subsequently fails, we log a warning; the new session is
	// already returned to the client and old sessions will expire at their TTL.
	newID, err := newSessionID()
	if err != nil {
		return "", err
	}

	now := time.Now()
	newSess := &domain.Session{
		SessionID: newID,
		UserID:    usr.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
		UserAgent: sess.UserAgent,
		IP:        sess.IP,
	}

	err = m.sessions.Create(ctx, newSess)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	// Invalidate all old sessions except the one just created.
	// The new session must survive so the caller stays logged in.
	err = m.sessions.DeleteByUserIDExcept(ctx, usr.ID, newID)
	if err != nil {
		m.log.WarnContext(ctx, "invalidate old sessions after password change",
			"user_id", usr.ID, "err", err)
	}

	return newID, nil
}

// IsEmpty returns true when no users are registered (bootstrap state).
func (m *Manager) IsEmpty(ctx context.Context) (bool, error) {
	users, err := m.users.List(ctx)
	if err != nil {
		return false, fmt.Errorf("list users: %w", err)
	}

	return len(users) == 0, nil
}

// ListUsers returns all registered users for the admin panel.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *Manager) ListUsers(ctx context.Context, adminUserID string) ([]*domain.User, error) {
	admin, err := m.users.GetByID(ctx, adminUserID)
	if err != nil {
		return nil, fmt.Errorf("get admin user: %w", err)
	}

	if !admin.IsAdmin {
		return nil, domain.ErrForbidden
	}

	users, err := m.users.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	return users, nil
}

// RevokeUser removes a user. Returns domain.ErrForbidden when:
//   - the caller is not an admin
//   - the caller tries to revoke themselves
//   - the target is the last admin (would open the bootstrap hole)
func (m *Manager) RevokeUser(ctx context.Context, adminUserID, targetUserID string) error {
	if adminUserID == targetUserID {
		return domain.ErrForbidden
	}

	admin, err := m.users.GetByID(ctx, adminUserID)
	if err != nil {
		return fmt.Errorf("get admin user: %w", err)
	}

	if !admin.IsAdmin {
		return domain.ErrForbidden
	}

	target, err := m.users.GetByID(ctx, targetUserID)
	if err != nil {
		return fmt.Errorf("get target user: %w", err)
	}

	if target.IsAdmin {
		all, listErr := m.users.List(ctx)
		if listErr != nil {
			return fmt.Errorf("list users: %w", listErr)
		}

		remainingAdmins := 0

		for _, u := range all {
			if u.IsAdmin && u.ID != targetUserID {
				remainingAdmins++
			}
		}

		if remainingAdmins == 0 {
			return domain.ErrForbidden
		}
	}

	err = m.users.Revoke(ctx, targetUserID)
	if err != nil {
		return fmt.Errorf("revoke user: %w", err)
	}

	return nil
}

// buildNewCredentials validates newPassword complexity, derives a fresh KDF key, and
// re-encrypts the already-decrypted TOTP secret so it can be stored under the new password.
// rawSecret comes from verifyTOTPStepUp, so the secret is decrypted only once per change.
// Returns the new password hash, KDF salt, and encrypted TOTP secret.
func (m *Manager) buildNewCredentials(
	newPassword, rawSecret string,
) (hash string, salt []byte, encSecret string, err error) {
	err = domain.ValidatePassword(newPassword)
	if err != nil {
		return "", nil, "", err //nolint:wrapcheck // ErrWeakPassword sentinel passes through for errors.Is
	}

	newSalt, err := m.kdf.GenerateSalt()
	if err != nil {
		return "", nil, "", fmt.Errorf("generate kdf salt: %w", err)
	}

	newKey, err := m.kdf.DeriveKey([]byte(newPassword), newSalt)
	if err != nil {
		return "", nil, "", fmt.Errorf("derive new key: %w", err)
	}

	encSecret, err = m.enc.Encrypt(rawSecret, newKey)
	if err != nil {
		return "", nil, "", fmt.Errorf("encrypt totp secret: %w", err)
	}

	hash, err = m.pw.Hash(newPassword)
	if err != nil {
		return "", nil, "", fmt.Errorf("hash password: %w", err)
	}

	return hash, newSalt, encSecret, nil
}

// verifyLoginFactors checks the password and the TOTP second factor for usr.
// Returns domain.ErrInvalidTOTP on any factor mismatch so the caller cannot learn
// which factor failed (no username/password enumeration oracle).
//
// Timing note: a wrong password returns after one argon2 verify, while a correct
// password additionally runs HKDF + AES-GCM + TOTP validation, so the correct-password
// path is observably longer. This is accepted: it is not an enumeration oracle (exploiting
// it requires already knowing the password), the argon2 verify dominates the measurable
// time, and login is rate-limited.
func (m *Manager) verifyLoginFactors(ctx context.Context, usr *domain.User, password, totpCode string) error {
	if !m.pw.Verify(usr.PasswordHash, password) {
		return domain.ErrInvalidTOTP
	}

	derivedKey, err := m.kdf.DeriveKey([]byte(password), usr.KDFSalt)
	if err != nil {
		return fmt.Errorf("derive user key: %w", err)
	}

	secret, err := m.enc.Decrypt(usr.TOTPSecret, derivedKey)
	if err != nil {
		return fmt.Errorf("decrypt totp secret: %w", err)
	}

	ok, err := m.validateAndRecordTOTP(ctx, usr.ID, secret, totpCode)
	if err != nil {
		return err
	}

	if !ok {
		return domain.ErrInvalidTOTP
	}

	return nil
}

// verifyTOTPStepUp decrypts the TOTP secret using the old password key, validates the
// provided code, and returns the decrypted secret so the caller can re-encrypt it under a
// new key without a second HKDF + AES-GCM pass. Second-factor gate for account-critical ops.
func (m *Manager) verifyTOTPStepUp(
	ctx context.Context, oldPassword string, usr *domain.User, totpCode string,
) (string, error) {
	oldKey, err := m.kdf.DeriveKey([]byte(oldPassword), usr.KDFSalt)
	if err != nil {
		return "", fmt.Errorf("derive user key: %w", err)
	}

	rawSecret, err := m.enc.Decrypt(usr.TOTPSecret, oldKey)
	if err != nil {
		return "", fmt.Errorf("decrypt totp secret: %w", err)
	}

	ok, err := m.validateAndRecordTOTP(ctx, usr.ID, rawSecret, totpCode)
	if err != nil {
		return "", err
	}

	if !ok {
		return "", domain.ErrInvalidTOTP
	}

	return rawSecret, nil
}

// buildUser generates a fresh TOTP secret, encrypts it with a password-derived key, and
// assembles the user record together with its QR code data URL — without touching the DB.
// The caller persists the returned user (directly or via RedeemInvite); generating the QR
// here, before any insert, keeps the insert the last fallible step so it can be rolled back.
func (m *Manager) buildUser(username, password string, isAdmin bool) (*domain.User, string, error) {
	rawSecret, err := m.totp.GenerateSecret()
	if err != nil {
		return nil, "", fmt.Errorf("generate totp secret: %w", err)
	}

	kdfSalt, err := m.kdf.GenerateSalt()
	if err != nil {
		return nil, "", fmt.Errorf("generate kdf salt: %w", err)
	}

	derivedKey, err := m.kdf.DeriveKey([]byte(password), kdfSalt)
	if err != nil {
		return nil, "", fmt.Errorf("derive user key: %w", err)
	}

	encSecret, err := m.enc.Encrypt(rawSecret, derivedKey)
	if err != nil {
		return nil, "", fmt.Errorf("encrypt totp secret: %w", err)
	}

	pwHash, err := m.pw.Hash(password)
	if err != nil {
		return nil, "", fmt.Errorf("hash password: %w", err)
	}

	usr := &domain.User{
		ID:           uuid.New().String(),
		Username:     username,
		TOTPSecret:   encSecret,
		PasswordHash: pwHash,
		KDFSalt:      kdfSalt,
		IsAdmin:      isAdmin,
		CreatedAt:    time.Now(),
	}

	qrURL, err := m.totp.GenerateQRCode(m.issuer, username, rawSecret)
	if err != nil {
		return nil, "", fmt.Errorf("generate qr code: %w", err)
	}

	return usr, qrURL, nil
}

// validateAndRecordTOTP validates the TOTP code and persists the accepted counter.
// Returns false when the code is invalid or has already been used (replay).
// Replay protection is enforced by an atomic conditional update in the store, so it
// survives process restarts and is consistent across instances.
func (m *Manager) validateAndRecordTOTP(ctx context.Context, userID, secret, code string) (bool, error) {
	valid, counter := m.totp.ValidateWithCounter(secret, code)
	if !valid {
		return false, nil
	}

	accepted, err := m.users.UpdateTOTPCounter(ctx, userID, counter)
	if err != nil {
		return false, fmt.Errorf("record totp counter: %w", err)
	}

	return accepted, nil
}

func newSessionID() (string, error) {
	buf := make([]byte, sessionIDBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
