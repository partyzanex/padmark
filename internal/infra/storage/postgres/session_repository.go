package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

type sessionRow struct {
	bun.BaseModel `bun:"table:sessions"`

	CreatedAt time.Time `bun:"created_at,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
	SessionID string    `bun:"session_id,pk"`
	UserID    string    `bun:"user_id,notnull"`
	UserAgent string    `bun:"user_agent"`
	IP        string    `bun:"ip"`
}

func toSessionRow(sess *domain.Session) *sessionRow {
	return &sessionRow{
		SessionID: sess.SessionID,
		UserID:    sess.UserID,
		CreatedAt: sess.CreatedAt,
		ExpiresAt: sess.ExpiresAt,
		UserAgent: sess.UserAgent,
		IP:        sess.IP,
	}
}

func (r *sessionRow) toDomain() *domain.Session {
	return &domain.Session{
		SessionID: r.SessionID,
		UserID:    r.UserID,
		CreatedAt: r.CreatedAt,
		ExpiresAt: r.ExpiresAt,
		UserAgent: r.UserAgent,
		IP:        r.IP,
	}
}

// SessionRepository is a PostgreSQL-backed store for authenticated browser sessions.
type SessionRepository struct {
	db *bun.DB
}

// NewSessionRepository returns a new PostgreSQL-backed SessionRepository.
func NewSessionRepository(db *bun.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

// Create inserts a new session record.
func (r *SessionRepository) Create(ctx context.Context, s *domain.Session) error {
	_, err := r.db.NewInsert().Model(toSessionRow(s)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}

	return nil
}

// Get fetches a session by ID that has not yet expired.
// Returns domain.ErrSessionExpired for both unknown and expired sessions so that
// callers cannot distinguish the two cases (avoids session-existence oracle).
func (r *SessionRepository) Get(ctx context.Context, sessionID string) (*domain.Session, error) {
	var row sessionRow

	err := r.db.NewSelect().
		Model(&row).
		Where("session_id = ?", sessionID).
		Where("expires_at > ?", time.Now()).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrSessionExpired
		}

		return nil, fmt.Errorf("get session: %w", err)
	}

	return row.toDomain(), nil
}

// Delete removes a single session by ID (logout).
func (r *SessionRepository) Delete(ctx context.Context, sessionID string) error {
	_, err := r.db.NewDelete().
		TableExpr("sessions").
		Where("session_id = ?", sessionID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

// DeleteExpired removes all sessions whose expiry time is in the past.
func (r *SessionRepository) DeleteExpired(ctx context.Context) error {
	_, err := r.db.NewDelete().
		TableExpr("sessions").
		Where("expires_at < ?", time.Now()).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}

	return nil
}
