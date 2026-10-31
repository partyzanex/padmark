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
	"github.com/uptrace/bun/driver/pgdriver"

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
func (r *UserRepository) UpdateLastLogin(ctx context.Context, id uuid.UUID, t time.Time) error {
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

// revokeAdminLockKey keys the transaction-scoped advisory lock guarding the last-admin
// invariant in Revoke.
const revokeAdminLockKey = "padmark:user-revoke"

// Revoke deletes the user record by ID. The delete refuses to remove the last remaining admin.
// The EXISTS guard lives in the DELETE, but under PostgreSQL's READ COMMITTED a conditional
// statement alone is not race-free: two concurrent transactions each evaluate the subquery
// before the other commits its delete (the statement re-check only covers the deleted row, not
// the subquery). The same advisory-lock pattern as APITokenRepository.CreateIfUnderLimit
// (auto-released at commit/rollback) serializes revocations cross-instance, so the guard always
// sees the state after the previously committed one. Zero affected rows means the target does
// not exist or is the last admin — both map to domain.ErrForbidden; callers pre-check GetByID
// for a friendlier ErrNotFound.
func (r *UserRepository) Revoke(ctx context.Context, id uuid.UUID) error {
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, btx bun.Tx) error {
		_, lockErr := btx.NewRaw("SELECT pg_advisory_xact_lock(hashtext(?))", revokeAdminLockKey).Exec(ctx)
		if lockErr != nil {
			return fmt.Errorf("acquire advisory lock: %w", lockErr)
		}

		res, execErr := btx.NewDelete().TableExpr("users").
			Where("id = ?", id).
			Where("is_admin = FALSE OR EXISTS (SELECT 1 FROM users AS u WHERE u.is_admin AND u.id != ?)", id).
			Exec(ctx)
		if execErr != nil {
			return fmt.Errorf("revoke user: %w", execErr)
		}

		affected, affectedErr := res.RowsAffected()
		if affectedErr != nil {
			return fmt.Errorf("revoke user rows affected: %w", affectedErr)
		}

		if affected == 0 {
			return domain.ErrForbidden
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("revoke user: %w", err)
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

// UpdatePassword atomically replaces password_hash, kdf_salt, and totp_secret.
// kdfSalt is raw bytes; the repository encodes it as base64url before storage.
func (r *UserRepository) UpdatePassword(
	ctx context.Context, id uuid.UUID, passwordHash string, kdfSalt []byte, totpSecret string,
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

// UpdateTOTPSecret replaces only the sealed TOTP secret for the given user ID. The password hash
// and KDF salt are deliberately left alone: this is the re-seal-on-login migration path, where
// the secret is rewritten under the current argon2 cost while the credentials stay as they are.
func (r *UserRepository) UpdateTOTPSecret(ctx context.Context, id uuid.UUID, totpSecret string) error {
	_, err := r.db.NewUpdate().
		TableExpr("users").
		Set("totp_secret = ?", totpSecret).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("update totp secret: %w", err)
	}

	return nil
}
