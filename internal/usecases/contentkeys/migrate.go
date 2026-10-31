package contentkeys

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/errorcontext"
)

const maxSlugLine = 4096

// MigrateIfNeeded prepares startup automatically. Ready is persisted only after every row
// validates; restarts skip writes, and interrupted migrations resume the remaining raw rows.
func (m *Manager) MigrateIfNeeded(ctx context.Context) (Report, error) {
	err := m.initialize(ctx)
	if err != nil {
		return newReport(Pending), Wrap(err)
	}

	report, err := m.Status(ctx)
	if err != nil {
		return report, Wrap(err)
	}

	if report.State == Ready && report.Raw == 0 && report.Plaintext == 0 {
		return report, nil
	}

	operationResult, operationErr := m.Migrate(ctx)
	if operationErr != nil {
		return operationResult, fmt.Errorf("migrate: %w", operationErr)
	}

	return operationResult, nil
}

func (m *Manager) replace(ctx context.Context, row Row, value string, report *Report) error {
	if value == row.Value {
		return nil
	}

	replaced, err := m.store.Replace(ctx, row, value)
	if err != nil {
		return Wrap(err)
	}

	if replaced {
		report.Changed++
	} else {
		report.Conflicts++
	}

	return nil
}

// Migrate adds the installation-key envelope to every structurally valid raw legacy row and
// converts every tagged v0.1.6 plaintext note in place (see convertLegacy).
// A failure leaves phase=pending; committed rows are checked and skipped on the next run.
// Status is the dry run of Migrate: it reports the same counts without changes.
func (m *Manager) Migrate(ctx context.Context) (Report, error) {
	report := newReport(Pending)
	// Existing corrupt raw records should keep a previously initialized DB pending.
	initErr := m.initialize(ctx)
	if initErr != nil {
		operationResult, operationErr := m.failureReport(ctx, &report, initErr)
		if operationErr != nil {
			return operationResult, fmt.Errorf("failure report: %w", operationErr)
		}

		return operationResult, nil
	}

	pendingErr := m.store.SetPhase(ctx, Pending)
	if pendingErr != nil {
		return report, Wrap(pendingErr)
	}

	err := m.walk(ctx, func(row Row) error { return errorcontext.Wrap(m.migrateRow(ctx, &row, &report), "migrate row") })
	if err != nil {
		operationResult, operationErr := m.failureReport(ctx, &report, err)
		if operationErr != nil {
			return operationResult, fmt.Errorf("failure report: %w", operationErr)
		}

		return operationResult, nil
	}

	final, err := m.Status(ctx)

	final.Changed, final.Conflicts = report.Changed, report.Conflicts
	if err != nil {
		return final, Wrap(err)
	}

	if final.Raw != 0 || final.Plaintext != 0 {
		return final, errMigrationIncomplete
	}

	readyErr := m.store.SetPhase(ctx, Ready)
	if readyErr != nil {
		return final, Wrap(readyErr)
	}

	final.State = Ready

	return final, nil
}

// migrateRow converts a tagged v0.1.6 note or wraps one structurally valid raw legacy value.
func (m *Manager) migrateRow(ctx context.Context, row *Row, report *Report) error {
	if row.Legacy != nil {
		return errorcontext.Wrap(m.convertLegacy(ctx, row, report), "convert legacy")
	}

	// An untagged v0.1.6 note is plaintext: wrapping it as legacy ciphertext would make it
	// unreadable. The final Status reports it until MarkLegacy tags it.
	cipher := m.ciphers[row.Resource]
	if cipher.Versioned(row.Value) || m.untaggedLegacy(row) {
		return nil
	}

	value, err := cipher.WrapLegacy(row.Value, row.ID)
	if err != nil {
		return nil //nolint:nilerr // final validation counts corrupt rows without exposing data
	}

	return errorcontext.Wrap(m.replace(ctx, *row, value, report), "replace")
}

// PreviewRewrap is the dry run of Rewrap: it reports the current counts and fails when Rewrap
// would refuse to start.
func (m *Manager) PreviewRewrap(ctx context.Context) (Report, error) {
	report, err := m.readyReport(ctx)

	return report, Wrap(err)
}

// Rewrap only changes DEK wrappers. Interrupted rotations remain readable with both keys.
func (m *Manager) Rewrap(ctx context.Context) (Report, error) {
	report, err := m.readyReport(ctx)
	if err != nil {
		return report, Wrap(err)
	}

	err = m.walk(ctx, func(row Row) error {
		value, rewrapErr := m.ciphers[row.Resource].Rewrap(row.Value, row.ID)
		if rewrapErr != nil {
			return errInvalidRotationEnvelope
		}

		return errorcontext.Wrap(m.replace(ctx, row, value, &report), "replace")
	})
	if err != nil {
		operationResult, operationErr := m.failureReport(ctx, &report, err)
		if operationErr != nil {
			return operationResult, fmt.Errorf("failure report: %w", operationErr)
		}

		return operationResult, nil
	}

	final, err := m.Status(ctx)

	final.Changed, final.Conflicts = report.Changed, report.Conflicts
	if err != nil {
		return final, Wrap(err)
	}

	for id, count := range final.Keys {
		if id != m.keys.ActiveKey() && count != 0 {
			return final, errRotationIncomplete
		}
	}

	return final, nil
}

// ConvertSlugs consumes one original slug per line. It never sends a slug or plaintext to logs.
// Both resource tables are checked; absent/deleted records are counted but never recreated.
func (m *Manager) ConvertSlugs(ctx context.Context, input io.Reader, dryRun bool) (Report, error) {
	report, err := m.readyReport(ctx)
	if err != nil {
		return report, Wrap(err)
	}

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, maxSlugLine), maxSlugLine)
	conversionFailures := make(map[string]struct{})
	run := &slugConversion{report: &report, failures: conversionFailures, dryRun: dryRun}

	for scanner.Scan() {
		slug := scanner.Text()
		if slug == "" {
			continue
		}

		err = m.convertSlug(ctx, slug, run)
		if err != nil {
			return report, Wrap(err)
		}
	}

	if scanner.Err() != nil {
		return report, errReadSlugInput
	}

	final, err := m.statusWithFailures(ctx, conversionFailures)
	final.Changed, final.Conflicts, final.Missing = report.Changed, report.Conflicts, report.Missing

	// Only failures not already represented by Status remain (e.g. an inner legacy
	// ciphertext that authenticates structurally but cannot decrypt with its slug).
	final.Failed += len(conversionFailures)
	if err != nil {
		return final, Wrap(err)
	}

	if final.Failed != 0 || final.Missing != 0 || final.Conflicts != 0 {
		return final, errConversionIncomplete
	}

	return final, nil
}

// slugConversion is the state of one ConvertSlugs run: the report it fills, the records that
// failed conversion and whether it only checks the records.
type slugConversion struct {
	report   *Report
	failures map[string]struct{}
	dryRun   bool
}

func (m *Manager) convertSlug(ctx context.Context, slug string, run *slugConversion) error {
	found := false

	for _, resource := range []string{resourceNote, resourceLink} {
		row, err := m.store.Get(ctx, resource, domain.HashSlug(slug))
		if err != nil {
			return Wrap(err)
		}

		if row == nil {
			continue
		}

		found = true
		cipher := m.ciphers[resource]

		kind, err := inspectVersioned(cipher, row)
		if err != nil {
			recordFailure(run.failures, run.report, row)

			continue
		}

		if kind == "p" {
			continue
		}

		plain, err := cipher.Decrypt(row.Value, row.ID, slug)
		if err != nil {
			recordFailure(run.failures, run.report, row)

			continue
		}

		if run.dryRun {
			continue
		}

		value, err := cipher.Encrypt(plain, row.ID)
		if err != nil {
			return errEncryptConvertedRecord
		}

		replaceErr := m.replace(ctx, *row, value, run.report)
		if replaceErr != nil {
			return Wrap(replaceErr)
		}
	}

	if !found {
		run.report.Missing++
	}

	return nil
}

// inspectVersioned authenticates versioned envelopes only. Unversioned values cannot be
// converted; the final scan reports them as raw or malformed raw records.
func inspectVersioned(cipher Cipher, row *Row) (string, error) {
	if !cipher.Versioned(row.Value) {
		return "", errRecordNotVersioned
	}

	kind, _, err := cipher.Inspect(row.Value, row.ID)
	if err != nil {
		return "", fmt.Errorf("inspect envelope: %w", err)
	}

	return kind, nil
}

// recordFailure counts each failed row once, however often its slug is supplied.
func recordFailure(failures map[string]struct{}, report *Report, row *Row) {
	failures[row.Resource+"/"+row.ID] = struct{}{}
	report.Failed = len(failures)
}

func (m *Manager) readyReport(ctx context.Context) (Report, error) {
	report, err := m.Status(ctx)
	if err != nil {
		return report, Wrap(err)
	}

	reg, err := m.store.Registry(ctx)
	if err != nil {
		return report, Wrap(err)
	}

	if reg == nil || report.State != Ready || report.Raw != 0 || report.Plaintext != 0 {
		return report, errMigrationNotComplete
	}

	err = m.validateActiveKeys(reg)

	return report, Wrap(err)
}

// Refresh counts after a partial failure when the caller's context still allows reading.
func (m *Manager) failureReport(ctx context.Context, partial *Report, cause error) (Report, error) {
	if ctx.Err() != nil {
		return *partial, Wrap(cause)
	}

	current, statusErr := m.Status(ctx)
	current.Changed, current.Conflicts = partial.Changed, partial.Conflicts
	current.Pruned = partial.Pruned

	return current, Wrap(errors.Join(unwrapContext(cause), unwrapContext(statusErr)))
}
