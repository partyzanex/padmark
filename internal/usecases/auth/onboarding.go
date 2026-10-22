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
	sealer  SecretSealer
	pw      PasswordHasher
	users   UserStore
	stepUp  StepUpVerifier
	issuer  string
}

// NewOnboardingManager returns a new OnboardingManager. issuer is the TOTP issuer name shown
// in the authenticator app when a new user enrolls; stepUp re-authenticates a bearer-token
// caller before it may issue an invite.
func NewOnboardingManager(
	users UserStore, invites InviteStore, passwordHasher PasswordHasher,
	sealer SecretSealer, totp TOTPManager, stepUp StepUpVerifier, issuer string,
) *OnboardingManager {
	return &OnboardingManager{
		users:   users,
		invites: invites,
		pw:      passwordHasher,
		sealer:  sealer,
		totp:    totp,
		stepUp:  stepUp,
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

// GenerateInvite creates a single-use invite link for adminUserID. issuer says how the caller
// authenticated: a bearer-token caller must pass a step-up.
// Returns domain.ErrForbidden when the caller is not an admin, and domain.ErrStepUpRequired /
// domain.ErrStepUpFailed for a bearer without a valid step-up.
func (m *OnboardingManager) GenerateInvite(ctx context.Context, adminUserID uuid.UUID, issuer Issuer) (string, error) {
	admin, err := m.users.GetByID(ctx, adminUserID)
	if err != nil {
		return "", fmt.Errorf("get admin user: %w", err)
	}

	if !admin.IsAdmin {
		return "", domain.ErrForbidden
	}

	err = authorizeIssuer(ctx, m.stepUp, admin.ID, issuer)
	if err != nil {
		return "", err
	}

	tok, err := m.invites.Issue(ctx, adminUserID)
	if err != nil {
		return "", fmt.Errorf("issue invite: %w", err)
	}

	return tok, nil
}

// Enrollment is what a newly created account must show its owner exactly once: the
// machine-readable otpauth:// enrollment URI (headless provisioning prints this) and the
// browser-rendered QR code data URL. Both must be computed while the raw TOTP secret is still
// in scope — it is only stored sealed, so neither can be reconstructed afterwards.
type Enrollment struct {
	TOTPURI string
	QRCode  string
}

// AcceptInvite consumes the invite token, creates a new user with a fresh TOTP secret,
// and returns the enrollment material (otpauth URI + QR code) the user should scan. The
// consume and the user insert happen in a single transaction, so a failed registration
// never burns the token.
func (m *OnboardingManager) AcceptInvite(ctx context.Context, token, username, password string) (*Enrollment, error) {
	err := domain.ValidateUsername(username)
	if err != nil {
		return nil, fmt.Errorf("validate username: %w", err)
	}

	err = domain.ValidatePassword(password)
	if err != nil {
		return nil, fmt.Errorf("validate password: %w", err)
	}

	// Fast pre-check to reject an already-taken username before the expensive buildUser work
	// (argon2 hash + TOTP secret + QR). The authoritative guard is the unique constraint inside
	// RedeemInvite's transaction. Note: revealing "username taken" is an inherent UX trade-off
	// (the same signal is observable via RedeemInvite, which rolls back without burning the
	// invite on a collision); the pre-check does not widen that, it only avoids needless argon2.
	_, lookupErr := m.users.GetByUsername(ctx, username)
	if lookupErr == nil {
		return nil, domain.ErrUserExists
	}

	if !errors.Is(lookupErr, domain.ErrNotFound) {
		return nil, fmt.Errorf("check username: %w", lookupErr)
	}

	usr, enr, err := m.buildUser(username, password, false)
	if err != nil {
		return nil, err
	}

	err = m.invites.RedeemInvite(ctx, token, username, usr)
	if err != nil {
		return nil, fmt.Errorf("redeem invite: %w", err)
	}

	return enr, nil
}

// AcceptFirstAdmin creates the first admin user without requiring an invite token.
// Returns domain.ErrForbidden when users already exist (not empty DB).
//
// The List check is a best-effort gate; the hard guarantee against a concurrent
// double-bootstrap (e.g. two instances racing on an empty DB) is the partial
// unique index on users.is_admin: the second writer loses with domain.ErrUserExists.
func (m *OnboardingManager) AcceptFirstAdmin(ctx context.Context, username, password string) (*Enrollment, error) {
	err := domain.ValidateUsername(username)
	if err != nil {
		return nil, fmt.Errorf("validate username: %w", err)
	}

	err = domain.ValidatePassword(password)
	if err != nil {
		return nil, fmt.Errorf("validate password: %w", err)
	}

	users, err := m.users.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	if len(users) > 0 {
		return nil, domain.ErrForbidden
	}

	usr, enr, err := m.buildUser(username, password, true)
	if err != nil {
		return nil, err
	}

	err = m.users.Create(ctx, usr)
	if err != nil {
		return nil, fmt.Errorf("create first admin user: %w", err)
	}

	return enr, nil
}

// buildUser generates a fresh TOTP secret, seals it under a password-derived key, and
// assembles the user record together with its enrollment material (otpauth URI + QR code) —
// without touching the DB. The caller persists the returned user (directly or via
// RedeemInvite); generating enrollment here, before any insert, keeps the insert the last
// fallible step so it can be rolled back.
func (m *OnboardingManager) buildUser(username, password string, isAdmin bool) (*domain.User, *Enrollment, error) {
	rawSecret, err := m.totp.GenerateSecret()
	if err != nil {
		return nil, nil, fmt.Errorf("generate totp secret: %w", err)
	}

	kdfSalt, err := m.sealer.GenerateSalt()
	if err != nil {
		return nil, nil, fmt.Errorf("generate kdf salt: %w", err)
	}

	encSecret, err := m.sealer.Seal(rawSecret, []byte(password), kdfSalt)
	if err != nil {
		return nil, nil, fmt.Errorf("seal totp secret: %w", err)
	}

	pwHash, err := m.pw.Hash(password)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}

	usr := &domain.User{
		ID:           uuid.New(),
		Username:     username,
		TOTPSecret:   encSecret,
		PasswordHash: pwHash,
		KDFSalt:      kdfSalt,
		IsAdmin:      isAdmin,
		CreatedAt:    time.Now(),
		// LastLoginAt:    — no previous login on a freshly created account; nil pointer signals "never".
		// LastTOTPCounter: counter starts at 0; first TOTP code validates against this baseline.
		LastLoginAt:     nil,
		LastTOTPCounter: 0,
	}

	totpURI, err := m.totp.TOTPURI(m.issuer, username, rawSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("generate totp uri: %w", err)
	}

	qrURL, err := m.totp.GenerateQRCode(m.issuer, username, rawSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("generate qr code: %w", err)
	}

	return usr, &Enrollment{TOTPURI: totpURI, QRCode: qrURL}, nil
}
