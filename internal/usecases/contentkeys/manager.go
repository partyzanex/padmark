// Package contentkeys manages encrypted records without invoking read/burn lifecycle operations.
package contentkeys

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

type Row struct{ Resource, ID, Value string }

type Registry struct {
	Checks         map[string]string
	InstallationID string
	Phase          string
}

// Storage updates only ciphertext using compare-and-swap. It must never upsert a record.
type Storage interface {
	Registry(ctx context.Context) (*Registry, error)
	Initialize(ctx context.Context, registry *Registry) error
	Register(ctx context.Context, keyID, check string) error
	SetPhase(ctx context.Context, phase string) error
	Batch(ctx context.Context, resource, after string, limit int) ([]Row, error)
	Get(ctx context.Context, resource, id string) (*Row, error)
	Replace(ctx context.Context, row Row, value string) (bool, error)
}

type Keyring interface {
	ActiveKey() string
	KeyIDs() []string
	KeyCheck(installationID, keyID string) (string, error)
	VerifyKeyCheck(installationID, keyID, value string) error
}

type Cipher interface {
	Inspect(value, recordID string) (string, string, error)
	ValidateLegacy(value string) error
	WrapLegacy(value, recordID string) (string, error)
	Rewrap(value, recordID string) (string, error)
	Decrypt(value, recordID, legacySlug string) (string, error)
	Encrypt(plain, recordID string) (string, error)
}

// Report contains counts and public key IDs only; never slugs, content, or key bytes.
type Report struct {
	Keys      map[string]int `json:"keys"`
	State     string         `json:"state"`
	Raw       int            `json:"raw"`
	Wrapped   int            `json:"wrapped"`
	Native    int            `json:"native"`
	Failed    int            `json:"failed"`
	Changed   int            `json:"changed"`
	Conflicts int            `json:"conflicts"`
	Missing   int            `json:"missing"`
}

type Manager struct {
	store   Storage
	keys    Keyring
	ciphers map[string]Cipher
}

func New(store Storage, keys Keyring, notes, links Cipher) *Manager {
	return &Manager{store: store, keys: keys, ciphers: map[string]Cipher{resourceNote: notes, resourceLink: links}}
}

// Status authenticates envelopes and reports the remaining legacy layers without writing.
func (m *Manager) Status(ctx context.Context) (Report, error) {
	reg, err := m.store.Registry(ctx)

	report := newReport("uninitialized")
	if err != nil {
		return report, encryptionError(err)
	}

	validationErr := m.validateKeys(reg, false)
	if validationErr != nil {
		return report, encryptionError(validationErr)
	}

	if reg != nil {
		report.State = reg.Phase
	}

	err = m.walk(ctx, func(row Row) error {
		m.inspect(row, reg, &report)

		return nil
	})
	if err != nil {
		return report, encryptionError(err)
	}

	if report.Failed != 0 {
		return report, errors.New("encrypted records failed validation; check keyring and database integrity")
	}

	return report, nil
}

// PrepareServe runs before any listener, cleanup, TTL or burn operation.
func (m *Manager) PrepareServe(ctx context.Context) error {
	initErr := m.initialize(ctx)
	if initErr != nil {
		return encryptionError(initErr)
	}

	report, err := m.Status(ctx)
	if err != nil {
		return encryptionError(err)
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
		return encryptionError(err)
	}

	if reg == nil {
		return errors.New("initialize encryption with migrate before registering another key")
	}

	validationErr := m.validateKeys(reg, false)
	if validationErr != nil {
		return encryptionError(validationErr)
	}

	_, statusErr := m.Status(ctx)
	if statusErr != nil {
		return encryptionError(statusErr)
	}

	check, err := m.keys.KeyCheck(reg.InstallationID, keyID)
	if err != nil {
		return encryptionError(err)
	}

	if _, exists := reg.Checks[keyID]; exists {
		return nil
	}

	registerErr := m.store.Register(ctx, keyID, check)
	if registerErr != nil {
		return encryptionError(registerErr)
	}

	reg, err = m.store.Registry(ctx)
	if err != nil {
		return encryptionError(err)
	}

	return encryptionError(m.keys.VerifyKeyCheck(reg.InstallationID, keyID, reg.Checks[keyID]))
}

// CanRemove checks live references; historical registry checks remain to prevent ID reuse.
// All writers must be stopped until replicas have installed the resulting keyring.
func (m *Manager) CanRemove(ctx context.Context, keyID string) error {
	if keyID == m.keys.ActiveKey() {
		return errors.New("cannot remove the active key")
	}

	prepareErr := m.PrepareServe(ctx)
	if prepareErr != nil {
		return encryptionError(prepareErr)
	}

	report, err := m.Status(ctx)
	if err != nil {
		return encryptionError(err)
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
					return encryptionError(err)
				}

				err = visit(*row)
				if err != nil {
					return encryptionError(err)
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
				return encryptionError(err)
			}
		}
	}

	return nil
}

func (m *Manager) inspect(row Row, reg *Registry, report *Report) {
	cipher := m.ciphers[row.Resource]
	if !strings.Contains(row.Value, "$") {
		if cipher.ValidateLegacy(row.Value) != nil {
			report.Failed++

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
func (m *Manager) initialize(ctx context.Context) error {
	reg, err := m.store.Registry(ctx)
	if err != nil {
		return encryptionError(err)
	}

	if reg == nil {
		err := m.initializeRegistry(ctx)
		if err != nil {
			return encryptionError(err)
		}

		reg, err = m.store.Registry(ctx)
		if err != nil {
			return encryptionError(err)
		}
	}

	return m.validateKeys(reg, true)
}

func (m *Manager) initializeRegistry(ctx context.Context) error {
	report, err := m.Status(ctx)
	if err != nil {
		return encryptionError(err)
	}

	reg := &Registry{InstallationID: uuid.NewString(), Phase: Ready, Checks: make(map[string]string)}
	if report.Raw != 0 {
		reg.Phase = Pending
	}

	for _, id := range m.keys.KeyIDs() {
		check, err := m.keys.KeyCheck(reg.InstallationID, id)
		if err != nil {
			return encryptionError(err)
		}

		reg.Checks[id] = check
	}

	return encryptionError(m.store.Initialize(ctx, reg))
}
