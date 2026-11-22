package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/errorcontext"
)

type userRow struct {
	bun.BaseModel `bun:"table:users"`

	CreatedAt       time.Time  `bun:"created_at,notnull"`
	LastLoginAt     *time.Time `bun:"last_login_at"`
	Username        string     `bun:"username,notnull"`
	TOTPSecret      string     `bun:"totp_secret,notnull"`
	PasswordHash    string     `bun:"password_hash,notnull"`
	KDFSalt         string     `bun:"kdf_salt,notnull"`
	LastTOTPCounter int64      `bun:"last_totp_counter,notnull"`
	ID              uuid.UUID  `bun:"id,pk"`
	IsAdmin         bool       `bun:"is_admin,notnull"`
}

func toUserRow(usr *domain.User) *userRow {
	return &userRow{
		BaseModel:       bun.BaseModel{},
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
	return errorcontext.Wrap(insertUser(ctx, r.db, u), "insert user")
}

// insertUser inserts a user via conn (a *bun.DB or bun.Tx), mapping a unique-constraint
// violation to domain.ErrUserExists. Shared by Create and InviteRepository.RedeemInvite
// so both paths classify duplicate users identically inside or outside a transaction.
func insertUser(ctx context.Context, conn bun.IDB, u *domain.User) error {
	_, err := conn.NewInsert().Model(toUserRow(u)).Exec(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrUserExists
		}

		return fmt.Errorf("insert user: %w", err)
	}

	return nil
}

// GetByUsername fetches a user by username. Returns domain.ErrNotFound when absent.
func (r *UserRepository) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	var row userRow

	err := r.db.NewSelect().Model(&row).Where("lower(username) = lower(?)", username).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		return nil, fmt.Errorf("get user by username: %w", err)
	}

	return row.toDomain(), nil
}

// GetByID fetches a user by primary key. Returns domain.ErrNotFound when absent.
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
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
// Returns domain.ErrNotFound when the user does not exist.
func (r *UserRepository) UpdateLastLogin(ctx context.Context, id uuid.UUID, t time.Time) error {
	res, err := r.db.NewUpdate().
		TableExpr("users").
		Set("last_login_at = ?", t).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("update last login: %w", err)
	}

	return errorcontext.Wrap(requireAffected(res), "update last login")
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

// Revoke deletes the user record by ID. It never deletes an admin: idx_users_single_admin
// allows only one admin, so an admin is always the last admin, and its removal would leave the
// server without one. No operation sets is_admin on an existing user, so the condition in the
// DELETE is enough and no lock is necessary. Zero affected rows means the target does not exist
// or is the admin; both give domain.ErrForbidden. Callers check GetByID first to give ErrNotFound.
func (r *UserRepository) Revoke(ctx context.Context, id uuid.UUID) error {
	res, err := r.db.NewDelete().TableExpr("users").
		Where("id = ?", id).
		Where("is_admin = ?", false).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoke user: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke user rows affected: %w", err)
	}

	if affected == 0 {
		return fmt.Errorf("revoke user: %w", domain.ErrForbidden)
	}

	return nil
}

// UpdateTOTPCounter atomically advances last_totp_counter only when counter is
// strictly greater than the stored value. Returns true when the update applied
// (code accepted) and false when it was a replay (stored counter >= counter).
func (r *UserRepository) UpdateTOTPCounter(ctx context.Context, id uuid.UUID, counter int64) (bool, error) {
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

// ChangePassword replaces the credentials of sess.UserID, makes sess the only session of that user
// and deletes all API tokens of that user, in one transaction. A failure in any step rolls back
// all of them, so the old password and the old access stay valid and sess does not exist.
//
// The transaction first locks the user row with FOR UPDATE. The foreign key check of a session
// insert takes FOR KEY SHARE on the same row, and these two locks conflict. Thus a concurrent
// login stores its session before the lock, and the delete removes it, or after the commit, and
// its confirmCredentials check then sees the new hash. Without the lock, a session inserted
// after the delete and checked before the commit would keep the old password valid.
// Returns domain.ErrNotFound when the user does not exist.
func (r *UserRepository) ChangePassword(
	ctx context.Context, sess *domain.Session, passwordHash string, kdfSalt []byte, totpSecret string,
) error {
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, btx bun.Tx) error {
		var locked uuid.UUID

		lockErr := btx.NewSelect().
			TableExpr("users").
			Column("id").
			Where("id = ?", sess.UserID).
			For("UPDATE").
			Scan(ctx, &locked)
		if lockErr != nil {
			if errors.Is(lockErr, sql.ErrNoRows) {
				return domain.ErrNotFound
			}

			return fmt.Errorf("lock user: %w", lockErr)
		}

		_, updErr := btx.NewUpdate().
			TableExpr("users").
			Set("password_hash = ?", passwordHash).
			Set("kdf_salt = ?", base64.RawURLEncoding.EncodeToString(kdfSalt)).
			Set("totp_secret = ?", totpSecret).
			Where("id = ?", sess.UserID).
			Exec(ctx)
		if updErr != nil {
			return fmt.Errorf("update password: %w", updErr)
		}

		return replaceUserAccess(ctx, btx, sess)
	})
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}

	return nil
}

// replaceUserAccess deletes all sessions and API tokens of sess.UserID and stores sess, inside the
// transaction of ChangePassword.
func replaceUserAccess(ctx context.Context, btx bun.Tx, sess *domain.Session) error {
	_, err := btx.NewDelete().TableExpr("sessions").Where("user_id = ?", sess.UserID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete sessions of user: %w", err)
	}

	_, err = btx.NewInsert().Model(toSessionRow(sess)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}

	_, err = btx.NewDelete().TableExpr("api_tokens").Where("user_id = ?", sess.UserID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete api tokens of user: %w", err)
	}

	return nil
}

// UpdateTOTPSecret replaces only the sealed TOTP secret for the given user ID, and only while the
// stored seal still equals expected. The password hash and KDF salt are deliberately left alone:
// this is the re-seal-on-login migration path. The condition makes it a compare-and-set: after a
// concurrent password change the seal differs, so the old reseal reports false and writes nothing.
func (r *UserRepository) UpdateTOTPSecret(
	ctx context.Context, id uuid.UUID, expected, totpSecret string,
) (bool, error) {
	res, err := r.db.NewUpdate().
		TableExpr("users").
		Set("totp_secret = ?", totpSecret).
		Where("id = ?", id).
		Where("totp_secret = ?", expected).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("update totp secret: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}

	return affected > 0, nil
}

// requireAffected returns domain.ErrNotFound when res changed no row.
func requireAffected(res sql.Result) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if affected == 0 {
		return domain.ErrNotFound
	}

	return nil
}
