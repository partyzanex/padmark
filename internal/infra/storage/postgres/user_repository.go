package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
)

type userRow struct {
	bun.BaseModel `bun:"table:users"`

	CreatedAt       time.Time  `bun:"created_at,notnull"`
	LastLoginAt     *time.Time `bun:"last_login_at"`
	ID              string     `bun:"id,pk"`
	Username        string     `bun:"username,notnull"`
	TOTPSecret      string     `bun:"totp_secret,notnull"`
	PasswordHash    string     `bun:"password_hash,notnull"`
	KDFSalt         string     `bun:"kdf_salt,notnull"`
	LastTOTPCounter int64      `bun:"last_totp_counter,notnull"`
	IsAdmin         bool       `bun:"is_admin,notnull"`
}

func toUserRow(usr *domain.User) *userRow {
	return &userRow{
		ID:              usr.ID,
		Username:        usr.Username,
		TOTPSecret:      usr.TOTPSecret,
		PasswordHash:    usr.PasswordHash,
		KDFSalt:         base64.RawURLEncoding.EncodeToString(usr.KDFSalt),
		IsAdmin:         usr.IsAdmin,
		LastTOTPCounter: usr.LastTOTPCounter,
		CreatedAt:       usr.CreatedAt,
		LastLoginAt:     usr.LastLoginAt,
	}
}

func (r *userRow) toDomain() *domain.User {
	var kdfSalt []byte

	raw, decErr := base64.RawURLEncoding.DecodeString(r.KDFSalt)
	if decErr == nil {
		kdfSalt = raw
	}

	return &domain.User{
		ID:              r.ID,
		Username:        r.Username,
		TOTPSecret:      r.TOTPSecret,
		PasswordHash:    r.PasswordHash,
		KDFSalt:         kdfSalt,
		IsAdmin:         r.IsAdmin,
		LastTOTPCounter: r.LastTOTPCounter,
		CreatedAt:       r.CreatedAt,
		LastLoginAt:     r.LastLoginAt,
	}
}

// UserRepository is a PostgreSQL-backed store for registered users.
type UserRepository struct {
	db *bun.DB
}

// NewUserRepository returns a new PostgreSQL-backed UserRepository.
func NewUserRepository(db *bun.DB) *UserRepository {
	return &UserRepository{db: db}
}

// Create inserts a new user. Returns domain.ErrUserExists on duplicate username.
func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	return insertUser(ctx, r.db, u)
}

// insertUser inserts a user via conn (a *bun.DB or bun.Tx), mapping a unique-constraint
// violation to domain.ErrUserExists. Shared by Create and InviteRepository.RedeemInvite
// so both paths classify duplicate users identically inside or outside a transaction.
func insertUser(ctx context.Context, conn bun.IDB, u *domain.User) error {
	_, err := conn.NewInsert().Model(toUserRow(u)).Exec(ctx)
	if err != nil {
		var pgErr pgdriver.Error
		if errors.As(err, &pgErr) && pgErr.Field('C') == "23505" {
			return domain.ErrUserExists
		}

		return fmt.Errorf("insert user: %w", err)
	}

	return nil
}

// GetByUsername fetches a user by username. Returns domain.ErrNotFound when absent.
func (r *UserRepository) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	var row userRow

	err := r.db.NewSelect().Model(&row).Where("username = ?", username).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("get user by username: %w", err)
	}

	return row.toDomain(), nil
}

// GetByID fetches a user by primary key. Returns domain.ErrNotFound when absent.
func (r *UserRepository) GetByID(ctx context.Context, id string) (*domain.User, error) {
	var row userRow

	err := r.db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("get user by id: %w", err)
	}

	return row.toDomain(), nil
}

// UpdateLastLogin sets last_login_at for the given user ID.
func (r *UserRepository) UpdateLastLogin(ctx context.Context, id string, t time.Time) error {
	_, err := r.db.NewUpdate().
		TableExpr("users").
		Set("last_login_at = ?", t).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("update last login: %w", err)
	}

	return nil
}

// List returns all registered users ordered by created_at ascending.
func (r *UserRepository) List(ctx context.Context) ([]*domain.User, error) {
	var rows []userRow

	err := r.db.NewSelect().Model(&rows).OrderExpr("created_at ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	users := make([]*domain.User, len(rows))
	for i := range rows {
		users[i] = rows[i].toDomain()
	}

	return users, nil
}

// Revoke deletes the user record by ID.
func (r *UserRepository) Revoke(ctx context.Context, id string) error {
	_, err := r.db.NewDelete().TableExpr("users").Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoke user: %w", err)
	}

	return nil
}

// UpdateTOTPCounter atomically advances last_totp_counter only when counter is
// strictly greater than the stored value. Returns true when the update applied
// (code accepted) and false when it was a replay (stored counter >= counter).
func (r *UserRepository) UpdateTOTPCounter(ctx context.Context, id string, counter int64) (bool, error) {
	res, err := r.db.NewUpdate().
		TableExpr("users").
		Set("last_totp_counter = ?", counter).
		Where("id = ?", id).
		Where("last_totp_counter < ?", counter).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("update totp counter: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}

	return affected > 0, nil
}

// UpdatePassword atomically replaces password_hash, kdf_salt, and totp_secret.
// kdfSalt is raw bytes; the repository encodes it as base64url before storage.
func (r *UserRepository) UpdatePassword(
	ctx context.Context, id, passwordHash string, kdfSalt []byte, totpSecret string,
) error {
	_, err := r.db.NewUpdate().
		TableExpr("users").
		Set("password_hash = ?", passwordHash).
		Set("kdf_salt = ?", base64.RawURLEncoding.EncodeToString(kdfSalt)).
		Set("totp_secret = ?", totpSecret).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	return nil
}
