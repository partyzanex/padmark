package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	// apiTokenBytes is the entropy of a freshly generated API key (256 bits).
	apiTokenBytes = 32
	// maxAPITokensPerUser caps how many live API keys a single user may hold, so an admin
	// (or a compromised admin session) cannot mint an unbounded number of bearer credentials.
	maxAPITokensPerUser = 20
	// MaxAPITokenTTLDays bounds the lifetime an issuer may choose (ten years); 0 means "never
	// expires", an explicit choice of the issuer.
	MaxAPITokenTTLDays = 3650
)

// APITokenInfo is the admin-facing projection of an API token: identifying and audit metadata
// without the hash or any material that could reconstruct the key.
type APITokenInfo struct {
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	// IssuedBy is the ID of the API token whose bearer issued this one; nil for a browser session.
	IssuedBy *string
	ID       string
	Username string
	UserID   uuid.UUID
}

// APITokenManager issues, resolves, lists, and revokes long-lived API tokens.
//
// apiTokens may be nil to disable the flow entirely: every method then returns
// domain.ErrFeatureNotSupported instead of touching a store.
type APITokenManager struct {
	apiTokens APITokenStore
	users     UserStore
	stepUp    StepUpVerifier
	log       *slog.Logger
}

// NewAPITokenManager returns a new APITokenManager. Pass a nil apiTokens to disable the
// API-token flow (all methods then return domain.ErrFeatureNotSupported). stepUp re-authenticates
// a bearer-token caller before it may issue another token.
func NewAPITokenManager(
	apiTokens APITokenStore, users UserStore, stepUp StepUpVerifier, log *slog.Logger,
) *APITokenManager {
	return &APITokenManager{apiTokens: apiTokens, users: users, stepUp: stepUp, log: log}
}

// CreateAPIToken issues an API key for the user identified by userID that expires after ttlDays
// days (0 = never, the issuer's explicit choice). The plain key is returned exactly once; only its
// SHA-256 hash is stored. issuer says how the caller authenticated: a bearer-token caller must
// pass a step-up, and the new key records that token as its issuer, so revoking it revokes this
// one too. The caller is responsible for any other authorisation check (target-user gate).
// Returns domain.ErrFeatureNotSupported when the API-token flow is not enabled on the Manager,
// domain.ErrInvalidAPITokenTTL for ttlDays outside 0…MaxAPITokenTTLDays,
// domain.ErrStepUpRequired / domain.ErrStepUpFailed for a bearer without a valid step-up, and
// domain.ErrAPITokenLimit when userID already holds maxAPITokensPerUser keys.
func (m *APITokenManager) CreateAPIToken(
	ctx context.Context, userID uuid.UUID, issuer Issuer, ttlDays int,
) (string, error) {
	if m.apiTokens == nil {
		return "", domain.ErrFeatureNotSupported
	}

	if ttlDays < 0 || ttlDays > MaxAPITokenTTLDays {
		return "", domain.ErrInvalidAPITokenTTL
	}

	usr, err := m.users.GetByID(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}

	err = authorizeIssuer(ctx, m.stepUp, usr.ID, issuer)
	if err != nil {
		return "", err
	}

	plain, err := newAPIToken()
	if err != nil {
		return "", err
	}

	now := time.Now()

	token := &domain.APIToken{
		UserID:    usr.ID,
		TokenHash: hashToken(plain),
		CreatedAt: now,
		// nil = never expires (ttlDays 0).
		ExpiresAt: apiTokenExpiry(now, ttlDays),
		// LastUsedAt: unused until the first authenticated request resolves the token.
		LastUsedAt: nil,
		IssuedBy:   apiTokenIssuedBy(issuer),
	}

	ok, err := m.apiTokens.CreateIfUnderLimit(ctx, token, maxAPITokensPerUser)
	if err != nil {
		return "", fmt.Errorf("create api token: %w", err)
	}

	if !ok {
		return "", domain.ErrAPITokenLimit
	}

	return plain, nil
}

// ResolveAPIToken maps a bearer API key to its owning user and the key's public ID (its hash,
// which a token issued with this bearer records as IssuedBy), recording last-used.
// Returns domain.ErrNotFound when the key is unknown, revoked, or expired so the caller cannot
// distinguish those cases (no token enumeration oracle).
func (m *APITokenManager) ResolveAPIToken(ctx context.Context, plainToken string) (*domain.User, string, error) {
	if m.apiTokens == nil {
		return nil, "", domain.ErrFeatureNotSupported
	}

	token, err := m.apiTokens.GetByHash(ctx, hashToken(plainToken))
	if err != nil {
		return nil, "", fmt.Errorf("get api token by hash: %w", err)
	}

	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, "", domain.ErrNotFound
	}

	usr, err := m.users.GetByID(ctx, token.UserID)
	if err != nil {
		return nil, "", fmt.Errorf("get token user: %w", err)
	}

	updateErr := m.apiTokens.UpdateLastUsed(ctx, token.TokenHash, time.Now())
	if updateErr != nil {
		// Last-used tracking is advisory; a failed update must not reject a valid key.
		m.log.WarnContext(ctx, "update api token last used failed", "token_hash", token.TokenHash, "err", updateErr)
	}

	return usr, token.TokenHash, nil
}

// ListAPITokens returns all API tokens with owning usernames for the admin panel.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *APITokenManager) ListAPITokens(ctx context.Context, adminUserID uuid.UUID) ([]*APITokenInfo, error) {
	if m.apiTokens == nil {
		return nil, domain.ErrFeatureNotSupported
	}

	admin, err := m.users.GetByID(ctx, adminUserID)
	if err != nil {
		return nil, fmt.Errorf("get admin user: %w", err)
	}

	if !admin.IsAdmin {
		return nil, domain.ErrForbidden
	}

	tokens, err := m.apiTokens.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}

	// Resolve usernames from a single user-list pass to avoid N+1 lookups.
	users, err := m.users.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	usernames := make(map[uuid.UUID]string, len(users))
	for _, usr := range users {
		usernames[usr.ID] = usr.Username
	}

	infos := make([]*APITokenInfo, 0, len(tokens))
	for _, token := range tokens {
		infos = append(infos, &APITokenInfo{
			ID:         token.TokenHash,
			UserID:     token.UserID,
			Username:   usernames[token.UserID],
			CreatedAt:  token.CreatedAt,
			ExpiresAt:  token.ExpiresAt,
			LastUsedAt: token.LastUsedAt,
			IssuedBy:   token.IssuedBy,
		})
	}

	return infos, nil
}

// RevokeAPIToken deletes an API token by its public ID, and with it (store cascade along
// IssuedBy) every token issued with it.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *APITokenManager) RevokeAPIToken(ctx context.Context, adminUserID uuid.UUID, tokenID string) error {
	if m.apiTokens == nil {
		return domain.ErrFeatureNotSupported
	}

	admin, err := m.users.GetByID(ctx, adminUserID)
	if err != nil {
		return fmt.Errorf("get admin user: %w", err)
	}

	if !admin.IsAdmin {
		return domain.ErrForbidden
	}

	err = m.apiTokens.RevokeByHash(ctx, tokenID)
	if err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}

	return nil
}

// apiTokenExpiry is now + ttlDays days, or nil (never expires) for ttlDays 0.
func apiTokenExpiry(now time.Time, ttlDays int) *time.Time {
	if ttlDays == 0 {
		return nil
	}

	expires := now.AddDate(0, 0, ttlDays)

	return &expires
}

// apiTokenIssuedBy is the issuing bearer token's hash, or nil for a browser session.
func apiTokenIssuedBy(issuer Issuer) *string {
	if issuer.APITokenHash == "" {
		return nil
	}

	issuedBy := issuer.APITokenHash

	return &issuedBy
}

func newAPIToken() (string, error) {
	buf := make([]byte, apiTokenBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken derives the at-rest primary key for a plaintext bearer credential: the SHA-256
// digest base64url-encoded. APIToken.TokenHash and Session.SessionHash share this scheme, so
// the raw credential value is never persisted; it lives only in the client cookie.
func hashToken(plain string) string {
	hash := sha256.Sum256([]byte(plain))

	return base64.RawURLEncoding.EncodeToString(hash[:])
}
