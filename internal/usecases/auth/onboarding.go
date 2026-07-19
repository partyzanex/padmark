package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// OnboardingManager creates the first admin account and enrolls new users via invite tokens.
type OnboardingManager struct {
	totp    TOTPManager
	invites InviteStore
	enc     Encryptor
	pw      PasswordHasher
	kdf     KeyDeriver
	users   UserStore
	issuer  string
}

// NewOnboardingManager returns a new OnboardingManager. issuer is the TOTP issuer name shown
// in the authenticator app when a new user enrolls.
func NewOnboardingManager(
	users UserStore, invites InviteStore, passwordHasher PasswordHasher, kdf KeyDeriver,
	enc Encryptor, totp TOTPManager, issuer string,
) *OnboardingManager {
	return &OnboardingManager{
		users:   users,
		invites: invites,
		pw:      passwordHasher,
		kdf:     kdf,
		enc:     enc,
		totp:    totp,
		issuer:  issuer,
	}
}

// IsEmpty returns true when no users are registered (bootstrap state).
func (m *OnboardingManager) IsEmpty(ctx context.Context) (bool, error) {
	users, err := m.users.List(ctx)
	if err != nil {
		return false, fmt.Errorf("list users: %w", err)
	}

	return len(users) == 0, nil
}

// GenerateInvite creates a single-use invite link for adminUserID.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *OnboardingManager) GenerateInvite(ctx context.Context, adminUserID uuid.UUID) (string, error) {
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
func (m *OnboardingManager) AcceptInvite(ctx context.Context, token, username, password string) (string, error) {
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
func (m *OnboardingManager) AcceptFirstAdmin(ctx context.Context, username, password string) (string, error) {
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

// buildUser generates a fresh TOTP secret, encrypts it with a password-derived key, and
// assembles the user record together with its QR code data URL — without touching the DB.
// The caller persists the returned user (directly or via RedeemInvite); generating the QR
// here, before any insert, keeps the insert the last fallible step so it can be rolled back.
func (m *OnboardingManager) buildUser(username, password string, isAdmin bool) (*domain.User, string, error) {
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
		ID:           uuid.New(),
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
