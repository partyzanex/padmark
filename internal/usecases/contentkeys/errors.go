package contentkeys

import (
	"errors"
	"fmt"
)

const errorContext = "content encryption"

// ErrConvertTargetExists reports that a legacy note's hashed ID is already taken by another
// note. The storage never overwrites that row; the legacy row stays for manual resolution.
var ErrConvertTargetExists = errors.New("converted legacy note ID already exists")

var errInvalidRecords = errors.New("encrypted records failed validation; check keyring and database integrity")

// invalidRecordsError suggests prune only when every failure is malformed raw data;
// prune refuses to run while any versioned envelope fails, so recommending it then
// would send the operator in a circle.
func invalidRecordsError(report *Report) error {
	if report.Failed > report.CorruptRaw {
		return fmt.Errorf("%w; versioned envelopes failed authentication (missing or wrong key, "+
			"tampering, or an unsupported format version): restore the matching keyring or a database backup; "+
			"encryption prune never deletes versioned records and refuses to run until they authenticate",
			errInvalidRecords)
	}

	if report.CorruptRaw != 0 {
		return fmt.Errorf("%w; malformed raw records require explicit cleanup: preview with "+
			"encryption prune --corrupt --dry-run, then encryption prune --corrupt and rerun encryption migrate",
			errInvalidRecords)
	}

	return errInvalidRecords
}

// Never confuse I/O, registry, or envelope authentication failures with malformed
// raw data. Only the latter may be bypassed during initialization or pruning.
func onlyCorruptRaw(report *Report, err error) bool {
	return errors.Is(err, errInvalidRecords) && report.CorruptRaw != 0 && report.Failed == report.CorruptRaw
}

// contextError carries the shared "content encryption" prefix exactly once.
type contextError struct{ err error }

func (e *contextError) Error() string { return errorContext + ": " + e.err.Error() }

func (e *contextError) Unwrap() error { return e.err }

// WrapError adds context while preserving successful nil results and error identity.
// An error that already carries the context anywhere in its chain is returned
// unchanged, so nested calls never repeat the prefix.
func WrapError(err error) error {
	if err == nil {
		return nil
	}

	target, ok := errors.AsType[*contextError](err)
	if ok && target != nil {
		return err
	}

	return &contextError{err: err}
}

// unwrapContext strips a top-level context so a joined error is prefixed only once.
func unwrapContext(err error) error {
	if wrapped, ok := err.(*contextError); ok { //nolint:errorlint // strip only the outermost context
		return wrapped.err
	}

	return err
}
