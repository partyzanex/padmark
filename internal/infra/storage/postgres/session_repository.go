package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
)

type sessionRow struct {
	bun.BaseModel `bun:"table:sessions"`

	CreatedAt   time.Time `bun:"created_at,notnull"`
	ExpiresAt   time.Time `bun:"expires_at,notnull"`
	SessionHash string    `bun:"session_hash,pk"`
	UserAgent   string    `bun:"user_agent"`
	IP          string    `bun:"ip"`
	UserID      uuid.UUID `bun:"user_id,notnull"`
}

func toSessionRow(sess *domain.Session) *sessionRow {
	return &sessionRow{
		BaseModel:   bun.BaseModel{},
		SessionHash: sess.SessionHash,
		UserID:      sess.UserID,
		CreatedAt:   sess.CreatedAt,
		ExpiresAt:   sess.ExpiresAt,
		UserAgent:   sess.UserAgent,
		IP:          sess.IP,
	}
}

func (r *sessionRow) toDomain() *domain.Session {
	return &domain.Session{
		SessionHash: r.SessionHash,
		UserID:      r.UserID,
		CreatedAt:   r.CreatedAt,
		ExpiresAt:   r.ExpiresAt,
		UserAgent:   r.UserAgent,
		IP:          r.IP,
	}
}

// SessionRepository is a PostgreSQL-backed store for authenticated browser sessions.
type SessionRepository struct {
	db  *bun.DB
	log *slog.Logger
}

// NewSessionRepository returns a new PostgreSQL-backed SessionRepository. log is used to surface
// lazy-sweep failures.
func NewSessionRepository(db *bun.DB, log *slog.Logger) *SessionRepository {
	return &SessionRepository{db: db, log: log}
}

// Create inserts a new session record. Expired sessions are lazily swept on each call
// (mirroring the invite/reveal-token pattern) since there is no background sweep worker.
func (r *SessionRepository) Create(ctx context.Context, s *domain.Session) error {
	_, err := r.db.NewInsert().Model(toSessionRow(s)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}

	// Best-effort sweep of expired sessions; a failure here does not block session creation —
	// stale rows simply persist until the next Create call.
	sweepErr := r.DeleteExpired(ctx)
	if sweepErr != nil {
		r.log.WarnContext(ctx, "session sweep failed", "err", sweepErr)
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
		Where("session_hash = ?", sessionID).
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
		Where("session_hash = ?", sessionID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

// DeleteByUserIDExcept removes all sessions for the user except the specified one.
func (r *SessionRepository) DeleteByUserIDExcept(ctx context.Context, userID uuid.UUID, exceptSessionID string) error {
	_, err := r.db.NewDelete().
		TableExpr("sessions").
		Where("user_id = ?", userID).
		Where("session_hash != ?", exceptSessionID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete sessions by user except: %w", err)
	}

	return nil
}

// DeleteByUserID removes all sessions belonging to the given user.
func (r *SessionRepository) DeleteByUserID(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.NewDelete().
		TableExpr("sessions").
		Where("user_id = ?", userID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete sessions by user: %w", err)
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
