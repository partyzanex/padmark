package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

type apiTokenRow struct {
	bun.BaseModel `bun:"table:api_tokens"`

	CreatedAt  time.Time  `bun:"created_at,notnull"`
	ExpiresAt  *time.Time `bun:"expires_at"`
	LastUsedAt *time.Time `bun:"last_used_at"`
	TokenHash  string     `bun:"token_hash,pk"`
	UserID     uuid.UUID  `bun:"user_id,notnull"`
}

func toAPITokenRow(token *domain.APIToken) *apiTokenRow {
	return &apiTokenRow{
		UserID:     token.UserID,
		TokenHash:  token.TokenHash,
		CreatedAt:  token.CreatedAt,
		ExpiresAt:  token.ExpiresAt,
		LastUsedAt: token.LastUsedAt,
	}
}

func (r *apiTokenRow) toDomain() *domain.APIToken {
	return &domain.APIToken{
		UserID:     r.UserID,
		TokenHash:  r.TokenHash,
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
		LastUsedAt: r.LastUsedAt,
	}
}

// APITokenRepository is a SQLite-backed store for CLI API tokens keyed by their SHA-256 hash.
type APITokenRepository struct {
	db *bun.DB
}

// NewAPITokenRepository returns a new SQLite-backed APITokenRepository.
func NewAPITokenRepository(db *bun.DB) *APITokenRepository {
	return &APITokenRepository{db: db}
}

// CreateIfUnderLimit persists token only if token.UserID currently holds fewer than limit
// tokens, atomically — SQLite is single-writer, so the count-then-insert inside a transaction is
// race-free against a concurrent CreateIfUnderLimit call for the same user. Returns false (no
// error) when the limit is already reached; the plain key is never stored either way.
func (r *APITokenRepository) CreateIfUnderLimit(ctx context.Context, token *domain.APIToken, limit int) (bool, error) {
	var created bool

	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, btx bun.Tx) error {
		count, countErr := btx.NewSelect().
			Model((*apiTokenRow)(nil)).
			Where("user_id = ?", token.UserID).
			Count(ctx)
		if countErr != nil {
			return fmt.Errorf("count api tokens: %w", countErr)
		}

		if count >= limit {
			return nil
		}

		_, insErr := btx.NewInsert().Model(toAPITokenRow(token)).Exec(ctx)
		if insErr != nil {
			return fmt.Errorf("insert api token: %w", insErr)
		}

		created = true

		return nil
	})
	if err != nil {
		return false, fmt.Errorf("create api token if under limit: %w", err)
	}

	return created, nil
}

// GetByHash resolves a token by its SHA-256 hash. Returns domain.ErrNotFound when absent.
func (r *APITokenRepository) GetByHash(ctx context.Context, tokenHash string) (*domain.APIToken, error) {
	var row apiTokenRow

	err := r.db.NewSelect().
		Model(&row).
		Where("token_hash = ?", tokenHash).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("get api token: %w", err)
	}

	return row.toDomain(), nil
}

// List returns all tokens, newest first, for the admin panel.
func (r *APITokenRepository) List(ctx context.Context) ([]*domain.APIToken, error) {
	var rows []apiTokenRow

	err := r.db.NewSelect().
		Model(&rows).
		Order("created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}

	tokens := make([]*domain.APIToken, 0, len(rows))
	for i := range rows {
		tokens = append(tokens, rows[i].toDomain())
	}

	return tokens, nil
}

// RevokeByHash deletes a token by its SHA-256 hash. Returns domain.ErrNotFound when absent.
func (r *APITokenRepository) RevokeByHash(ctx context.Context, tokenHash string) error {
	res, err := r.db.NewDelete().
		TableExpr("api_tokens").
		Where("token_hash = ?", tokenHash).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke api token: rows affected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// UpdateLastUsed records the time of the most recent successful token check, keyed by hash.
// A missing row is not an error: last-used tracking is advisory and a token revoked between
// the Resolve and the Update must not turn a successful auth into a failure.
func (r *APITokenRepository) UpdateLastUsed(ctx context.Context, tokenHash string, t time.Time) error {
	_, err := r.db.NewUpdate().
		Model((*apiTokenRow)(nil)).
		Set("last_used_at = ?", t).
		Where("token_hash = ?", tokenHash).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("update api token last used: %w", err)
	}

	return nil
}
