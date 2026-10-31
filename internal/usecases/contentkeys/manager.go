// Package contentkeys manages encrypted records without invoking read/burn lifecycle operations.
package contentkeys

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/errorcontext"
)

// The package godoc lives here, so the generated mocks must not add a second one.
//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=contentkeys -write_package_comment=false

// Migration phases of the encryption state; batchSize bounds the records read per batch.
const (
	Pending   = "pending"
	Ready     = "ready"
	batchSize = 32
)

const (
	resourceNote = "note"
	resourceLink = "link"
)

// Row is one stored record as the migration protocol sees it. Legacy is set only for notes
// tagged by the legacy_notes schema migrations or MarkLegacy; it is nil for every other note
// and for all links. StoredEditCode is the edit_code of an untagged note (a hash, unless the
// row is an untagged v0.1.6 note) and empty for links and tagged notes. Never log or wrap any
// field but Resource.
type Row struct {
	Legacy         *LegacyNote
	Resource       string
	ID             string
	Value          string
	StoredEditCode string
}

// LegacyNote carries the plaintext columns of a note written by v0.1.6 or earlier, whose ID
// is the slug itself and whose content and edit code were stored unencrypted and unhashed.
type LegacyNote struct {
	Slug     string
	EditCode string
}

// Registry is the stored encryption state: the installation ID, the migration phase and the
// verification value of each registered key.
type Registry struct {
	Checks         map[string]string
	InstallationID string
	Phase          string
}

// Storage uses compare-and-swap for ciphertext updates and explicit corrupt-raw
// deletion. It must never upsert a record.
type Storage interface {
	Registry(ctx context.Context) (*Registry, error)
	Initialize(ctx context.Context, registry *Registry) error
	Register(ctx context.Context, keyID, check string) error
	SetPhase(ctx context.Context, phase string) error
	Batch(ctx context.Context, resource, after string, limit int) ([]Row, error)
	Get(ctx context.Context, resource, id string) (*Row, error)
	Replace(ctx context.Context, row Row, value string) (bool, error)
	Delete(ctx context.Context, row Row) (bool, error)
	// ConvertLegacy rewrites one tagged plaintext note in a single compare-and-swap: it sets
	// id, content and edit_code and clears legacy_slug only while the row still holds the
	// inspected id, content and legacy_slug. false means a concurrent change won. A target ID
	// that already exists is reported as ErrConvertTargetExists, never by replacing that row.
	ConvertLegacy(ctx context.Context, row Row, newID, value, editCodeHash string) (bool, error)
	// MarkLegacy tags one untagged note for conversion (legacy_slug = id) only while the row
	// still holds the inspected id and content and has no tag. false means a concurrent change won.
	MarkLegacy(ctx context.Context, row Row) (bool, error)
}

// EditCodeHasher hashes plaintext edit codes of converted legacy notes. Hash is the fast
// format for server-generated high-entropy codes; HashUserProvided is the memory-hard format
// for caller-chosen codes whose entropy is unknown. IsStoredHash reports whether a stored
// value has the exact shape of such a hash: every release since v0.2.0 stores only hashes, so
// an untagged note whose edit code is not hash-shaped can only be a v0.1.6 plaintext note.
type EditCodeHasher interface {
	Hash(code string) (string, error)
	HashUserProvided(code string) (string, error)
	IsStoredHash(value string) bool
}

// Keyring exposes the content key IDs and their verification values. KeyCheck and
// VerifyKeyCheck errors are fail-hard: a key that cannot be verified must not encrypt content.
type Keyring interface {
	ActiveKey() string
	KeyIDs() []string
	KeyCheck(installationID, keyID string) (string, error)
	VerifyKeyCheck(installationID, keyID, value string) error
}

// Cipher authenticates and converts stored values of one resource type.
// Versioned is a purely syntactic classification shared by every operation: true
// means the value carries an envelope version tag and is never malformed raw data,
// even when it fails authentication or names an unknown future version.
type Cipher interface {
	Versioned(value string) bool
	Inspect(value, recordID string) (string, string, error)
	ValidateLegacy(value string) error
	WrapLegacy(value, recordID string) (string, error)
	Rewrap(value, recordID string) (string, error)
	Decrypt(value, recordID, legacySlug string) (string, error)
	Encrypt(plain, recordID string) (string, error)
}

// Report contains counts and public key IDs; never content or key bytes. Only a prune dry run
// fills Candidates with record IDs, so the operator can review them before any deletion; for an
// untagged v0.1.6 note the ID is its slug.
type Report struct {
	Keys           map[string]int `json:"keys"`
	State          string         `json:"state"`
	Candidates     []Candidate    `json:"candidates,omitempty"`
	Raw            int            `json:"raw"`
	Wrapped        int            `json:"wrapped"`
	Native         int            `json:"native"`
	Failed         int            `json:"failed"`
	CorruptRaw     int            `json:"corrupt_raw"`
	UntaggedLegacy int            `json:"untagged_legacy"`
	Plaintext      int            `json:"plaintext"`
	Pruned         int            `json:"pruned"`
	Changed        int            `json:"changed"`
	Conflicts      int            `json:"conflicts"`
	Missing        int            `json:"missing"`
}

// Candidate is one record a prune dry run reports: Reason is "corrupt_raw" (prune would delete
// it) or "untagged_legacy" (a v0.1.6 note that prune never deletes; tag it with MarkLegacy).
type Candidate struct {
	Resource string `json:"resource"`
	ID       string `json:"id"`
	Reason   string `json:"reason"`
}

// Manager migrates, rewraps, converts and prunes encrypted records without the read and burn
// lifecycle of notes and links.
type Manager struct {
	store   Storage
	keys    Keyring
	hasher  EditCodeHasher
	ciphers map[string]Cipher
}

// New returns a Manager with one cipher for notes and one for links.
func New(store Storage, keys Keyring, notes, links Cipher, hasher EditCodeHasher) *Manager {
	return &Manager{
		store: store, keys: keys, hasher: hasher,
		ciphers: map[string]Cipher{resourceNote: notes, resourceLink: links},
	}
}

// Status authenticates envelopes and reports the remaining legacy layers without writing.
func (m *Manager) Status(ctx context.Context) (Report, error) {
	operationResult, operationErr := m.status(ctx, nil)
	if operationErr != nil {
		return operationResult, fmt.Errorf("status: %w", operationErr)
	}

	return operationResult, nil
}

// PrepareServe validates readiness before listeners, lifecycle work or key activation.
func (m *Manager) PrepareServe(ctx context.Context) error {
	initErr := m.initialize(ctx)
	if initErr != nil {
		return Wrap(initErr)
	}

	report, err := m.Status(ctx)
	if err != nil {
		return Wrap(err)
	}

	if report.State != Ready || report.Raw != 0 || report.Plaintext != 0 {
		return ErrMigrationRequired
	}

	return nil
}

// Register explicitly registers one new KEK. Existing checks can never be overwritten.
func (m *Manager) Register(ctx context.Context, keyID string) error {
	reg, err := m.store.Registry(ctx)
	if err != nil {
		return Wrap(err)
	}

	if reg == nil {
		return errMigrationNotInitialized
	}

	validationErr := m.validateKeys(reg, false)
	if validationErr != nil {
		return Wrap(validationErr)
	}

	_, statusErr := m.Status(ctx)
	if statusErr != nil {
		return Wrap(statusErr)
	}

	check, err := m.keys.KeyCheck(reg.InstallationID, keyID)
	if err != nil {
		return Wrap(err)
	}

	if _, exists := reg.Checks[keyID]; exists {
		return nil
	}

	registerErr := m.store.Register(ctx, keyID, check)
	if registerErr != nil {
		return Wrap(registerErr)
	}

	reg, err = m.store.Registry(ctx)
	if err != nil {
		return Wrap(err)
	}

	return Wrap(m.keys.VerifyKeyCheck(reg.InstallationID, keyID, reg.Checks[keyID]))
}

// CanRemove checks live references; historical registry checks remain to prevent ID reuse.
// All writers must be stopped until replicas have installed the resulting keyring.
func (m *Manager) CanRemove(ctx context.Context, keyID string) error {
	if keyID == m.keys.ActiveKey() {
		return errCannotRemoveActiveKey
	}

	prepareErr := m.PrepareServe(ctx)
	if prepareErr != nil {
		return Wrap(prepareErr)
	}

	report, err := m.Status(ctx)
	if err != nil {
		return Wrap(err)
	}

	if report.Keys[keyID] != 0 {
		return errKeyStillReferenced
	}

	return nil
}

func newReport(state string) Report {
	var report Report

	report.State = state
	report.Keys = make(map[string]int)

	return report
}

func (m *Manager) walk(ctx context.Context, visit func(Row) error) error {
	for _, resource := range []string{resourceNote, resourceLink} {
		after := ""
		for {
			rows, err := m.store.Batch(ctx, resource, after, batchSize)
			if err != nil {
				return fmt.Errorf("scan encrypted records: %w", err)
			}

			if len(rows) == 0 {
				break
			}

			for i := range rows {
				row := &rows[i]

				err := ctx.Err()
				if err != nil {
					return Wrap(err)
				}

				err = visit(*row)
				if err != nil {
					return Wrap(err)
				}
			}

			after = rows[len(rows)-1].ID
		}
	}

	return nil
}

func (m *Manager) validateKeys(reg *Registry, requireActive bool) error {
	if reg == nil {
		return nil
	}

	if reg.Phase != Ready && reg.Phase != Pending {
		return errInvalidMigrationState
	}

	_, parseErr := uuid.Parse(reg.InstallationID)
	if parseErr != nil {
		return errInvalidInstallationID
	}

	if requireActive && reg.Checks[m.keys.ActiveKey()] == "" {
		return errActiveKeyNotRegistered
	}

	for _, id := range m.keys.KeyIDs() {
		if check, found := reg.Checks[id]; found {
			err := m.keys.VerifyKeyCheck(reg.InstallationID, id, check)
			if err != nil {
				return Wrap(err)
			}
		}
	}

	return nil
}

// inspect classifies a row: notes tagged as v0.1.6 plaintext are Plaintext whatever their
// content looks like (it may resemble an envelope or base64 ciphertext); an untagged note with
// an unversioned value and an edit code that is not hash-shaped is an untagged v0.1.6 note
// (UntaggedLegacy, also Failed, never pruned); other unversioned values are raw legacy
// ciphertext (Raw) or malformed raw data (CorruptRaw, also Failed); versioned values that do
// not authenticate are Failed only and are never eligible for pruning.
func (m *Manager) inspect(row Row, reg *Registry, report *Report) {
	if row.Legacy != nil {
		report.Plaintext++

		return
	}

	cipher := m.ciphers[row.Resource]
	if !cipher.Versioned(row.Value) {
		if m.untaggedLegacy(&row) {
			report.Failed++
			report.UntaggedLegacy++

			return
		}

		if cipher.ValidateLegacy(row.Value) != nil {
			report.Failed++
			report.CorruptRaw++

			return
		}

		report.Raw++

		return
	}

	kind, id, err := cipher.Inspect(row.Value, row.ID)
	if err != nil {
		report.Failed++

		return
	}

	if reg != nil {
		check, found := reg.Checks[id]
		if !found || m.keys.VerifyKeyCheck(reg.InstallationID, id, check) != nil {
			report.Failed++

			return
		}
	}

	report.Keys[id]++
	if kind == "l" {
		report.Wrapped++
	} else {
		report.Native++
	}
}

// untaggedLegacy reports whether an unversioned row is a v0.1.6 plaintext note that no schema
// migration tagged: only notes carry edit codes, and every release since v0.2.0 stores them
// hashed, so a note whose edit code is not hash-shaped cannot be raw legacy ciphertext. Such a
// row must never be wrapped as ciphertext or pruned as malformed data.
func (m *Manager) untaggedLegacy(row *Row) bool {
	return row.Resource == resourceNote && row.Legacy == nil && !m.hasher.IsStoredHash(row.StoredEditCode)
}

// initialize authenticates existing envelopes before binding this DB to the supplied keys.
// Initialize must atomically insert state plus checks, or leave an existing registry untouched.
// Malformed raw records may be bound only in pending state for explicit cleanup, and v0.1.6
// plaintext notes only in pending state for conversion (they are not failures);
// envelope, key and storage failures still prevent first-time initialization.
func (m *Manager) initialize(ctx context.Context) error {
	reg, err := m.store.Registry(ctx)
	if err != nil {
		return Wrap(err)
	}

	if reg == nil {
		err := m.initializeRegistry(ctx)
		if err != nil {
			return Wrap(err)
		}

		reg, err = m.store.Registry(ctx)
		if err != nil {
			return Wrap(err)
		}
	}

	return errorcontext.Wrap(m.validateKeys(reg, true), "validate keys")
}

func (m *Manager) initializeRegistry(ctx context.Context) error {
	report, err := m.Status(ctx)
	if err != nil && !onlyCorruptRaw(&report, err) {
		return Wrap(err)
	}

	reg := &Registry{InstallationID: uuid.NewString(), Phase: Ready, Checks: make(map[string]string)}
	if report.Raw != 0 || report.Failed != 0 || report.Plaintext != 0 {
		reg.Phase = Pending
	}

	for _, id := range m.keys.KeyIDs() {
		check, err := m.keys.KeyCheck(reg.InstallationID, id)
		if err != nil {
			return Wrap(err)
		}

		reg.Checks[id] = check
	}

	return Wrap(m.store.Initialize(ctx, reg))
}

// status removes conversion failures already counted by the final storage scan.
func (m *Manager) status(ctx context.Context, conversionFailures map[string]struct{}) (Report, error) {
	reg, err := m.store.Registry(ctx)

	report := newReport("uninitialized")
	if err != nil {
		return report, Wrap(err)
	}

	validationErr := m.validateKeys(reg, false)
	if validationErr != nil {
		return report, Wrap(validationErr)
	}

	if reg != nil {
		report.State = reg.Phase
	}

	err = m.walk(ctx, func(row Row) error {
		before := report.Failed
		m.inspect(row, reg, &report)

		if report.Failed > before {
			delete(conversionFailures, row.Resource+"/"+row.ID)
		}

		return nil
	})
	if err != nil {
		return report, Wrap(err)
	}

	if report.Failed != 0 {
		validationErr := invalidRecordsError(&report)

		return report, fmt.Errorf("validate encrypted records: %w", validationErr)
	}

	return report, nil
}
