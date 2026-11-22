//go:generate go run go.uber.org/mock/mockgen@latest -source=session.go -destination=session_mocks_test.go -package=auth -write_package_comment=false

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// DefaultSessionTTL is how long a session stays valid when NewSessionManager is given no
// override (sessionTTL <= 0). It is the single source of truth for the session lifetime default:
// internal/infra/server's --session-ttl flag default and internal/adapters/web's session cookie
// MaxAge (set via Handler.WithSessionTTL) both derive from this constant instead of each keeping
// their own copy, which would otherwise drift out of sync with the actual server-side TTL.
const DefaultSessionTTL = 30 * 24 * time.Hour

// SessionUserStore is the part of the user store that SessionManager uses.
// Errors are fail-hard unless the SessionManager method says otherwise.
type SessionUserStore interface {
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	// UpdateLastLogin records the time of the last login. Fail-soft: Login logs the error.
	UpdateLastLogin(ctx context.Context, id uuid.UUID, t time.Time) error
	// ChangePassword replaces the password hash, KDF salt and sealed TOTP secret of sess.UserID,
	// makes sess the only session of that user and deletes all API tokens of that user, in one
	// transaction. Fail-hard: on an error nothing changed, so the old password, sessions and API
	// tokens stay valid and sess does not exist. Returns domain.ErrNotFound when the user is gone.
	ChangePassword(
		ctx context.Context, sess *domain.Session, passwordHash string, kdfSalt []byte, totpSecret string,
	) error
	// UpdateTOTPSecret replaces only the sealed TOTP secret, leaving the password hash and KDF
	// salt untouched. It exists for the re-seal-on-login migration (see
	// SessionManager.resealTOTPSecret), where the secret is rewritten under the current argon2
	// cost but the credentials themselves do not change. It writes only while the stored seal
	// equals expected and reports false otherwise, so a reseal started before a concurrent
	// password change never overwrites the seal of the new password.
	UpdateTOTPSecret(ctx context.Context, id uuid.UUID, expected, totpSecret string) (bool, error)
	// UpdateTOTPCounter atomically advances the user's last accepted TOTP counter,
	// returning false when counter is not strictly greater (replay). The conditional
	// update is the cross-instance, restart-safe guard against TOTP code reuse.
	UpdateTOTPCounter(ctx context.Context, id uuid.UUID, counter int64) (bool, error)
}

// SessionStore persists authenticated browser sessions.
//
// All key parameters carry the at-rest value domain.Session.SessionHash — the SHA-256 digest
// of the raw session id (see hashToken) — never the raw cookie value.
type SessionStore interface {
	Create(ctx context.Context, s *domain.Session) error
	// Get returns the session that is not expired at now. An unknown or expired session gives
	// domain.ErrSessionExpired.
	Get(ctx context.Context, sessionID string, now time.Time) (*domain.Session, error)
	Delete(ctx context.Context, sessionID string) error
	// DeleteExpired removes the sessions that are expired at now. Fail-soft: Login calls it
	// after it stores a new session and only logs an error.
	DeleteExpired(ctx context.Context, now time.Time) error
}

// SessionManager authenticates a user and manages their login session lifecycle.
type SessionManager struct {
	totp        TOTPManager
	sessions    SessionStore
	sealer      SecretSealer
	pw          PasswordHasher
	users       SessionUserStore
	log         *slog.Logger
	now         func() time.Time
	dummyPwHash string
	ttl         time.Duration
}

// NewSessionManager returns a new SessionManager. sessionTTL controls how long a session
// remains valid; pass 0 for the default (30 days). deps.Now is the time source of session expiry.
//
// Returns an error when crypto/rand is unavailable (see the dummy-hash precompute below) —
// NewSessionManager runs only at the composition root, so the caller is expected to treat this
// as a fatal startup error rather than retry.
func NewSessionManager(
	users SessionUserStore, sessions SessionStore, deps *Deps, sessionTTL time.Duration,
) (*SessionManager, error) {
	ttl := sessionTTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}

	// Pre-compute once at startup so the first login attempt is not slower. A failure here means
	// crypto/rand is unavailable at boot — a fallback hash would ship with frozen argon2 params
	// that diverge from the configured cost, reintroducing a login timing asymmetry, so this
	// fails hard (returns an error) rather than silently degrading.
	dummyHash, err := deps.PasswordHasher.Hash("padmark-dummy-auth-sentinel")
	if err != nil {
		return nil, fmt.Errorf("precompute dummy password hash: %w", err)
	}

	return &SessionManager{
		users:       users,
		sessions:    sessions,
		pw:          deps.PasswordHasher,
		sealer:      deps.Sealer,
		totp:        deps.TOTP,
		log:         deps.Log,
		now:         deps.Now,
		ttl:         ttl,
		dummyPwHash: dummyHash,
	}, nil
}

// LoginRequest holds the values of one sign-in. Named fields replace six string parameters of
// one type, which a caller could give in the wrong sequence without a compile error.
type LoginRequest struct {
	Username string
	Password string
	TOTPCode string
	// UserAgent and ClientIP are stored with the session for the session list.
	UserAgent string
	ClientIP  string
	// PreviousSessionID is the session that the client presented when it signed in (empty if none).
	PreviousSessionID string
}

// Login authenticates the username, password and TOTP code of req and creates a new session.
// Returns the opaque session ID on success.
// All authentication failures return ErrInvalidTOTP to avoid leaking which factor failed.
//
// req.PreviousSessionID is revoked once the new session exists, so re-authenticating never
// leaves the old cookie valid (session-fixation hygiene, and a forgotten or stolen cookie cannot
// outlive a re-login).
// Revocation is fail-soft: the new session is already valid, so a store error is logged, not
// returned.
func (m *SessionManager) Login(ctx context.Context, req *LoginRequest) (string, error) {
	usr, err := m.users.GetByUsername(ctx, req.Username)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Run the same argon2id work as a real attempt to prevent timing-based
			// username enumeration. The result is always discarded.
			m.pw.Verify(m.dummyPwHash, req.Password)

			return "", domain.ErrInvalidTOTP
		}

		return "", fmt.Errorf("get user: %w", err)
	}

	factorErr := m.verifyLoginFactors(ctx, usr, req.Password, req.TOTPCode)
	if factorErr != nil {
		return "", factorErr
	}

	sessionID, err := newSessionID()
	if err != nil {
		return "", err
	}

	now := m.now()
	// The returned id goes into the client cookie; only its SHA-256 digest is persisted
	// (SessionHash, see hashToken) so a leaked database carries no replayable sessions.
	sess := &domain.Session{
		SessionHash: hashToken(sessionID),
		UserID:      usr.ID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(m.ttl),
		UserAgent:   req.UserAgent,
		IP:          req.ClientIP,
	}

	createErr := m.sessions.Create(ctx, sess)
	if createErr != nil {
		return "", fmt.Errorf("create session: %w", createErr)
	}

	confirmErr := m.confirmCredentials(ctx, usr, sess.SessionHash)
	if confirmErr != nil {
		return "", confirmErr
	}

	m.revokePreviousSession(ctx, usr.ID, req.PreviousSessionID)
	m.deleteExpiredSessions(ctx, now)

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
	sess, err := m.sessions.Get(ctx, hashToken(sessionID), m.now())
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
// TOTP secret under a new derived key. SessionUserStore.ChangePassword writes the new credentials, the
// replacement session and the revocation of all other sessions and API tokens in one transaction.
// Thus a database failure or a process stop cannot keep the old access valid next to the new
// password: on an error the account is as it was, and the caller can retry with a new TOTP code.
// Returns the new session ID on success.
func (m *SessionManager) ChangePassword(
	ctx context.Context, sessionID, oldPassword, newPassword, totpCode string,
) (string, error) {
	sess, err := m.sessions.Get(ctx, hashToken(sessionID), m.now())
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

	creds, err := m.buildNewCredentials(newPassword, rawSecret)
	if err != nil {
		return "", err
	}

	newID, newSess, err := m.newReplacementSession(sess)
	if err != nil {
		return "", err
	}

	err = m.users.ChangePassword(ctx, newSess, creds.hash, creds.salt, creds.encSecret)
	if err != nil {
		m.log.ErrorContext(ctx, "change password", "user_id", usr.ID, "err", err)

		return "", fmt.Errorf("change password: %w", err)
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
		return err
	}

	return nil
}

// confirmCredentials makes sure that the password this login verified is still the account's
// password now that the new session exists. A session created after the revocation of a
// concurrent ChangePassword would otherwise survive with the old password.
// When the hash changed, or the account is gone, the new session is deleted and the login fails
// as a wrong factor (ErrInvalidTOTP).
func (m *SessionManager) confirmCredentials(ctx context.Context, usr *domain.User, sessionHash string) error {
	current, err := m.users.GetByID(ctx, usr.ID)
	if err == nil && current.PasswordHash == usr.PasswordHash {
		return nil
	}

	delErr := m.sessions.Delete(ctx, sessionHash)
	if delErr != nil {
		m.log.ErrorContext(ctx, "delete session after credentials changed", "user_id", usr.ID, "err", delErr)
	}

	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("get user: %w", err)
	}

	return domain.ErrInvalidTOTP
}

// newReplacementSession returns a new raw session ID and its session record for the user of sess,
// with the same client details. It does not store the record.
func (m *SessionManager) newReplacementSession(sess *domain.Session) (string, *domain.Session, error) {
	newID, err := newSessionID()
	if err != nil {
		return "", nil, err
	}

	now := m.now()
	newSess := &domain.Session{
		SessionHash: hashToken(newID),
		UserID:      sess.UserID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(m.ttl),
		UserAgent:   sess.UserAgent,
		IP:          sess.IP,
	}

	return newID, newSess, nil
}

// buildNewCredentials validates newPassword complexity, derives a fresh KDF key, and
// re-encrypts the already-decrypted TOTP secret so it can be stored under the new password.
// rawSecret comes from verifyTOTPStepUp, so the secret is decrypted only once per change.
func (m *SessionManager) buildNewCredentials(newPassword, rawSecret string) (*newCredentials, error) {
	err := domain.ValidatePassword(newPassword)
	if err != nil {
		return nil, fmt.Errorf("validate new password: %w", err)
	}

	salt, err := m.sealer.GenerateSalt()
	if err != nil {
		return nil, fmt.Errorf("generate kdf salt: %w", err)
	}

	encSecret, err := m.sealer.Seal(rawSecret, []byte(newPassword), salt)
	if err != nil {
		return nil, fmt.Errorf("seal totp secret: %w", err)
	}

	hash, err := m.pw.Hash(newPassword)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	return &newCredentials{hash: hash, encSecret: encSecret, salt: salt}, nil
}

// newCredentials is the stored form of a changed password: its hash, the new KDF salt and the
// TOTP secret sealed under the new password.
type newCredentials struct {
	hash      string
	encSecret string
	salt      []byte
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

	updated, err := m.users.UpdateTOTPSecret(ctx, usr.ID, usr.TOTPSecret, sealed)
	if err != nil {
		m.log.WarnContext(ctx, "persist resealed totp secret", "user_id", usr.ID, "err", err)

		return
	}

	if !updated {
		// The credentials changed after this login read them; the new seal already uses the
		// current parameters, so there is nothing left to migrate.
		m.log.DebugContext(ctx, "totp reseal skipped: credentials changed concurrently", "user_id", usr.ID)

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
	return newRandomToken()
}

// deleteExpiredSessions removes expired sessions after a login. No background worker removes
// them, so each login does it. Fail-soft: the new session is valid, so an error is only logged.
func (m *SessionManager) deleteExpiredSessions(ctx context.Context, now time.Time) {
	err := m.sessions.DeleteExpired(ctx, now)
	if err != nil {
		m.log.WarnContext(ctx, "delete expired sessions failed", "err", err)
	}
}

// revokePreviousSession deletes the session a client held before signing in again; no-op when it
// held none. See Login for why a failure is only logged.
func (m *SessionManager) revokePreviousSession(ctx context.Context, userID uuid.UUID, previousSessionID string) {
	if previousSessionID == "" {
		return
	}

	err := m.sessions.Delete(ctx, hashToken(previousSessionID))
	if err != nil {
		m.log.WarnContext(ctx, "revoke previous session on login failed", "user_id", userID, "err", err)
	}
}
