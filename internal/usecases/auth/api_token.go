package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

const (
	// apiTokenBytes is the entropy of a freshly generated API key (256 bits).
	apiTokenBytes = 32
	// loginLinkTTL bounds how long a CLI login link remains usable. The link is a self-contained
	// HMAC-signed expiry carried in the URL; it is not persisted and not single-use (issuing a new
	// key on each confirmation is safe — keys are visible and revocable in the admin panel).
	loginLinkTTL = 10 * time.Minute
)

// APITokenInfo is the admin-facing projection of an API token: identifying and audit metadata
// without the hash or any material that could reconstruct the key.
type APITokenInfo struct {
	ID         string
	UserID     string
	Username   string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}

// WithAPITokens enables the CLI API-token flow on a Manager. linkSecret signs the time-limited
// /login/cli URL; it must be non-empty and constant across restarts (a rotating secret merely
// invalidates outstanding login links). Returns the same Manager for builder-style chaining.
func (m *Manager) WithAPITokens(store APITokenStore, linkSecret []byte) *Manager {
	m.apiTokens = store
	m.linkSecret = linkSecret

	return m
}

// CreateLoginLink issues a signed, time-limited token for the CLI login flow. It does NOT touch
// the database: the token is a self-contained HMAC over its expiry (and a random nonce), and the
// API key is created only when an authenticated browser session redeems the token on /login/cli
// (see ActivateAPIToken). Returns the opaque token and its TTL.
// Returns domain.ErrFeatureNotSupported when the API-token flow is not enabled.
func (m *Manager) CreateLoginLink(ctx context.Context) (string, time.Duration, error) {
	if m.apiTokens == nil {
		return "", 0, domain.ErrFeatureNotSupported
	}

	signed, err := m.signLoginLink(time.Now().Add(loginLinkTTL))
	if err != nil {
		return "", 0, err
	}

	return signed, loginLinkTTL, nil
}

// ActivateAPIToken verifies the signed login token and issues a long-lived API key for userID.
// The browser session (validated by the caller) supplies userID; the signed token supplies the
// expiry guarantee. The plain key is returned exactly once; only its SHA-256 hash is stored.
func (m *Manager) ActivateAPIToken(ctx context.Context, signedToken, userID string) (string, error) {
	if m.apiTokens == nil {
		return "", domain.ErrFeatureNotSupported
	}

	if err := m.verifyLoginLink(signedToken); err != nil {
		return "", err
	}

	usr, err := m.users.GetByID(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}

	plain, err := newAPIToken()
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256([]byte(plain))
	token := &domain.APIToken{
		ID:        uuid.New().String(),
		UserID:    usr.ID,
		TokenHash: base64.RawURLEncoding.EncodeToString(hash[:]),
		CreatedAt: time.Now(),
	}

	if err := m.apiTokens.Create(ctx, token); err != nil {
		return "", fmt.Errorf("create api token: %w", err)
	}

	return plain, nil
}

// ResolveAPIToken maps a bearer API key to its owning user, recording last-used.
// Returns domain.ErrNotFound when the key is unknown, revoked, or expired so the caller cannot
// distinguish those cases (no token enumeration oracle).
func (m *Manager) ResolveAPIToken(ctx context.Context, plainToken string) (*domain.User, error) {
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

	updateErr := m.apiTokens.UpdateLastUsed(ctx, token.ID, time.Now())
	if updateErr != nil {
		// Last-used tracking is advisory; a failed update must not reject a valid key.
		m.log.WarnContext(ctx, "update api token last used failed", "token_id", token.ID, "err", updateErr)
	}

	return usr, nil
}

// ListAPITokens returns all API tokens with owning usernames for the admin panel.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *Manager) ListAPITokens(ctx context.Context, adminUserID string) ([]*APITokenInfo, error) {
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
	for _, tk := range tokens {
		infos = append(infos, &APITokenInfo{
			ID:         tk.ID,
			UserID:     tk.UserID,
			Username:   usernames[tk.UserID],
			CreatedAt:  tk.CreatedAt,
			ExpiresAt:  tk.ExpiresAt,
			LastUsedAt: tk.LastUsedAt,
		})
	}

	return infos, nil
}

// RevokeAPIToken deletes an API token by its public ID.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *Manager) RevokeAPIToken(ctx context.Context, adminUserID, tokenID string) error {
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

	if err := m.apiTokens.RevokeByID(ctx, tokenID); err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}

	return nil
}

// signLoginLink builds a self-contained, HMAC-signed token carrying exp and a random nonce:
//
//	base64url(exp).base64url(nonce).base64url(hmac_sha256(secret, payload))
//
// The nonce distinguishes links issued within the same nanosecond and keeps the signature input
// non-trivially bound to randomness, but is not otherwise interpreted.
func (m *Manager) signLoginLink(exp time.Time) (string, error) {
	var expBuf [8]byte
	binary.BigEndian.PutUint64(expBuf[:], uint64(exp.UnixNano()))

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	payload := base64.RawURLEncoding.EncodeToString(expBuf[:]) + "." + base64.RawURLEncoding.EncodeToString(nonce)

	mac := hmac.New(sha256.New, m.linkSecret)
	_, _ = mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return payload + "." + sig, nil
}

// verifyLoginLink validates the structure, signature, and expiry of a signed login token.
func (m *Manager) verifyLoginLink(token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return domain.ErrLoginLinkInvalid
	}

	expB, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(expB) != 8 {
		return domain.ErrLoginLinkInvalid
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return domain.ErrLoginLinkInvalid
	}

	// payload covers exp and nonce; the nonce is validated only by the HMAC envelope.
	payload := parts[0] + "." + parts[1]

	mac := hmac.New(sha256.New, m.linkSecret)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return domain.ErrLoginLinkInvalid
	}

	exp := time.Unix(0, int64(binary.BigEndian.Uint64(expB)))
	if time.Now().After(exp) {
		return domain.ErrLoginLinkExpired
	}

	return nil
}

func newAPIToken() (string, error) {
	buf := make([]byte, apiTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
