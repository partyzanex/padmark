// Package contentkeys manages encrypted records without invoking read/burn lifecycle operations.
package contentkeys

// The package godoc lives here, so the generated mocks must not add a second one.
//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=contentkeys -write_package_comment=false

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

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
// tagged by the legacy_notes schema migration; it is nil for every other note and for all
// links. Never log or wrap any field but Resource.
type Row struct {
	Legacy   *LegacyNote
	Resource string
	ID       string
	Value    string
}

// LegacyNote carries the plaintext columns of a note written by v0.1.6 or earlier, whose ID
// is the slug itself and whose content and edit code were stored unencrypted and unhashed.
type LegacyNote struct {
	Slug     string
	EditCode string
}

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
}

// EditCodeHasher hashes plaintext edit codes of converted legacy notes. Hash is the fast
// format for server-generated high-entropy codes; HashUserProvided is the memory-hard format
// for caller-chosen codes whose entropy is unknown.
type EditCodeHasher interface {
	Hash(code string) (string, error)
	HashUserProvided(code string) (string, error)
}

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

// Report contains counts and public key IDs only; never slugs, content, or key bytes.
type Report struct {
	Keys       map[string]int `json:"keys"`
	State      string         `json:"state"`
	Raw        int            `json:"raw"`
	Wrapped    int            `json:"wrapped"`
	Native     int            `json:"native"`
	Failed     int            `json:"failed"`
	CorruptRaw int            `json:"corrupt_raw"`
	Plaintext  int            `json:"plaintext"`
	Pruned     int            `json:"pruned"`
	Changed    int            `json:"changed"`
	Conflicts  int            `json:"conflicts"`
	Missing    int            `json:"missing"`
}

type Manager struct {
	store   Storage
	keys    Keyring
	hasher  EditCodeHasher
	ciphers map[string]Cipher
}

func New(store Storage, keys Keyring, notes, links Cipher, hasher EditCodeHasher) *Manager {
	return &Manager{
		store: store, keys: keys, hasher: hasher,
		ciphers: map[string]Cipher{resourceNote: notes, resourceLink: links},
	}
}

// Status authenticates envelopes and reports the remaining legacy layers without writing.
func (m *Manager) Status(ctx context.Context) (Report, error) {
	return m.status(ctx, nil)
}

// PrepareServe runs before any listener, cleanup, TTL or burn operation.
func (m *Manager) PrepareServe(ctx context.Context) error {
	initErr := m.initialize(ctx)
	if initErr != nil {
		return WrapError(initErr)
	}

	report, err := m.Status(ctx)
	if err != nil {
		return WrapError(err)
	}

	if report.State != Ready || report.Raw != 0 {
		return errors.New("content migration required; run encryption migrate while all servers are stopped")
	}

	return nil
}

// Register explicitly registers one new KEK. Existing checks can never be overwritten.
func (m *Manager) Register(ctx context.Context, keyID string) error {
	reg, err := m.store.Registry(ctx)
	if err != nil {
		return WrapError(err)
	}

	if reg == nil {
		return errors.New("initialize encryption with migrate before registering another key")
	}

	validationErr := m.validateKeys(reg, false)
	if validationErr != nil {
		return WrapError(validationErr)
	}

	_, statusErr := m.Status(ctx)
	if statusErr != nil {
		return WrapError(statusErr)
	}

	check, err := m.keys.KeyCheck(reg.InstallationID, keyID)
	if err != nil {
		return WrapError(err)
	}

	if _, exists := reg.Checks[keyID]; exists {
		return nil
	}

	registerErr := m.store.Register(ctx, keyID, check)
	if registerErr != nil {
		return WrapError(registerErr)
	}

	reg, err = m.store.Registry(ctx)
	if err != nil {
		return WrapError(err)
	}

	return WrapError(m.keys.VerifyKeyCheck(reg.InstallationID, keyID, reg.Checks[keyID]))
}

// CanRemove checks live references; historical registry checks remain to prevent ID reuse.
// All writers must be stopped until replicas have installed the resulting keyring.
func (m *Manager) CanRemove(ctx context.Context, keyID string) error {
	if keyID == m.keys.ActiveKey() {
		return errors.New("cannot remove the active key")
	}

	prepareErr := m.PrepareServe(ctx)
	if prepareErr != nil {
		return WrapError(prepareErr)
	}

	report, err := m.Status(ctx)
	if err != nil {
		return WrapError(err)
	}

	if report.Keys[keyID] != 0 {
		return errors.New("key is still referenced by encrypted records")
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
					return WrapError(err)
				}

				err = visit(*row)
				if err != nil {
					return WrapError(err)
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
		return errors.New("invalid encryption migration state")
	}

	_, parseErr := uuid.Parse(reg.InstallationID)
	if parseErr != nil {
		return errors.New("invalid encryption installation ID")
	}

	if requireActive && reg.Checks[m.keys.ActiveKey()] == "" {
		return errors.New("active key is not registered; run encryption register")
	}

	for _, id := range m.keys.KeyIDs() {
		if check, found := reg.Checks[id]; found {
			err := m.keys.VerifyKeyCheck(reg.InstallationID, id, check)
			if err != nil {
				return WrapError(err)
			}
		}
	}

	return nil
}

// inspect classifies a row: unversioned values are raw legacy ciphertext (Raw) or
// malformed raw data (CorruptRaw, also Failed); versioned values that do not
// authenticate are Failed only and are never eligible for pruning.
func (m *Manager) inspect(row Row, reg *Registry, report *Report) {
	cipher := m.ciphers[row.Resource]
	if !cipher.Versioned(row.Value) {
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

// initialize authenticates existing envelopes before binding this DB to the supplied keys.
// Initialize must atomically insert state plus checks, or leave an existing registry untouched.
// Malformed raw records may be bound only in pending state for explicit cleanup;
// envelope, key and storage failures still prevent first-time initialization.
func (m *Manager) initialize(ctx context.Context) error {
	reg, err := m.store.Registry(ctx)
	if err != nil {
		return WrapError(err)
	}

	if reg == nil {
		err := m.initializeRegistry(ctx)
		if err != nil {
			return WrapError(err)
		}

		reg, err = m.store.Registry(ctx)
		if err != nil {
			return WrapError(err)
		}
	}

	return m.validateKeys(reg, true)
}

func (m *Manager) initializeRegistry(ctx context.Context) error {
	report, err := m.Status(ctx)
	if err != nil && !onlyCorruptRaw(&report, err) {
		return WrapError(err)
	}

	reg := &Registry{InstallationID: uuid.NewString(), Phase: Ready, Checks: make(map[string]string)}
	if report.Raw != 0 || report.Failed != 0 {
		reg.Phase = Pending
	}

	for _, id := range m.keys.KeyIDs() {
		check, err := m.keys.KeyCheck(reg.InstallationID, id)
		if err != nil {
			return WrapError(err)
		}

		reg.Checks[id] = check
	}

	return WrapError(m.store.Initialize(ctx, reg))
}

// status removes conversion failures already counted by the final storage scan.
func (m *Manager) status(ctx context.Context, conversionFailures map[string]struct{}) (Report, error) {
	reg, err := m.store.Registry(ctx)

	report := newReport("uninitialized")
	if err != nil {
		return report, WrapError(err)
	}

	validationErr := m.validateKeys(reg, false)
	if validationErr != nil {
		return report, WrapError(validationErr)
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
		return report, WrapError(err)
	}

	if report.Failed != 0 {
		err = invalidRecordsError(&report)

		return report, err
	}

	return report, nil
}
