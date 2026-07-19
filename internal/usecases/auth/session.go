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

const sessionIDBytes = 32

// DefaultSessionTTL is how long a session stays valid when NewSessionManager is given no
// override (sessionTTL <= 0). It is the single source of truth for the session lifetime default:
// internal/infra/server's --session-ttl flag default and internal/adapters/http's session cookie
// MaxAge (set via Handler.WithSessionTTL) both derive from this constant instead of each keeping
// their own copy, which would otherwise drift out of sync with the actual server-side TTL.
const DefaultSessionTTL = 30 * 24 * time.Hour

// SessionManager authenticates a user and manages their login session lifecycle.
type SessionManager struct {
	totp        TOTPManager
	sessions    SessionStore
	enc         Encryptor
	pw          PasswordHasher
	kdf         KeyDeriver
	users       UserStore
	log         *slog.Logger
	dummyPwHash string
	ttl         time.Duration
}

// NewSessionManager returns a new SessionManager. sessionTTL controls how long a session
// remains valid; pass 0 for the default (30 days).
//
// Returns an error when crypto/rand is unavailable (see the dummy-hash precompute below) —
// NewSessionManager runs only at the composition root, so the caller is expected to treat this
// as a fatal startup error rather than retry.
func NewSessionManager(
	users UserStore, sessions SessionStore, passwordHasher PasswordHasher, kdf KeyDeriver,
	enc Encryptor, totp TOTPManager, log *slog.Logger, sessionTTL time.Duration,
) (*SessionManager, error) {
	ttl := sessionTTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}

	// Pre-compute once at startup so the first login attempt is not slower. A failure here means
	// crypto/rand is unavailable at boot — a fallback hash would ship with frozen argon2 params
	// that diverge from the configured cost, reintroducing a login timing asymmetry, so this
	// fails hard (returns an error) rather than silently degrading.
	dummyHash, err := passwordHasher.Hash("padmark-dummy-auth-sentinel")
	if err != nil {
		return nil, fmt.Errorf("precompute dummy password hash: %w", err)
	}

	return &SessionManager{
		users:       users,
		sessions:    sessions,
		pw:          passwordHasher,
		kdf:         kdf,
		enc:         enc,
		totp:        totp,
		log:         log,
		ttl:         ttl,
		dummyPwHash: dummyHash,
	}, nil
}

// Login authenticates username+password+totpCode and creates a new session.
// Returns the opaque session ID on success.
// All authentication failures return ErrInvalidTOTP to avoid leaking which factor failed.
func (m *SessionManager) Login(
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
func (m *SessionManager) Logout(ctx context.Context, sessionID string) error {
	err := m.sessions.Delete(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

// GetSession resolves sessionID to the owning User.
// Returns domain.ErrSessionExpired when the session is absent or expired.
func (m *SessionManager) GetSession(ctx context.Context, sessionID string) (*domain.User, error) {
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

// ChangePassword verifies the current password and TOTP code, then re-encrypts the
// TOTP secret under a new derived key. All existing sessions are invalidated after the
// new session is safely persisted, so the caller never loses access on a transient error.
// Returns the new session ID on success.
func (m *SessionManager) ChangePassword(
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

// buildNewCredentials validates newPassword complexity, derives a fresh KDF key, and
// re-encrypts the already-decrypted TOTP secret so it can be stored under the new password.
// rawSecret comes from verifyTOTPStepUp, so the secret is decrypted only once per change.
// Returns the new password hash, KDF salt, and encrypted TOTP secret.
func (m *SessionManager) buildNewCredentials(
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
func (m *SessionManager) verifyLoginFactors(ctx context.Context, usr *domain.User, password, totpCode string) error {
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
func (m *SessionManager) verifyTOTPStepUp(
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

// validateAndRecordTOTP validates the TOTP code and persists the accepted counter.
// Returns false when the code is invalid or has already been used (replay).
// Replay protection is enforced by an atomic conditional update in the store, so it
// survives process restarts and is consistent across instances.
func (m *SessionManager) validateAndRecordTOTP(
	ctx context.Context, userID uuid.UUID, secret, code string,
) (bool, error) {
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
