//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=notes

package notes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math/big"
	"regexp"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

var slugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}$`)

const (
	slugChars  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	slugLength = 32

	editCodeChars  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	editCodeLength = 12
)

func newSlug() string     { return randomString(slugChars, slugLength) }
func newEditCode() string { return randomString(editCodeChars, editCodeLength) }

// resolveSlug validates or generates the note's slug, returning it.
func resolveSlug(note *domain.Note) (string, error) {
	if note.ID == "" {
		note.ID = newSlug()

		return note.ID, nil
	}

	if !slugRe.MatchString(note.ID) {
		return "", domain.ErrInvalidSlug
	}

	return note.ID, nil
}

// hashSlug returns sha256(slug) as hex — the DB primary key.
// The plaintext slug (the AES key material) is never stored at rest.
func hashSlug(slug string) string {
	sum := sha256.Sum256([]byte(slug))

	return hex.EncodeToString(sum[:])
}

func randomString(chars string, length int) string {
	size := big.NewInt(int64(len(chars)))
	buf := make([]byte, length)

	for idx := range length {
		nn, err := rand.Int(rand.Reader, size)
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}

		buf[idx] = chars[nn.Int64()]
	}

	return string(buf)
}

// Storage defines persistence operations for notes.
type Storage interface {
	Create(ctx context.Context, note *domain.Note) error
	Get(ctx context.Context, id string) (*domain.Note, error)
	Consume(ctx context.Context, id string) (*domain.Note, error)
	SetBurnExpiry(ctx context.Context, id string, expiresAt time.Time) (*domain.Note, error)
	Update(ctx context.Context, id string, note *domain.Note) error
	Delete(ctx context.Context, id string) error
	IncrementViews(ctx context.Context, id string) error
}

// Renderer defines markdown-to-HTML rendering.
type Renderer interface {
	Render(content string) (string, error)
}

// Encryptor encrypts and decrypts note content using the note's ID as key material.
type Encryptor interface {
	Encrypt(plaintext, key string) (string, error)
	Decrypt(ciphertext, key string) (string, error)
}

// EditCodeHasher hashes and verifies note edit codes.
type EditCodeHasher interface {
	Hash(code string) (string, error)
	Verify(storedHash, code string) bool
}

// Manager implements note business logic.
type Manager struct {
	storage   Storage
	renderer  Renderer
	encryptor Encryptor
	hasher    EditCodeHasher
	log       *slog.Logger
}

// NewManager creates a new Manager with required dependencies.
func NewManager(
	storage Storage, renderer Renderer, encryptor Encryptor, hasher EditCodeHasher, log *slog.Logger,
) *Manager {
	return &Manager{storage: storage, renderer: renderer, encryptor: encryptor, hasher: hasher, log: log}
}

// Create validates and persists a new note.
func (m *Manager) Create(ctx context.Context, note *domain.Note) (*domain.Note, error) {
	if note.ContentType == nil {
		ct := domain.ContentTypeMarkdown
		note.ContentType = &ct
	}

	err := note.Validate()
	if err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}

	slug, err := resolveSlug(note)
	if err != nil {
		return nil, err
	}

	if note.EditCode == "" {
		note.EditCode = newEditCode()
	}

	plaintextCode := note.EditCode

	hashedCode, hashErr := m.hasher.Hash(note.EditCode)
	if hashErr != nil {
		return nil, fmt.Errorf("hash edit code: %w", hashErr)
	}

	note.EditCode = hashedCode

	now := time.Now().UTC()
	note.CreatedAt = now
	note.UpdatedAt = now

	plaintextContent := note.Content

	encrypted, encErr := m.encryptor.Encrypt(note.Content, slug)
	if encErr != nil {
		note.EditCode = plaintextCode

		return nil, fmt.Errorf("encrypt content: %w", encErr)
	}

	note.Content = encrypted
	note.ID = hashSlug(slug) // store hash, not slug

	err = m.storage.Create(ctx, note)

	note.ID = slug // restore slug for caller regardless of error
	if err != nil {
		note.Content = plaintextContent
		note.EditCode = plaintextCode

		return nil, fmt.Errorf("create note: %w", err)
	}

	note.Content = plaintextContent
	note.EditCode = plaintextCode

	m.log.DebugContext(ctx, "note created", "id", note.ID)

	return note, nil
}

// Peek fetches a note by ID without incrementing views, triggering burn-after-reading,
// or applying expiry policy. Intentionally returns expired notes as-is — callers that
// need expiry enforcement should use Get or View instead.
func (m *Manager) Peek(ctx context.Context, id string) (*domain.Note, error) {
	note, err := m.storage.Get(ctx, hashSlug(id))
	if err != nil {
		return nil, fmt.Errorf("peek note: %w", err)
	}

	note.ID = id

	err = m.decryptNote(note)
	if err != nil {
		return nil, fmt.Errorf("peek note: %w", err)
	}

	return note, nil
}

// Get returns a note by ID. If the note has expired it is deleted and ErrExpired is returned.
// Burn-after-reading notes are atomically consumed (deleted and returned) to prevent races.
func (m *Manager) Get(ctx context.Context, id string) (*domain.Note, error) {
	note, err := m.storage.Get(ctx, hashSlug(id))
	if err != nil {
		return nil, fmt.Errorf("get note: %w", err)
	}

	note.ID = id

	err = m.decryptNote(note)
	if err != nil {
		return nil, fmt.Errorf("get note: %w", err)
	}

	return m.applyNotePolicy(ctx, id, note)
}

// View returns a note by ID and increments its view counter.
// Burn-after-reading notes are not incremented because they are deleted on read.
func (m *Manager) View(ctx context.Context, id string) (*domain.Note, error) {
	note, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	m.applyViewIncrement(ctx, id, note)

	return note, nil
}

// ViewPreloaded is like View but skips the initial storage.Get by reusing an already-fetched note.
// Use this when the caller has already loaded the note (e.g. during auth checks) to avoid a second SELECT.
func (m *Manager) ViewPreloaded(ctx context.Context, id string, preloaded *domain.Note) (*domain.Note, error) {
	note, err := m.applyNotePolicy(ctx, id, preloaded)
	if err != nil {
		return nil, err
	}

	m.applyViewIncrement(ctx, id, note)

	return note, nil
}

// Update validates and updates an existing note, preserving immutable metadata.
// The caller must supply the correct edit code.
func (m *Manager) Update(
	ctx context.Context, id, editCode string, note *domain.Note,
) (*domain.Note, error) {
	err := note.Validate()
	if err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}

	dbID := hashSlug(id)

	existing, err := m.storage.Get(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	if !m.hasher.Verify(existing.EditCode, editCode) {
		return nil, domain.ErrForbidden
	}

	note.ID = id
	note.CreatedAt = existing.CreatedAt
	note.UpdatedAt = time.Now().UTC()
	note.EditCode = editCode

	if note.ExpiresAt == nil {
		// When burn is being disabled (BurnTTL cleared) but the existing note has a
		// burn-timer expiry, do not inherit it — the stale expiry must be dropped.
		if note.BurnTTL == 0 && existing.BurnTTL > 0 {
			note.ExpiresAt = nil
		} else {
			note.ExpiresAt = existing.ExpiresAt
		}
	}

	if note.ContentType == nil {
		note.ContentType = existing.ContentType
	}

	plaintext := note.Content

	encrypted, encErr := m.encryptor.Encrypt(note.Content, id)
	if encErr != nil {
		return nil, fmt.Errorf("encrypt content: %w", encErr)
	}

	note.Content = encrypted

	err = m.storage.Update(ctx, dbID, note)
	if err != nil {
		note.Content = plaintext

		return nil, fmt.Errorf("update note: %w", err)
	}

	note.Content = plaintext

	m.log.DebugContext(ctx, "note updated", "id", id)

	return note, nil
}

// Delete removes a note by ID after verifying the edit code.
func (m *Manager) Delete(ctx context.Context, id, editCode string) error {
	dbID := hashSlug(id)

	existing, err := m.storage.Get(ctx, dbID)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}

	if !m.hasher.Verify(existing.EditCode, editCode) {
		return domain.ErrForbidden
	}

	err = m.storage.Delete(ctx, dbID)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}

	return nil
}

// GetRendered fetches a note, increments its view counter, and returns it with content as safe HTML.
func (m *Manager) GetRendered(ctx context.Context, id string) (*domain.Note, string, error) {
	note, err := m.View(ctx, id)
	if err != nil {
		return nil, "", fmt.Errorf("get note for render: %w", err)
	}

	rendered, err := m.renderNote(id, note)
	if err != nil {
		return nil, "", err
	}

	return note, rendered, nil
}

// GetRenderedPreloaded is like GetRendered but skips the initial storage.Get by reusing an already-fetched note.
// Use this when the caller has already loaded the note (e.g. during auth checks) to avoid a second SELECT.
func (m *Manager) GetRenderedPreloaded(
	ctx context.Context, id string, preloaded *domain.Note,
) (*domain.Note, string, error) {
	note, err := m.ViewPreloaded(ctx, id, preloaded)
	if err != nil {
		return nil, "", fmt.Errorf("get note for render: %w", err)
	}

	rendered, err := m.renderNote(id, note)
	if err != nil {
		return nil, "", err
	}

	return note, rendered, nil
}

// applyNotePolicy applies expiry and burn-after-reading logic to an already-fetched note.
func (m *Manager) applyNotePolicy(ctx context.Context, id string, note *domain.Note) (*domain.Note, error) {
	if note.ExpiresAt != nil && time.Now().After(*note.ExpiresAt) {
		delErr := m.storage.Delete(ctx, hashSlug(id))
		if delErr != nil {
			m.log.ErrorContext(ctx, "delete expired note", "id", id, "err", delErr)
		}

		return nil, domain.ErrExpired
	}

	if note.BurnAfterReading {
		return m.burnNote(ctx, id, note)
	}

	return note, nil
}

// applyViewIncrement increments the view counter unless the note is in a burn state.
// After SetBurnExpiry the storage flips burn_after_reading=false, so the note
// comes back with BurnAfterReading=false. Checking only BurnAfterReading
// would incorrectly increment views during the burn grace period.
// BurnTTL>0 && ExpiresAt!=nil is the unique signature of a note whose burn
// timer has been started but has not yet expired.
func (m *Manager) applyViewIncrement(ctx context.Context, id string, note *domain.Note) {
	inBurnGrace := note.BurnTTL > 0 && note.ExpiresAt != nil
	if note.BurnAfterReading || inBurnGrace {
		return
	}

	incErr := m.storage.IncrementViews(ctx, hashSlug(id))
	if incErr != nil {
		m.log.ErrorContext(ctx, "increment views", "id", id, "err", incErr)
	} else {
		note.Views++
	}
}

// renderNote converts note content to safe HTML.
// Plain-text notes are HTML-escaped and wrapped in <pre>; markdown notes are rendered.
func (m *Manager) renderNote(id string, note *domain.Note) (string, error) {
	if note.ContentType != nil && *note.ContentType == domain.ContentTypePlain {
		return "<pre>" + html.EscapeString(note.Content) + "</pre>", nil
	}

	rendered, err := m.renderer.Render(note.Content)
	if err != nil {
		return "", fmt.Errorf("render note %s: %w", id, err)
	}

	return rendered, nil
}

func (m *Manager) decryptNote(note *domain.Note) error {
	plaintext, err := m.encryptor.Decrypt(note.Content, note.ID)
	if err != nil {
		// Treat decryption failure as not-found: without the plaintext slug from the URL
		// there is no way to decrypt, so the note is inaccessible from the attacker's POV.
		m.log.Warn("content decryption failed", "id", note.ID, "err", err)

		return domain.ErrNotFound
	}

	note.Content = plaintext

	return nil
}

func (m *Manager) burnNote(ctx context.Context, id string, note *domain.Note) (*domain.Note, error) {
	if note.BurnTTL > 0 {
		return m.startBurnTimer(ctx, id, note)
	}

	consumed, consumeErr := m.storage.Consume(ctx, hashSlug(id))
	if consumeErr != nil {
		return nil, fmt.Errorf("burn after reading: %w", consumeErr)
	}

	consumed.ID = id

	decErr := m.decryptNote(consumed)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading: %w", decErr)
	}

	m.log.DebugContext(ctx, "note burned", "id", id)

	return consumed, nil
}

func (m *Manager) startBurnTimer(ctx context.Context, id string, note *domain.Note) (*domain.Note, error) {
	expiresAt := time.Now().UTC().Add(time.Duration(note.BurnTTL) * time.Second)

	updated, burnErr := m.storage.SetBurnExpiry(ctx, hashSlug(id), expiresAt)
	if burnErr != nil {
		return m.handleSetBurnExpiryErr(ctx, id, burnErr)
	}

	updated.ID = id

	decErr := m.decryptNote(updated)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading: %w", decErr)
	}

	m.log.DebugContext(ctx, "burn timer started", "id", id, "expires_at", expiresAt)

	return updated, nil
}

func (m *Manager) handleSetBurnExpiryErr(ctx context.Context, id string, burnErr error) (*domain.Note, error) {
	if !errors.Is(burnErr, domain.ErrNotFound) {
		return nil, fmt.Errorf("burn after reading: %w", burnErr)
	}

	current, getErr := m.storage.Get(ctx, hashSlug(id))
	if getErr != nil {
		return nil, fmt.Errorf("burn after reading (re-fetch): %w", getErr)
	}

	current.ID = id

	decErr := m.decryptNote(current)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading (re-fetch): %w", decErr)
	}

	return current, nil
}
