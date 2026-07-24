//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=notes

package notes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

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

// LinksChecker lets notes.Manager verify a candidate slug isn't already taken by a link — slugs
// are globally unique across notes and links. See links.Manager's mirrored NotesChecker, which
// enforces the same rule in the other direction.
type LinksChecker interface {
	Exists(ctx context.Context, id string) (bool, error)
}

// Renderer converts note content to safe HTML. Render handles markdown; RenderPlain handles
// domain.ContentTypePlain notes (escape + wrap, no markdown parsing). Both live behind this one
// port so the HTML-specific mechanics (escaping, sanitizing, wrapping) stay in the adapter that
// implements it (internal/infra/render) — Manager only decides which content type calls for which.
type Renderer interface {
	Render(content string) (string, error)
	RenderPlain(content string) (string, error)
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

// Manager implements note business logic: CRUD, slug resolution, and encryption coordination.
// Burn-after-reading / on-read expiry policy is delegated to burn (see burn.go) so that state
// machine stays independently readable and testable instead of growing inline here.
type Manager struct {
	storage   Storage
	links     LinksChecker
	renderer  Renderer
	encryptor Encryptor
	hasher    EditCodeHasher
	log       *slog.Logger
	burn      *burner
	// allowCustomSlugs gates user-supplied (human-readable) slugs. When false, custom slugs are
	// rejected and only server-generated random slugs are issued.
	allowCustomSlugs bool
}

// NewManager creates a new Manager with required dependencies. allowCustomSlugs enables
// user-supplied slugs; when false, a create request carrying a slug is rejected with
// domain.ErrCustomSlugDisabled (a custom slug is low-entropy content-encryption key material).
func NewManager(
	storage Storage, links LinksChecker, renderer Renderer, encryptor Encryptor, hasher EditCodeHasher,
	log *slog.Logger, allowCustomSlugs bool,
) *Manager {
	return &Manager{
		storage:          storage,
		links:            links,
		renderer:         renderer,
		encryptor:        encryptor,
		hasher:           hasher,
		log:              log,
		burn:             newBurner(storage, encryptor, log),
		allowCustomSlugs: allowCustomSlugs,
	}
}

// validateForCreate runs Note.Validate and the ownership invariant (Note.ValidateOwnership).
// OwnerID is already final at this point (set by the HTTP layer before Create is called), unlike
// Update, where the same ownership check must wait until after OwnerID is pinned from the
// existing note — see the call site in Update for why it cannot share this helper.
func validateForCreate(note *domain.Note) error {
	err := note.Validate()
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	err = note.ValidateOwnership()
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	return nil
}

// Create validates and persists a new note.
func (m *Manager) Create(ctx context.Context, note *domain.Note) (*domain.Note, error) {
	if note.ContentType == nil {
		ct := domain.ContentTypeMarkdown
		note.ContentType = &ct
	}

	err := validateForCreate(note)
	if err != nil {
		return nil, err
	}

	slug, err := m.resolveUniqueSlug(ctx, note)
	if err != nil {
		return nil, err
	}

	if note.EditCode == "" {
		editCode, editCodeErr := domain.NewEditCode()
		if editCodeErr != nil {
			return nil, fmt.Errorf("generate edit code: %w", editCodeErr)
		}

		note.EditCode = editCode
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
	note.ID = domain.HashSlug(slug) // store hash, not slug

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
	note, err := m.storage.Get(ctx, domain.HashSlug(id))
	if err != nil {
		return nil, fmt.Errorf("peek note: %w", err)
	}

	note.ID = id

	err = decryptContent(ctx, m.encryptor, m.log, note)
	if err != nil {
		return nil, fmt.Errorf("peek note: %w", err)
	}

	return note, nil
}

// Get returns a note by ID. If the note has expired it is deleted and ErrExpired is returned.
// Burn-after-reading notes are atomically consumed (deleted and returned) to prevent races.
func (m *Manager) Get(ctx context.Context, id string) (*domain.Note, error) {
	note, err := m.storage.Get(ctx, domain.HashSlug(id))
	if err != nil {
		return nil, fmt.Errorf("get note: %w", err)
	}

	note.ID = id

	err = decryptContent(ctx, m.encryptor, m.log, note)
	if err != nil {
		return nil, fmt.Errorf("get note: %w", err)
	}

	return m.burn.applyPolicy(ctx, id, note)
}

// View returns a note by ID and increments its view counter.
// Burn-after-reading notes are not incremented because they are deleted on read.
func (m *Manager) View(ctx context.Context, id string) (*domain.Note, error) {
	note, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	m.burn.applyViewIncrement(ctx, id, note)

	return note, nil
}

// ViewPreloaded is like View but skips the initial storage.Get by reusing an already-fetched note.
// Use this when the caller has already loaded the note (e.g. during auth checks) to avoid a second SELECT.
func (m *Manager) ViewPreloaded(ctx context.Context, id string, preloaded *domain.Note) (*domain.Note, error) {
	note, err := m.burn.applyPolicy(ctx, id, preloaded)
	if err != nil {
		return nil, err
	}

	m.burn.applyViewIncrement(ctx, id, note)

	return note, nil
}

// pinImmutableFields carries forward two fields Update must never let the caller change: the
// stored edit-code hash (never the plaintext the caller supplied — the repository's Update
// column list excludes edit_code today, but if that ever changes this avoids leaking it) and
// OwnerID (fixed at creation — excluded from the same column list today, kept here so it isn't
// silently nilled out if that changes). Only once OwnerID is pinned to its real value does
// ValidateOwnership's check become meaningful: the upfront Validate() call in Update runs before
// this pin, when the caller-supplied OwnerID is always nil regardless of actual ownership, so it
// cannot reject privacy=owner on a note that never had one — this can.
func pinImmutableFields(note, existing *domain.Note) error {
	note.EditCode = existing.EditCode
	note.OwnerID = existing.OwnerID

	err := note.ValidateOwnership()
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	return nil
}

// Update validates and updates an existing note, preserving immutable metadata.
// The caller must supply the correct edit code.
// Update replaces a note's editable fields. The caller must either supply the note's edit_code,
// or be the note's owner (callerID matches existing.OwnerID) — the latter lets an authenticated
// creator (session or API token) edit without the edit_code. callerID is uuid.Nil for an
// anonymous caller, which never matches an owner and falls back to the edit_code check
// unconditionally.
func (m *Manager) Update(
	ctx context.Context, id, editCode string, callerID uuid.UUID, note *domain.Note,
) (*domain.Note, error) {
	err := note.Validate()
	if err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}

	dbID := domain.HashSlug(id)

	existing, err := m.storage.Get(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	if !existing.OwnedBy(callerID) && !m.hasher.Verify(existing.EditCode, editCode) {
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

	err = pinImmutableFields(note, existing)
	if err != nil {
		return nil, err
	}

	note.Content, err = m.encryptor.Encrypt(note.Content, id)
	if err != nil {
		return nil, fmt.Errorf("encrypt content: %w", err)
	}

	err = m.storage.Update(ctx, dbID, note)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	m.log.DebugContext(ctx, "note updated", "id", id)

	return note, nil
}

// Delete removes a note by ID after verifying the edit code, or that the caller is the note's
// owner — see Update for the exact bypass semantics.
func (m *Manager) Delete(ctx context.Context, id, editCode string, callerID uuid.UUID) error {
	dbID := domain.HashSlug(id)

	existing, err := m.storage.Get(ctx, dbID)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}

	if !existing.OwnedBy(callerID) && !m.hasher.Verify(existing.EditCode, editCode) {
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

// renderNote converts note content to safe HTML.
// Plain-text notes are escaped and wrapped in <pre> (Renderer.RenderPlain); markdown notes are
// parsed and sanitized (Renderer.Render). Manager only picks which strategy the content type calls for.
func (m *Manager) renderNote(id string, note *domain.Note) (string, error) {
	if note.ContentType != nil && *note.ContentType == domain.ContentTypePlain {
		rendered, err := m.renderer.RenderPlain(note.Content)
		if err != nil {
			return "", fmt.Errorf("render note %s: %w", id, err)
		}

		return rendered, nil
	}

	rendered, err := m.renderer.Render(note.Content)
	if err != nil {
		return "", fmt.Errorf("render note %s: %w", id, err)
	}

	return rendered, nil
}

// resolveUniqueSlug resolves note.ID into a concrete slug and confirms no link already claims it.
// Slugs are globally unique across notes and links (see links.Manager.Create's mirrored
// notes.Exists check). Unlike links.Manager.Create, this never retries on collision — notes never
// retried on a same-table collision either (the DB's own PK constraint on storage.Create was
// always the sole backstop), so a cross-table hit is treated the same way: an immediate
// domain.ErrSlugConflict, not a silently-generated different slug.
func (m *Manager) resolveUniqueSlug(ctx context.Context, note *domain.Note) (string, error) {
	slug, err := domain.ResolveSlug(note.ID, m.allowCustomSlugs)
	if err != nil {
		return "", fmt.Errorf("resolve slug: %w", err)
	}

	linkTaken, linksErr := m.links.Exists(ctx, domain.HashSlug(slug))
	if linksErr != nil {
		return "", fmt.Errorf("check link slug: %w", linksErr)
	}

	if linkTaken {
		return "", domain.ErrSlugConflict
	}

	return slug, nil
}

// decryptContent decrypts note.Content in place using the slug (note.ID) as the key.
// The content key is derived from the slug, so any decryption failure means the caller
// lacks the correct slug (or the stored data is corrupt) — either way the note is
// inaccessible, so it is collapsed to domain.ErrNotFound (also avoids an existence oracle).
// Genuine data corruption (malformed ciphertext) is logged at error level to distinguish it
// from an ordinary wrong-slug miss, which is expected and logged at warn level.
//
// A free function rather than a Manager method so both Manager and burner (burn.go) — which
// hold separate copies of the same encryptor/log collaborators — can decrypt without one
// depending on the other.
func decryptContent(ctx context.Context, enc Encryptor, log *slog.Logger, note *domain.Note) error {
	plaintext, err := enc.Decrypt(note.Content, note.ID)
	if err != nil {
		if errors.Is(err, domain.ErrMalformedCiphertext) {
			log.ErrorContext(ctx, "stored content is corrupt", "id", note.ID, "err", err)
		} else {
			log.WarnContext(ctx, "content decryption failed (likely wrong slug)", "id", note.ID, "err", err)
		}

		return domain.ErrNotFound
	}

	note.Content = plaintext

	return nil
}
