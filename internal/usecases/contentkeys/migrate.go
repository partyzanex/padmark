package contentkeys

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/partyzanex/padmark/internal/domain"
)

const maxSlugLine = 4096

func (m *Manager) replace(ctx context.Context, row Row, value string, report *Report) error {
	if value == row.Value {
		return nil
	}

	replaced, err := m.store.Replace(ctx, row, value)
	if err != nil {
		return encryptionError(err)
	}

	if replaced {
		report.Changed++
	} else {
		report.Conflicts++
	}

	return nil
}

// Migrate adds the installation-key envelope to every structurally valid raw legacy row.
// A failure leaves phase=pending; committed rows are checked and skipped on the next run.
func (m *Manager) Migrate(ctx context.Context, dryRun bool) (Report, error) {
	if dryRun {
		return m.Status(ctx)
	}

	report := newReport(Pending)
	// Existing corrupt raw records should keep a previously initialized DB pending.
	initErr := m.initialize(ctx)
	if initErr != nil {
		return m.failureReport(ctx, &report, initErr)
	}

	pendingErr := m.store.SetPhase(ctx, Pending)
	if pendingErr != nil {
		return report, encryptionError(pendingErr)
	}

	err := m.walk(ctx, func(row Row) error {
		if strings.Contains(row.Value, "$") {
			return nil
		}

		value, err := m.ciphers[row.Resource].WrapLegacy(row.Value, row.ID)
		if err != nil {
			return nil //nolint:nilerr // final validation counts corrupt rows without exposing data
		}

		return m.replace(ctx, row, value, &report)
	})
	if err != nil {
		return m.failureReport(ctx, &report, err)
	}

	final, err := m.Status(ctx)

	final.Changed, final.Conflicts = report.Changed, report.Conflicts
	if err != nil {
		return final, encryptionError(err)
	}

	if final.Raw != 0 {
		return final, errors.New("migration incomplete; retry after stopping concurrent writers")
	}

	readyErr := m.store.SetPhase(ctx, Ready)
	if readyErr != nil {
		return final, encryptionError(readyErr)
	}

	final.State = Ready

	return final, nil
}

// Rewrap only changes DEK wrappers. Interrupted rotations remain readable with both keys.
func (m *Manager) Rewrap(ctx context.Context, dryRun bool) (Report, error) {
	report, err := m.readyReport(ctx)
	if err != nil {
		return report, encryptionError(err)
	}

	if dryRun {
		return report, nil
	}

	err = m.walk(ctx, func(row Row) error {
		value, rewrapErr := m.ciphers[row.Resource].Rewrap(row.Value, row.ID)
		if rewrapErr != nil {
			return errors.New("rotation encountered an invalid envelope")
		}

		return m.replace(ctx, row, value, &report)
	})
	if err != nil {
		return m.failureReport(ctx, &report, err)
	}

	final, err := m.Status(ctx)

	final.Changed, final.Conflicts = report.Changed, report.Conflicts
	if err != nil {
		return final, encryptionError(err)
	}

	for id, count := range final.Keys {
		if id != m.keys.ActiveKey() && count != 0 {
			return final, errors.New("rotation incomplete; retry after stopping concurrent writers")
		}
	}

	return final, nil
}

// ConvertSlugs consumes one original slug per line. It never sends a slug or plaintext to logs.
// Both resource tables are checked; absent/deleted records are counted but never recreated.
func (m *Manager) ConvertSlugs(ctx context.Context, input io.Reader, dryRun bool) (Report, error) {
	report, err := m.readyReport(ctx)
	if err != nil {
		return report, encryptionError(err)
	}

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, maxSlugLine), maxSlugLine)

	for scanner.Scan() {
		slug := scanner.Text()
		if slug == "" {
			continue
		}

		err = m.convertSlug(ctx, slug, dryRun, &report)
		if err != nil {
			return report, encryptionError(err)
		}
	}

	if scanner.Err() != nil {
		return report, errors.New("could not read slug input (maximum line length 4096 bytes)")
	}

	final, err := m.Status(ctx)
	final.Changed, final.Conflicts, final.Missing = report.Changed, report.Conflicts, report.Missing

	final.Failed += report.Failed
	if err != nil {
		return final, encryptionError(err)
	}

	if final.Failed != 0 || final.Missing != 0 || final.Conflicts != 0 {
		return final, errors.New("some supplied records could not be converted; see counts")
	}

	return final, nil
}

func (m *Manager) convertSlug(ctx context.Context, slug string, dryRun bool, report *Report) error {
	found := false

	for _, resource := range []string{resourceNote, resourceLink} {
		row, err := m.store.Get(ctx, resource, domain.HashSlug(slug))
		if err != nil {
			return encryptionError(err)
		}

		if row == nil {
			continue
		}

		found = true
		cipher := m.ciphers[resource]

		kind, _, err := cipher.Inspect(row.Value, row.ID)
		if err != nil {
			report.Failed++

			continue
		}

		if kind == "p" {
			continue
		}

		plain, err := cipher.Decrypt(row.Value, row.ID, slug)
		if err != nil {
			report.Failed++

			continue
		}

		if dryRun {
			continue
		}

		value, err := cipher.Encrypt(plain, row.ID)
		if err != nil {
			return errors.New("could not encrypt converted record")
		}

		replaceErr := m.replace(ctx, *row, value, report)
		if replaceErr != nil {
			return encryptionError(replaceErr)
		}
	}

	if !found {
		report.Missing++
	}

	return nil
}

func (m *Manager) readyReport(ctx context.Context) (Report, error) {
	report, err := m.Status(ctx)
	if err != nil {
		return report, encryptionError(err)
	}

	reg, err := m.store.Registry(ctx)
	if err != nil {
		return report, encryptionError(err)
	}

	if reg == nil || report.State != Ready || report.Raw != 0 {
		return report, errors.New("complete content migration first")
	}

	err = m.validateKeys(reg, true)

	return report, encryptionError(err)
}

// Refresh counts after a partial failure when the caller's context still allows reading.
func (m *Manager) failureReport(ctx context.Context, partial *Report, cause error) (Report, error) {
	if ctx.Err() != nil {
		return *partial, encryptionError(cause)
	}

	current, statusErr := m.Status(ctx)
	current.Changed, current.Conflicts = partial.Changed, partial.Conflicts

	return current, errors.Join(cause, statusErr)
}
