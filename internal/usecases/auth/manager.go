//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=auth

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
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
	Revoke(ctx context.Context, id string) error
}

// InviteStore persists single-use invite links.
type InviteStore interface {
	Issue(ctx context.Context, createdByID string) (string, error)
	Consume(ctx context.Context, token, username string) (*domain.Invite, error)
}

// SessionStore persists authenticated browser sessions.
type SessionStore interface {
	Create(ctx context.Context, s *domain.Session) error
	Get(ctx context.Context, sessionID string) (*domain.Session, error)
	Delete(ctx context.Context, sessionID string) error
}

// Encryptor encrypts and decrypts TOTP secrets at rest.
type Encryptor interface {
	Encrypt(plaintext, key string) (string, error)
	Decrypt(ciphertext, key string) (string, error)
}

// Manager orchestrates TOTP-based authentication and invite-link onboarding.
type Manager struct {
	users        UserStore
	invites      InviteStore
	sessions     SessionStore
	enc          Encryptor
	log          *slog.Logger
	totpCounters sync.Map
	issuer       string
	dummyPwHash  string
	ttl          time.Duration
	firstAdminMu sync.Mutex
}

// NewManager returns a new auth Manager.
// issuer is the TOTP issuer name shown in the authenticator app.
// sessionTTL controls how long a session remains valid; pass 0 for the default (30 days).
func NewManager(
	users UserStore,
	invites InviteStore,
	sessions SessionStore,
	enc Encryptor,
	log *slog.Logger,
	issuer string,
	sessionTTL time.Duration,
) *Manager {
	ttl := sessionTTL
	if ttl <= 0 {
		ttl = defaultTTL
	}

	// Pre-compute once at startup so the first login attempt is not slower.
	dummyHash, err := crypto.HashPassword("padmark-dummy-auth-sentinel")
	if err != nil {
		// crypto/rand failure — fall back to a syntactically valid but un-matchable hash.
		dummyHash = "v1$65536$1$1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}

	return &Manager{
		users:       users,
		invites:     invites,
		sessions:    sessions,
		enc:         enc,
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
			crypto.VerifyPassword(m.dummyPwHash, password)

			return "", domain.ErrInvalidTOTP
		}

		return "", fmt.Errorf("get user: %w", err)
	}

	if !crypto.VerifyPassword(usr.PasswordHash, password) {
		return "", domain.ErrInvalidTOTP
	}

	derivedKey, keyErr := crypto.DeriveUserKey([]byte(password), usr.KDFSalt)
	if keyErr != nil {
		return "", fmt.Errorf("derive user key: %w", keyErr)
	}

	secret, decErr := m.enc.Decrypt(usr.TOTPSecret, derivedKey)
	if decErr != nil {
		return "", fmt.Errorf("decrypt totp secret: %w", decErr)
	}

	if !m.validateAndRecordTOTP(usr.ID, secret, totpCode) {
		return "", domain.ErrInvalidTOTP
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
// and returns the QR code data URL the user should scan.
func (m *Manager) AcceptInvite(ctx context.Context, token, username, password string) (string, error) {
	err := domain.ValidatePassword(password)
	if err != nil {
		return "", err //nolint:wrapcheck // ErrWeakPassword sentinel passes through for errors.Is
	}

	// Check username uniqueness before consuming the invite so a collision does not burn the token.
	_, lookupErr := m.users.GetByUsername(ctx, username)
	if lookupErr == nil {
		return "", domain.ErrUserExists
	}

	if !errors.Is(lookupErr, domain.ErrNotFound) {
		return "", fmt.Errorf("check username: %w", lookupErr)
	}

	_, err = m.invites.Consume(ctx, token, username)
	if err != nil {
		return "", err //nolint:wrapcheck // domain sentinels (ErrInviteExpired/Used/NotFound) pass through for errors.Is
	}

	return m.createUser(ctx, username, password, false)
}

// AcceptFirstAdmin creates the first admin user without requiring an invite token.
// Returns domain.ErrForbidden when users already exist (not empty DB).
func (m *Manager) AcceptFirstAdmin(ctx context.Context, username, password string) (string, error) {
	err := domain.ValidatePassword(password)
	if err != nil {
		return "", err //nolint:wrapcheck // ErrWeakPassword sentinel passes through for errors.Is
	}

	m.firstAdminMu.Lock()
	defer m.firstAdminMu.Unlock()

	users, err := m.users.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list users: %w", err)
	}

	if len(users) > 0 {
		return "", domain.ErrForbidden
	}

	return m.createUser(ctx, username, password, true)
}

// ChangePassword re-encrypts the TOTP secret under a new derived key.
// Verifies the session and old password before applying the change.
func (m *Manager) ChangePassword(
	ctx context.Context, sessionID, oldPassword, newPassword string,
) error {
	sess, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		return err //nolint:wrapcheck // ErrSessionExpired passes through for errors.Is
	}

	usr, err := m.users.GetByID(ctx, sess.UserID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	if !crypto.VerifyPassword(usr.PasswordHash, oldPassword) {
		return domain.ErrInvalidPassword
	}

	err = domain.ValidatePassword(newPassword)
	if err != nil {
		return err //nolint:wrapcheck // ErrWeakPassword sentinel passes through for errors.Is
	}

	oldKey, err := crypto.DeriveUserKey([]byte(oldPassword), usr.KDFSalt)
	if err != nil {
		return fmt.Errorf("derive old key: %w", err)
	}

	rawSecret, err := m.enc.Decrypt(usr.TOTPSecret, oldKey)
	if err != nil {
		return fmt.Errorf("decrypt totp secret: %w", err)
	}

	newSalt, err := crypto.GenerateKDFSalt()
	if err != nil {
		return fmt.Errorf("generate kdf salt: %w", err)
	}

	newKey, err := crypto.DeriveUserKey([]byte(newPassword), newSalt)
	if err != nil {
		return fmt.Errorf("derive new key: %w", err)
	}

	encSecret, err := m.enc.Encrypt(rawSecret, newKey)
	if err != nil {
		return fmt.Errorf("encrypt totp secret: %w", err)
	}

	newHash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	err = m.users.UpdatePassword(ctx, usr.ID, newHash, newSalt, encSecret)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	return nil
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

// createUser generates TOTP secret, encrypts it with a password-derived key, and creates the user record.
func (m *Manager) createUser(ctx context.Context, username, password string, isAdmin bool) (string, error) {
	rawSecret, err := crypto.GenerateTOTPSecret()
	if err != nil {
		return "", fmt.Errorf("generate totp secret: %w", err)
	}

	kdfSalt, err := crypto.GenerateKDFSalt()
	if err != nil {
		return "", fmt.Errorf("generate kdf salt: %w", err)
	}

	derivedKey, err := crypto.DeriveUserKey([]byte(password), kdfSalt)
	if err != nil {
		return "", fmt.Errorf("derive user key: %w", err)
	}

	encSecret, err := m.enc.Encrypt(rawSecret, derivedKey)
	if err != nil {
		return "", fmt.Errorf("encrypt totp secret: %w", err)
	}

	pwHash, err := crypto.HashPassword(password)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	userID := uuid.New().String()

	usr := &domain.User{
		ID:           userID,
		Username:     username,
		TOTPSecret:   encSecret,
		PasswordHash: pwHash,
		KDFSalt:      kdfSalt,
		IsAdmin:      isAdmin,
		CreatedAt:    time.Now(),
	}

	createErr := m.users.Create(ctx, usr)
	if createErr != nil {
		return "", createErr //nolint:wrapcheck // ErrUserExists sentinel passes through for errors.Is
	}

	qrURL, err := crypto.GenerateQRCodeDataURL(m.issuer, username, rawSecret)
	if err != nil {
		return "", fmt.Errorf("generate qr code: %w", err)
	}

	return qrURL, nil
}

// validateAndRecordTOTP validates the TOTP code and stores the counter on success.
// Returns false when the code is invalid or has already been used (replay).
func (m *Manager) validateAndRecordTOTP(userID, secret, code string) bool {
	valid, counter := crypto.ValidateTOTPWithCounter(secret, code)
	if !valid {
		return false
	}

	if lastVal, loaded := m.totpCounters.Load(userID); loaded {
		if lastCounter, ok := lastVal.(int64); ok && counter <= lastCounter {
			return false
		}
	}

	m.totpCounters.Store(userID, counter)

	return true
}

func newSessionID() (string, error) {
	buf := make([]byte, sessionIDBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
