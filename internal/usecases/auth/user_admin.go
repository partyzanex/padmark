//go:generate go run go.uber.org/mock/mockgen@latest -source=user_admin.go -destination=user_admin_mocks_test.go -package=auth -write_package_comment=false

package auth

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// UserAdminStore is the part of the user store that UserAdminManager uses. Fail-hard.
type UserAdminStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	List(ctx context.Context) ([]*domain.User, error)
	// Revoke deletes the user record by ID. The delete itself refuses to remove an admin and
	// returns domain.ErrForbidden in that case or when the target does not exist. Callers check
	// GetByID first to give ErrNotFound.
	Revoke(ctx context.Context, id uuid.UUID) error
}

// UserAdminManager lists and revokes user accounts for the admin panel.
type UserAdminManager struct {
	users UserAdminStore
}

// NewUserAdminManager returns a new UserAdminManager.
func NewUserAdminManager(users UserAdminStore) *UserAdminManager {
	return &UserAdminManager{users: users}
}

// ListUsers returns all registered users for the admin panel.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *UserAdminManager) ListUsers(ctx context.Context, adminUserID uuid.UUID) ([]*domain.User, error) {
	admin, err := m.users.GetByID(ctx, adminUserID)
	if err != nil {
		return nil, fmt.Errorf("get admin user: %w", err)
	}

	if !admin.IsAdmin {
		return nil, domain.ErrForbidden
	}

	users, err := m.users.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	return users, nil
}

// RevokeUser removes a user. Returns domain.ErrForbidden when:
//   - the caller is not an admin
//   - the caller tries to revoke themselves
//   - the target is an admin
//
// The schema allows only one admin (idx_users_single_admin), so an admin target is always the
// last admin, and its removal would open the bootstrap hole. UserAdminStore.Revoke refuses it too.
func (m *UserAdminManager) RevokeUser(ctx context.Context, adminUserID, targetUserID uuid.UUID) error {
	if adminUserID == targetUserID {
		return domain.ErrForbidden
	}

	admin, err := m.users.GetByID(ctx, adminUserID)
	if err != nil {
		return fmt.Errorf("get admin user: %w", err)
	}

	if !admin.IsAdmin {
		return domain.ErrForbidden
	}

	target, err := m.users.GetByID(ctx, targetUserID)
	if err != nil {
		return fmt.Errorf("get target user: %w", err)
	}

	if target.IsAdmin {
		return domain.ErrForbidden
	}

	err = m.users.Revoke(ctx, targetUserID)
	if err != nil {
		return fmt.Errorf("revoke user: %w", err)
	}

	return nil
}
