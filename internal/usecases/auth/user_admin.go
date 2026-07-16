package auth

import (
	"context"
	"fmt"

	"github.com/partyzanex/padmark/internal/domain"
)

// UserAdminManager lists and revokes user accounts for the admin panel.
type UserAdminManager struct {
	users UserStore
}

// NewUserAdminManager returns a new UserAdminManager.
func NewUserAdminManager(users UserStore) *UserAdminManager {
	return &UserAdminManager{users: users}
}

// ListUsers returns all registered users for the admin panel.
// Returns domain.ErrForbidden when the caller is not an admin.
func (m *UserAdminManager) ListUsers(ctx context.Context, adminUserID string) ([]*domain.User, error) {
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
//   - the target is the last admin (would open the bootstrap hole)
func (m *UserAdminManager) RevokeUser(ctx context.Context, adminUserID, targetUserID string) error {
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
		all, listErr := m.users.List(ctx)
		if listErr != nil {
			return fmt.Errorf("list users: %w", listErr)
		}

		remainingAdmins := 0

		for _, u := range all {
			if u.IsAdmin && u.ID != targetUserID {
				remainingAdmins++
			}
		}

		if remainingAdmins == 0 {
			return domain.ErrForbidden
		}
	}

	err = m.users.Revoke(ctx, targetUserID)
	if err != nil {
		return fmt.Errorf("revoke user: %w", err)
	}

	return nil
}
