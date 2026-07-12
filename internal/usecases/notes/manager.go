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
	"strings"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// slugRe matches a custom slug: one or more path segments joined by "/", each starting with an
// alphanumeric and otherwise containing alphanumerics, dot, hyphen or underscore. This allows
// path-like slugs such as "project/GUIDE.md" while rejecting "..", "//", leading/trailing "/"
// and dotfiles (path-traversal-ish shapes). Overall length is bounded separately by slugMaxLen.
var slugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

// slugMaxLen bounds the total slug length (a slug doubles as the content encryption key material).
const slugMaxLen = 100

// reservedSlugPrefixes returns the first path segments that name built-in routes. A custom slug
// must not start with one, or its URL would resolve to that route (e.g. "edit/foo" → the editor)
// instead of the note. Keep in sync with buildNamedRoutes in internal/adapters/http.
func reservedSlugPrefixes() map[string]struct{} {
	return map[string]struct{}{
		"login": {}, "setup": {}, "logout": {}, "totp-login": {},
		"admin": {}, "api": {}, "success": {}, "healthz": {}, "readyz": {},
		"notes": {}, "edit": {}, "change-password": {}, "static": {},
	}
}

const (
	slugChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	// slugLength balances short, friendly URLs against the fact that the slug doubles
	// as the content encryption key. 10 chars over a 62-char alphabet ≈ 60 bits: safe
	// against online URL-guessing and a meaningful offline brute-force cost (years).
	slugLength = 10

	editCodeChars  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	editCodeLength = 12
)

func newSlug() string     { return randomString(slugChars, slugLength) }
func newEditCode() string { return randomString(editCodeChars, editCodeLength) }

// resolveSlug validates or generates the note's slug, returning it. A user-supplied slug is only
// accepted when allowCustom is true; otherwise it is rejected so callers cannot pick low-entropy,
// guessable key material.
func resolveSlug(note *domain.Note, allowCustom bool) (string, error) {
	if note.ID == "" {
		note.ID = newSlug()

		return note.ID, nil
	}

	if !allowCustom {
		return "", domain.ErrCustomSlugDisabled
	}

	if !validCustomSlug(note.ID) {
		return "", domain.ErrInvalidSlug
	}

	return note.ID, nil
}

// validCustomSlug reports whether a user-supplied slug is acceptable: bounded length, path-like
// shape (see slugRe), and a first segment that does not collide with a reserved route name.
func validCustomSlug(slug string) bool {
	if slug == "" || len(slug) > slugMaxLen {
		return false
	}

	if !slugRe.MatchString(slug) {
		return false
	}

	first, _, _ := strings.Cut(slug, "/")

	_, reserved := reservedSlugPrefixes()[first]

	return !reserved
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
	// allowCustomSlugs gates user-supplied (human-readable) slugs. When false, custom slugs are
	// rejected and only server-generated random slugs are issued.
	allowCustomSlugs bool
}

// NewManager creates a new Manager with required dependencies. allowCustomSlugs enables
// user-supplied slugs; when false, a create request carrying a slug is rejected with
// domain.ErrCustomSlugDisabled (a custom slug is low-entropy content-encryption key material).
func NewManager(
	storage Storage, renderer Renderer, encryptor Encryptor, hasher EditCodeHasher,
	log *slog.Logger, allowCustomSlugs bool,
) *Manager {
	return &Manager{
		storage:          storage,
		renderer:         renderer,
		encryptor:        encryptor,
		hasher:           hasher,
		log:              log,
		allowCustomSlugs: allowCustomSlugs,
	}
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

	slug, err := resolveSlug(note, m.allowCustomSlugs)
	if err != nil {
		return nil, err
	}

	if note.EditCode == "" {
		note.EditCode = newEditCode()
	}

	// Create mutates note in place for persistence (hash the edit code, encrypt the content,
	// store the hashed slug as the PK). The defer restores the caller-facing plaintext fields
	// and the slug ID on every return path, so no branch can leave note in an encrypted state.
	plaintextContent := note.Content
	plaintextCode := note.EditCode

	defer func() {
		note.Content = plaintextContent
		note.EditCode = plaintextCode
		note.ID = slug
	}()

	hashedCode, hashErr := m.hasher.Hash(note.EditCode)
	if hashErr != nil {
		return nil, fmt.Errorf("hash edit code: %w", hashErr)
	}

	note.EditCode = hashedCode

	now := time.Now().UTC()
	note.CreatedAt = now
	note.UpdatedAt = now

	encrypted, encErr := m.encryptor.Encrypt(note.Content, slug)
	if encErr != nil {
		return nil, fmt.Errorf("encrypt content: %w", encErr)
	}

	note.Content = encrypted
	note.ID = hashSlug(slug) // store hash, not slug

	err = m.storage.Create(ctx, note)
	if err != nil {
		return nil, fmt.Errorf("create note: %w", err)
	}

	m.log.DebugContext(ctx, "note created", "id", slug)

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

	err = m.decryptNote(ctx, note)
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

	err = m.decryptNote(ctx, note)
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
		return nil, domain.ErrInvalidEditCode
	}

	note.ID = id
	note.CreatedAt = existing.CreatedAt
	note.UpdatedAt = time.Now().UTC()

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

	// Update mutates note in place for persistence (encrypt content, carry the stored edit-code
	// hash). The defer restores the caller-facing plaintext fields on every return path, so the
	// returned note never exposes ciphertext and the caller still sees the plaintext edit code.
	plaintextContent := note.Content

	defer func() {
		note.Content = plaintextContent
		note.EditCode = editCode
	}()

	// Persist the stored hash, never the plaintext the caller supplied. The repository Update
	// writes a fixed column allow-list that excludes edit_code, so today this is not persisted
	// at all — but if that list ever changes, this rewrites the column with the existing hash
	// instead of leaking the plaintext code.
	note.EditCode = existing.EditCode

	encrypted, encErr := m.encryptor.Encrypt(note.Content, id)
	if encErr != nil {
		return nil, fmt.Errorf("encrypt content: %w", encErr)
	}

	note.Content = encrypted

	err = m.storage.Update(ctx, dbID, note)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

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
		return domain.ErrInvalidEditCode
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

// applyViewIncrement increments the view counter unless the note was just consumed by a
// pure burn-after-reading (no TTL) — that note no longer exists, so a view count is moot.
// A burn-after-reading note with a TTL survives its grace period and IS counted on every
// read: SetBurnExpiry flips burn_after_reading off on the timer-start read, so by the time
// this runs BurnAfterReading is true only for the deleted pure-burn case.
func (m *Manager) applyViewIncrement(ctx context.Context, id string, note *domain.Note) {
	if note.BurnAfterReading {
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

// decryptNote decrypts note.Content in place using the slug (note.ID) as the key.
// The content key is derived from the slug, so any decryption failure means the caller
// lacks the correct slug (or the stored data is corrupt) — either way the note is
// inaccessible, so it is collapsed to domain.ErrNotFound (also avoids an existence oracle).
// Genuine data corruption (malformed ciphertext) is logged at error level to distinguish it
// from an ordinary wrong-slug miss, which is expected and logged at warn level.
func (m *Manager) decryptNote(ctx context.Context, note *domain.Note) error {
	plaintext, err := m.encryptor.Decrypt(note.Content, note.ID)
	if err != nil {
		if errors.Is(err, domain.ErrMalformedCiphertext) {
			m.log.ErrorContext(ctx, "stored content is corrupt", "id", note.ID, "err", err)
		} else {
			m.log.WarnContext(ctx, "content decryption failed (likely wrong slug)", "id", note.ID, "err", err)
		}

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

	decErr := m.decryptNote(ctx, consumed)
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

	decErr := m.decryptNote(ctx, updated)
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

	decErr := m.decryptNote(ctx, current)
	if decErr != nil {
		return nil, fmt.Errorf("burn after reading (re-fetch): %w", decErr)
	}

	return current, nil
}
