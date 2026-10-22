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
	apiTokens   APITokenStore
	sealer      SecretSealer
	pw          PasswordHasher
	users       UserStore
	log         *slog.Logger
	dummyPwHash string
	ttl         time.Duration
}

// NewSessionManager returns a new SessionManager. sessionTTL controls how long a session
// remains valid; pass 0 for the default (30 days). apiTokens (nil when the API-token flow is
// disabled) lets a password change revoke the account's API tokens.
//
// Returns an error when crypto/rand is unavailable (see the dummy-hash precompute below) —
// NewSessionManager runs only at the composition root, so the caller is expected to treat this
// as a fatal startup error rather than retry.
func NewSessionManager(
	users UserStore, sessions SessionStore, apiTokens APITokenStore, passwordHasher PasswordHasher,
	sealer SecretSealer, totp TOTPManager, log *slog.Logger, sessionTTL time.Duration,
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
		apiTokens:   apiTokens,
		pw:          passwordHasher,
		sealer:      sealer,
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
	// The returned id goes into the client cookie; only its SHA-256 digest is persisted
	// (SessionHash, see hashToken) so a leaked database carries no replayable sessions.
	sess := &domain.Session{
		SessionHash: hashToken(sessionID),
		UserID:      usr.ID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(m.ttl),
		UserAgent:   userAgent,
		IP:          clientIP,
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
	err := m.sessions.Delete(ctx, hashToken(sessionID))
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

// GetSession resolves sessionID to the owning User.
// Returns domain.ErrSessionExpired when the session is absent or expired.
func (m *SessionManager) GetSession(ctx context.Context, sessionID string) (*domain.User, error) {
	sess, err := m.sessions.Get(ctx, hashToken(sessionID))
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
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
// Returns the new session ID on success. ErrSessionRevocation is partial success:
// the password and replacement session are persisted, but older sessions may remain.
// Callers must keep that replacement session and show the revocation warning.
func (m *SessionManager) ChangePassword(
	ctx context.Context, sessionID, oldPassword, newPassword, totpCode string,
) (string, error) {
	sess, err := m.sessions.Get(ctx, hashToken(sessionID))
	if err != nil {
		return "", fmt.Errorf("get session: %w", err)
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
	// A revocation failure must be surfaced alongside the replacement session.
	newID, err := newSessionID()
	if err != nil {
		return "", err
	}

	now := time.Now()
	newSess := &domain.Session{
		SessionHash: hashToken(newID),
		UserID:      usr.ID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(m.ttl),
		UserAgent:   sess.UserAgent,
		IP:          sess.IP,
	}

	err = m.sessions.Create(ctx, newSess)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	err = m.revokeOldCredentials(ctx, usr.ID, newID)
	if err != nil {
		return newID, err
	}

	return newID, nil
}

// VerifyStepUp re-authenticates userID with the account password and a fresh TOTP code, exactly
// like the login factors: the code is replay-protected, and an outdated seal is migrated on
// success. Implements StepUpVerifier. Returns domain.ErrStepUpFailed for a wrong password or a
// wrong/replayed code.
func (m *SessionManager) VerifyStepUp(ctx context.Context, userID uuid.UUID, stepUp StepUp) error {
	usr, err := m.users.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	err = m.verifyLoginFactors(ctx, usr, stepUp.Password, stepUp.TOTPCode)
	if errors.Is(err, domain.ErrInvalidTOTP) {
		return domain.ErrStepUpFailed
	}

	if err != nil {
		return fmt.Errorf("verify login factors: %w", err)
	}

	return nil
}

// revokeOldCredentials invalidates everything the old password vouched for: every session but
// keepSessionID (the replacement — it must survive so the caller stays logged in), and every API
// token of the account — a leaked key, and whatever it minted, must not outlive the change. Both
// are attempted even when one fails; a failure is ErrSessionRevocation (partial success).
func (m *SessionManager) revokeOldCredentials(ctx context.Context, userID uuid.UUID, keepSessionID string) error {
	err := errors.Join(
		m.sessions.DeleteByUserIDExcept(ctx, userID, hashToken(keepSessionID)),
		m.revokeAPITokens(ctx, userID),
	)
	if err != nil {
		m.log.ErrorContext(ctx, "invalidate old sessions and api tokens after password change",
			"user_id", userID, "err", err)

		return fmt.Errorf("%w: %w", domain.ErrSessionRevocation, err)
	}

	return nil
}

// revokeAPITokens deletes every API token of userID; a no-op when the API-token flow is disabled.
func (m *SessionManager) revokeAPITokens(ctx context.Context, userID uuid.UUID) error {
	if m.apiTokens == nil {
		return nil
	}

	err := m.apiTokens.DeleteByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("revoke api tokens: %w", err)
	}

	return nil
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
		return "", nil, "", fmt.Errorf("validate new password: %w", err)
	}

	newSalt, err := m.sealer.GenerateSalt()
	if err != nil {
		return "", nil, "", fmt.Errorf("generate kdf salt: %w", err)
	}

	encSecret, err = m.sealer.Seal(rawSecret, []byte(newPassword), newSalt)
	if err != nil {
		return "", nil, "", fmt.Errorf("seal totp secret: %w", err)
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
// Timing note: a wrong password returns after one argon2 verify, while a correct password
// additionally runs the sealer's own argon2 pass + AES-GCM + TOTP validation, so the
// correct-password path is observably longer. This is accepted: it is not an enumeration oracle
// (exploiting it requires already knowing the password), and login is rate-limited.
func (m *SessionManager) verifyLoginFactors(ctx context.Context, usr *domain.User, password, totpCode string) error {
	if !m.pw.Verify(usr.PasswordHash, password) {
		return domain.ErrInvalidTOTP
	}

	secret, stale, err := m.sealer.Open(usr.TOTPSecret, []byte(password), usr.KDFSalt)
	if err != nil {
		return fmt.Errorf("open totp secret: %w", err)
	}

	ok, err := m.validateAndRecordTOTP(ctx, usr.ID, secret, totpCode)
	if err != nil {
		return err
	}

	if !ok {
		return domain.ErrInvalidTOTP
	}

	// Login is the only place holding both the plaintext password and the opened secret, so it
	// is where an outdated seal can be rewritten. Done after the TOTP check so a wrong code
	// never triggers a write.
	if stale {
		m.resealTOTPSecret(ctx, usr, password, secret)
	}

	return nil
}

// resealTOTPSecret rewrites usr's TOTP secret under the sealer's current parameters, migrating
// a legacy or outdated-cost seal in place. The KDF salt is unchanged, so only totp_secret moves.
//
// Fail-soft: the login it runs inside has already succeeded on both factors, and the old seal
// stays perfectly usable, so a failure here is logged and the login proceeds. The next login
// retries the migration.
func (m *SessionManager) resealTOTPSecret(ctx context.Context, usr *domain.User, password, secret string) {
	sealed, err := m.sealer.Seal(secret, []byte(password), usr.KDFSalt)
	if err != nil {
		m.log.WarnContext(ctx, "reseal totp secret", "user_id", usr.ID, "err", err)

		return
	}

	err = m.users.UpdateTOTPSecret(ctx, usr.ID, sealed)
	if err != nil {
		m.log.WarnContext(ctx, "persist resealed totp secret", "user_id", usr.ID, "err", err)

		return
	}

	m.log.InfoContext(ctx, "totp secret resealed under current kdf parameters", "user_id", usr.ID)
}

// verifyTOTPStepUp opens the TOTP secret with the old password, validates the provided code,
// and returns the plaintext secret so the caller can re-seal it under a new key without a second
// derive + AES-GCM pass. Second-factor gate for account-critical ops.
//
// A stale seal is ignored here: every caller re-seals the secret under a fresh salt and the
// current parameters anyway, which supersedes the migration login would have performed.
func (m *SessionManager) verifyTOTPStepUp(
	ctx context.Context, oldPassword string, usr *domain.User, totpCode string,
) (string, error) {
	rawSecret, _, err := m.sealer.Open(usr.TOTPSecret, []byte(oldPassword), usr.KDFSalt)
	if err != nil {
		return "", fmt.Errorf("open totp secret: %w", err)
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
