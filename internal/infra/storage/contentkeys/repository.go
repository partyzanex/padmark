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

// Initialize atomically inserts state and checks, or leaves a winning registry untouched.
// The caller must reread and validate the registry after a concurrent initializer wins.
func (r *Repository) Initialize(ctx context.Context, reg *keys.Registry) error {
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, transaction bun.Tx) error {
		result, err := transaction.NewRaw("INSERT INTO content_encryption_state (id, installation_id, phase) "+
			"VALUES (1, ?, ?) ON CONFLICT (id) DO NOTHING",
			reg.InstallationID, reg.Phase).Exec(ctx)
		if err != nil {
			return keys.WrapError(err)
		}

		count, err := result.RowsAffected()
		if err != nil {
			return keys.WrapError(err)
		}

		if count == 0 {
			// Another initializer won. Do not mix our key checks into its registry.
			return nil
		}

		for id, check := range reg.Checks {
			_, err := transaction.NewRaw("INSERT INTO content_encryption_keys (key_id, verification) VALUES (?, ?)",
				id, check).Exec(ctx)
			if err != nil {
				return keys.WrapError(err)
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

// resourceNote is the only resource with v0.1.6 legacy rows and edit codes to inspect.
const resourceNote = "note"

func resourceColumns(resource string) (table, column string, err error) {
	switch resource {
	case resourceNote:
		return "notes", "content", nil
	case "link":
		return "links", "target_url", nil
	default:
		return "", "", errors.New("invalid encryption resource")
	}
}

// storedRow is one scanned record; LegacySlug and EditCode are read for notes only. LegacySlug
// stays empty unless a schema migration or MarkLegacy tagged the row.
type storedRow struct {
	ID         string `bun:"id"`
	Value      string `bun:"value"`
	LegacySlug string `bun:"legacy_slug"`
	EditCode   string `bun:"edit_code"`
}

func (s *storedRow) toRow(resource string) keys.Row {
	row := keys.Row{Resource: resource, ID: s.ID, Value: s.Value, Legacy: nil, StoredEditCode: ""}
	if s.LegacySlug != "" {
		row.Legacy = &keys.LegacyNote{Slug: s.LegacySlug, EditCode: s.EditCode}
	} else {
		row.StoredEditCode = s.EditCode
	}

	return row
}

// selectRows reads id and the encrypted column; for notes also the v0.1.6 legacy slug and the
// stored edit code: plaintext for a tagged row, and for an untagged row normally a hash, which
// the manager only checks for the hash shape (see contentkeys.EditCodeHasher.IsStoredHash).
func selectRows(db *bun.DB, table, column string) *bun.SelectQuery {
	query := db.NewSelect().Table(table).Column("id").ColumnExpr("? AS value", bun.Ident(column))
	if table == "notes" {
		query = query.ColumnExpr("COALESCE(legacy_slug, '') AS legacy_slug").Column("edit_code")
	}

	return query
}

func (r *Repository) Batch(ctx context.Context, resource, after string, limit int) ([]keys.Row, error) {
	table, column, err := resourceColumns(resource)
	if err != nil {
		return nil, err
	}

	var stored []storedRow

	err = selectRows(r.db, table, column).Where("id > ?", after).OrderExpr("id ASC").Limit(int64(limit)).
		Scan(ctx, &stored)
	if err != nil {
		return nil, fmt.Errorf("read encrypted records (apply schema migrations first): %w", err)
	}

	rows := make([]keys.Row, 0, len(stored))
	for i := range stored {
		rows = append(rows, stored[i].toRow(resource))
	}

	return rows, nil
}

func (r *Repository) Get(ctx context.Context, resource, id string) (*keys.Row, error) {
	table, column, err := resourceColumns(resource)
	if err != nil {
		return nil, err
	}

	var stored storedRow

	err = selectRows(r.db, table, column).Where("id = ?", id).Scan(ctx, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // absence is part of the migration storage contract
	}

	if err != nil {
		return nil, fmt.Errorf("read encrypted record: %w", err)
	}

	row := stored.toRow(resource)

	return &row, nil
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

// ConvertLegacy re-keys one tagged v0.1.6 note with a single compare-and-swap UPDATE: id,
// content and edit_code are replaced and legacy_slug cleared only while the row still holds
// the inspected id, content and legacy_slug. Errors never expose the slug, content, edit code
// or any SQL parameter.
func (r *Repository) ConvertLegacy(
	ctx context.Context, row keys.Row, newID, value, editCodeHash string,
) (bool, error) {
	if row.Resource != resourceNote || row.Legacy == nil {
		return false, errors.New("only tagged legacy notes can be converted")
	}

	result, err := r.db.NewUpdate().Table("notes").
		Set("id = ?", newID).Set("content = ?", value).Set("edit_code = ?", editCodeHash).Set("legacy_slug = NULL").
		Where("id = ? AND content = ? AND legacy_slug = ?", row.ID, row.Value, row.Legacy.Slug).Exec(ctx)
	if err != nil {
		return false, convertError(ctx, r.db, newID)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("could not read legacy note conversion count")
	}

	return count == 1, nil
}

// MarkLegacy tags one untagged note as v0.1.6 plaintext (legacy_slug = id) with a single
// compare-and-swap UPDATE: only while the row still holds the inspected id and content and has
// no tag. Errors never expose the slug, content or any SQL parameter.
func (r *Repository) MarkLegacy(ctx context.Context, row keys.Row) (bool, error) {
	if row.Resource != resourceNote || row.Legacy != nil {
		return false, errors.New("only untagged notes can be marked as legacy")
	}

	result, err := r.db.NewUpdate().Table("notes").Set("legacy_slug = id").
		Where("id = ? AND content = ? AND legacy_slug IS NULL", row.ID, row.Value).Exec(ctx)
	if err != nil {
		return false, errors.New("legacy note mark compare-and-swap failed")
	}

	count, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("could not read legacy note mark count")
	}

	return count == 1, nil
}

// convertError classifies a failed conversion without forwarding the driver error, which can
// quote SQL parameters. A row already stored under the target ID is reported as
// keys.ErrConvertTargetExists so the caller counts a conflict; it is never overwritten.
func convertError(ctx context.Context, db *bun.DB, newID string) error {
	exists, err := db.NewSelect().Table("notes").Where("id = ?", newID).Exists(ctx)
	if err == nil && exists {
		return keys.ErrConvertTargetExists
	}

	return errors.New("legacy note compare-and-swap failed")
}

// Delete removes exactly the invalid raw value inspected by the caller. A
// concurrent replacement or deletion counts as a conflict, never a reason to
// remove the new record. Errors never expose raw content or SQL parameters.
func (r *Repository) Delete(ctx context.Context, row keys.Row) (bool, error) {
	table, column, err := resourceColumns(row.Resource)
	if err != nil {
		return false, err
	}

	result, err := r.db.NewDelete().Table(table).
		Where("id = ? AND ? = ?", row.ID, bun.Ident(column), row.Value).Exec(ctx)
	if err != nil {
		return false, errors.New("corrupt raw compare-and-delete failed")
	}

	count, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("could not read corrupt raw deletion count")
	}

	return count == 1, nil
}
