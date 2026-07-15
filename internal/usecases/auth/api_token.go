package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	// apiTokenBytes is the entropy of a freshly generated API key (256 bits).
	apiTokenBytes = 32
	// maxAPITokensPerUser caps how many live API keys a single user may hold, so an admin
	// (or a compromised admin session) cannot mint an unbounded number of bearer credentials.
	maxAPITokensPerUser = 20
)

// APITokenInfo is the admin-facing projection of an API token: identifying and audit metadata
// without the hash or any material that could reconstruct the key.
type APITokenInfo struct {
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	ID         string
	UserID     string
	Username   string
}

// APITokenManager issues, resolves, lists, and revokes long-lived API tokens.
//
// apiTokens may be nil to disable the flow entirely: every method then returns
// domain.ErrFeatureNotSupported instead of touching a store.
type APITokenManager struct {
	apiTokens APITokenStore
	users     UserStore
	log       *slog.Logger
}

// NewAPITokenManager returns a new APITokenManager. Pass a nil apiTokens to disable the
// API-token flow (all methods then return domain.ErrFeatureNotSupported).
func NewAPITokenManager(apiTokens APITokenStore, users UserStore, log *slog.Logger) *APITokenManager {
	return &APITokenManager{apiTokens: apiTokens, users: users, log: log}
}

// CreateAPIToken issues a long-lived API key for the user identified by userID. The plain key is
// returned exactly once; only its SHA-256 hash is stored. The caller is responsible for any
// authorisation check (admin gate, target-user gate) — this method authenticates the *issuance*
// flow, not the resulting bearer.
// Returns domain.ErrFeatureNotSupported when the API-token flow is not enabled on the Manager, and
// domain.ErrAPITokenLimit when userID already holds maxAPITokensPerUser keys.
func (m *APITokenManager) CreateAPIToken(ctx context.Context, userID string) (string, error) {
	if m.apiTokens == nil {
		return "", domain.ErrFeatureNotSupported
	}

	usr, err := m.users.GetByID(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}

	count, err := m.apiTokens.CountByUser(ctx, usr.ID)
	if err != nil {
		return "", fmt.Errorf("count api tokens: %w", err)
	}

	if count >= maxAPITokensPerUser {
		return "", domain.ErrAPITokenLimit
	}

	plain, err := newAPIToken()
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256([]byte(plain))
	token := &domain.APIToken{
		UserID:    usr.ID,
		TokenHash: base64.RawURLEncoding.EncodeToString(hash[:]),
		CreatedAt: time.Now(),
	}

	err = m.apiTokens.Create(ctx, token)
	if err != nil {
		return "", fmt.Errorf("create api token: %w", err)
	}

	return plain, nil
}

// ResolveAPIToken maps a bearer API key to its owning user, recording last-used.
// Returns domain.ErrNotFound when the key is unknown, revoked, or expired so the caller cannot
// distinguish those cases (no token enumeration oracle).
func (m *APITokenManager) ResolveAPIToken(ctx context.Context, plainToken string) (*domain.User, error) {
	if m.apiTokens == nil {
		return nil, domain.ErrFeatureNotSupported
	}

	hash := sha256.Sum256([]byte(plainToken))

	token, err := m.apiTokens.GetByHash(ctx, base64.RawURLEncoding.EncodeToString(hash[:]))
	if err != nil {
		return nil, err //nolint:wrapcheck // domain.ErrNotFound sentinel passes through for errors.Is
	}

	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, domain.ErrNotFound
	}

	usr, err := m.users.GetByID(ctx, token.UserID)
	if err != nil {
		return nil, fmt.Errorf("get token user: %w", err)
	}

	updateErr := m.apiTokens.UpdateLastUsed(ctx, token.TokenHash, time.Now())
	if updateErr != nil {
		// Last-used tracking is advisory; a failed update must not reject a valid key.
		m.log.WarnContext(ctx, "update api token last used failed", "token_hash", token.TokenHash, "err", updateErr)
	}

	return usr, nil
}

// ListAPITokens returns all API tokens with owning usernames for the admin panel.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *APITokenManager) ListAPITokens(ctx context.Context, adminUserID string) ([]*APITokenInfo, error) {
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

	usernames := make(map[string]string, len(users))
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
		})
	}

	return infos, nil
}

// RevokeAPIToken deletes an API token by its public ID.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *APITokenManager) RevokeAPIToken(ctx context.Context, adminUserID, tokenID string) error {
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

func newAPIToken() (string, error) {
	buf := make([]byte, apiTokenBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
