// Package contentkeys implements the same migration protocol for SQLite and PostgreSQL via bun.
package contentkeys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	keys "github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

type Repository struct{ db *bun.DB }

func New(db *bun.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Registry(ctx context.Context) (*keys.Registry, error) {
	reg := &keys.Registry{InstallationID: "", Phase: "", Checks: make(map[string]string)}

	err := r.db.NewRaw("SELECT installation_id, phase FROM content_encryption_state WHERE id = 1").
		Scan(ctx, &reg.InstallationID, &reg.Phase)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // absence is part of the migration storage contract
	}

	if err != nil {
		return nil, fmt.Errorf("read encryption registry (apply schema migrations first): %w", err)
	}

	var checks []struct {
		KeyID        string
		Verification string
	}

	err = r.db.NewSelect().Table("content_encryption_keys").Column("key_id", "verification").Scan(ctx, &checks)
	if err != nil {
		return nil, fmt.Errorf("read key checks: %w", err)
	}

	for i := range checks {
		check := &checks[i]
		reg.Checks[check.KeyID] = check.Verification
	}

	return reg, nil
}

func (r *Repository) Initialize(ctx context.Context, reg *keys.Registry) error {
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, transaction bun.Tx) error {
		result, err := transaction.NewRaw("INSERT INTO content_encryption_state (id, installation_id, phase) "+
			"VALUES (1, ?, ?) ON CONFLICT (id) DO NOTHING",
			reg.InstallationID, reg.Phase).Exec(ctx)
		if err != nil {
			return encryptionError(err)
		}

		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return encryptionError(err)
		}

		for id, check := range reg.Checks {
			_, err := transaction.NewRaw("INSERT INTO content_encryption_keys (key_id, verification) VALUES (?, ?)",
				id, check).Exec(ctx)
			if err != nil {
				return encryptionError(err)
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("initialize encryption registry: %w", err)
	}

	return nil
}

func (r *Repository) Register(ctx context.Context, id, check string) error {
	_, err := r.db.NewRaw("INSERT INTO content_encryption_keys (key_id, verification) "+
		"VALUES (?, ?) ON CONFLICT (key_id) DO NOTHING", id, check).Exec(ctx)
	if err != nil {
		return fmt.Errorf("register content key: %w", err)
	}

	return nil
}

func (r *Repository) SetPhase(ctx context.Context, phase string) error {
	result, err := r.db.NewUpdate().Table("content_encryption_state").Set("phase = ?", phase).Where("id = 1").Exec(ctx)
	if err != nil {
		return fmt.Errorf("set encryption state: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read encryption state update count: %w", err)
	}

	if count != 1 {
		return errors.New("encryption registry is absent")
	}

	return nil
}

func resourceColumns(resource string) (table, column string, err error) {
	switch resource {
	case "note":
		return "notes", "content", nil
	case "link":
		return "links", "target_url", nil
	default:
		return "", "", errors.New("invalid encryption resource")
	}
}

type storedRow struct{ ID, Value string }

func (r *Repository) Batch(ctx context.Context, resource, after string, limit int) ([]keys.Row, error) {
	table, column, err := resourceColumns(resource)
	if err != nil {
		return nil, err
	}

	var stored []storedRow

	err = r.db.NewSelect().Table(table).Column("id").ColumnExpr("? AS value", bun.Ident(column)).
		Where("id > ?", after).OrderExpr("id ASC").Limit(limit).Scan(ctx, &stored)
	if err != nil {
		return nil, fmt.Errorf("read encrypted records: %w", err)
	}

	rows := make([]keys.Row, 0, len(stored))
	for i := range stored {
		row := &stored[i]
		rows = append(rows, keys.Row{Resource: resource, ID: row.ID, Value: row.Value})
	}

	return rows, nil
}

func (r *Repository) Get(ctx context.Context, resource, id string) (*keys.Row, error) {
	table, column, err := resourceColumns(resource)
	if err != nil {
		return nil, err
	}

	var value string

	err = r.db.NewSelect().Table(table).Column(column).Where("id = ?", id).Scan(ctx, &value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // absence is part of the migration storage contract
	}

	if err != nil {
		return nil, fmt.Errorf("read encrypted record: %w", err)
	}

	return &keys.Row{Resource: resource, ID: id, Value: value}, nil
}

func (r *Repository) Replace(ctx context.Context, row keys.Row, value string) (bool, error) {
	table, column, err := resourceColumns(row.Resource)
	if err != nil {
		return false, err
	}

	result, err := r.db.NewUpdate().Table(table).Set("? = ?", bun.Ident(column), value).
		Where("id = ? AND ? = ?", row.ID, bun.Ident(column), row.Value).Exec(ctx)
	// Driver errors can contain SQL parameters; never forward an error containing ciphertext.
	if err != nil {
		return false, errors.New("ciphertext compare-and-swap failed")
	}

	count, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("could not read ciphertext update count")
	}

	return count == 1, nil
}
