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

// Storage defines persistence operations for notes. Create must atomically claim the ID
// across notes and links, returning domain.ErrSlugConflict if another resource owns it.
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

// SlugLock serializes local preflight checks in resolveUniqueSlug. Share the same instance
// with links.Manager. Storage must independently enforce cross-resource uniqueness when
// Create persists a row: different server processes do not share this lock.
type SlugLock interface {
	Lock(ctx context.Context, key string) (unlock func(), err error)
}

// Renderer converts note content to safe HTML. Render handles markdown; RenderPlain handles
// domain.ContentTypePlain notes (escape + wrap, no markdown parsing). Both live behind this one
// port so the HTML-specific mechanics (escaping, sanitizing, wrapping) stay in the adapter that
// implements it (internal/infra/render) — Manager only decides which content type calls for which.
type Renderer interface {
	Render(content string) (string, error)
	RenderPlain(content string) (string, error)
}

// RenderCache memoizes rendered HTML so repeat views of an unchanged note skip markdown parsing
// and sanitizing. Entries are keyed by the hashed note id (domain.HashSlug) and valid only for the
// UpdatedAt they were rendered at, so a note changed anywhere — including through another replica —
// misses instead of serving stale HTML. Strategy: fail-soft; the cache is an optimisation, so a
// miss, an eviction or an implementation that stores nothing only costs a re-render.
type RenderCache interface {
	// Get returns the HTML cached for id at updatedAt, if any.
	Get(id string, updatedAt time.Time) (string, bool)
	// Set stores html as the render of id at updatedAt; an implementation may decline.
	Set(id string, updatedAt time.Time, html string)
	// Delete drops whatever is cached for id.
	Delete(id string)
}

// noRenderCache is the default RenderCache: it caches nothing.
type noRenderCache struct{}

func (noRenderCache) Get(string, time.Time) (string, bool) { return "", false }
func (noRenderCache) Set(string, time.Time, string)        {}
func (noRenderCache) Delete(string)                        {}

// Encryptor writes content using installation keys and the stored record ID as authenticated
// context. legacySlug is only for reading old records; new ciphertext needs no slug to decrypt.
// The interface lives at its consumer to keep the usecase independent of crypto infrastructure.
type Encryptor interface {
	Encrypt(plaintext, recordID string) (string, error)
	Decrypt(ciphertext, recordID, legacySlug string) (string, error)
}

// EditCodeHasher hashes and verifies note edit codes. Hash is for server-generated
// high-entropy codes; HashUserProvided is for caller-chosen codes, which need a
// memory-hard hash because their entropy is not guaranteed.
type EditCodeHasher interface {
	Hash(code string) (string, error)
	HashUserProvided(code string) (string, error)
	Verify(storedHash, code string) bool
}

// Manager implements note business logic: CRUD, slug resolution, and encryption coordination.
// Burn-after-reading / on-read expiry policy is delegated to burn (see burn.go) so that state
// machine stays independently readable and testable instead of growing inline here.
type Manager struct {
	storage   Storage
	links     LinksChecker
	slugLock  SlugLock
	renderer  Renderer
	encryptor Encryptor
	hasher    EditCodeHasher
	log       *slog.Logger
	burn      *burner
	// renderCache defaults to noRenderCache; attach a real one with WithRenderCache.
	renderCache RenderCache
	// allowCustomSlugs gates user-supplied (human-readable) slugs. When false, custom slugs are
	// rejected and only server-generated random slugs are issued.
	allowCustomSlugs bool
}

// NewManager creates a new Manager with required dependencies. allowCustomSlugs enables
// user-supplied slugs; when false, a create request carrying a slug is rejected with
// domain.ErrCustomSlugDisabled (custom public URLs are easier to guess).
// slugLock must be the same instance passed to the paired links.Manager — see SlugLock's doc
// comment for why.
func NewManager(
	storage Storage, links LinksChecker, slugLock SlugLock, renderer Renderer, encryptor Encryptor,
	hasher EditCodeHasher, log *slog.Logger, allowCustomSlugs bool,
) *Manager {
	return &Manager{
		storage:          storage,
		links:            links,
		slugLock:         slugLock,
		renderer:         renderer,
		encryptor:        encryptor,
		hasher:           hasher,
		log:              log,
		burn:             newBurner(storage, encryptor, log),
		renderCache:      noRenderCache{},
		allowCustomSlugs: allowCustomSlugs,
	}
}

// WithRenderCache attaches a RenderCache for rendered note HTML. The default caches nothing, so
// behaviour is identical without one. Called by the composition root; do not call after the
// server starts serving requests.
func (m *Manager) WithRenderCache(cache RenderCache) *Manager {
	m.renderCache = cache

	return m
}

// validateForCreate runs Note.Validate, the ownership invariant (Note.ValidateOwnership) and,
// when the caller chose one, the edit-code policy (domain.ValidateEditCode) — before anything is
// resolved, hashed or persisted. An empty EditCode is not validated: Create generates it.
// OwnerID is already final at this point (set by the HTTP layer before Create is called), unlike
// Update, where the same ownership check must wait until after OwnerID is pinned from the
// existing note — see the call site in Update for why it cannot share this helper. Update never
// accepts a replacement code, so the edit-code policy only ever applies here.
func validateForCreate(note *domain.Note) error {
	err := note.Validate()
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	err = note.ValidateOwnership()
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	if note.EditCode != "" {
		err = domain.ValidateEditCode(note.EditCode)
		if err != nil {
			return fmt.Errorf("validate edit code: %w", err)
		}
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

	slug, unlock, err := m.resolveUniqueSlug(ctx, note)
	if err != nil {
		return nil, err
	}
	defer unlock()

	hashedCode, hashErr := m.resolveEditCodeHash(note)
	if hashErr != nil {
		return nil, hashErr
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

	note.EditCode = hashedCode

	now := time.Now().UTC()
	note.CreatedAt = now
	note.UpdatedAt = now

	encrypted, encErr := m.encryptor.Encrypt(note.Content, domain.HashSlug(slug))
	if encErr != nil {
		return nil, fmt.Errorf("encrypt content: %w", encErr)
	}

	note.Content = encrypted
	note.ID = domain.HashSlug(slug) // store hash, not slug

	err = m.storage.Create(ctx, note)
	if err != nil {
		return nil, fmt.Errorf("create note: %w", err)
	}

	m.log.DebugContext(ctx, "note created", "id", domain.HashSlug(slug))

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

// EnforceExpiry applies only the expiry half of the read policy to an already-fetched note (from
// Peek): an expired note — plain TTL or a started burn timer — is deleted and ErrExpired is
// returned. It never consumes a burn-after-reading note, starts its timer or counts a view, so a
// caller that must not trigger burn (the editor) still cannot serve an expired note.
func (m *Manager) EnforceExpiry(ctx context.Context, id string, note *domain.Note) error {
	return m.burn.enforceExpiry(ctx, id, note)
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

// backfillPersistedState copies the storage-persisted privacy and view count into the working
// note before the update is written.
func backfillPersistedState(note, existing *domain.Note) {
	if note.Privacy == nil {
		note.Privacy = existing.Privacy
	}

	note.Views = existing.Views
}

// resolveExpiryTime fills in note.ExpiresAt when the caller omitted it. When burn is being
// disabled (BurnTTL cleared) but the existing note has a burn-timer expiry, the stale expiry
// must be dropped rather than inherited.
func resolveExpiryTime(note, existing *domain.Note) {
	if note.ClearExpiry {
		note.ExpiresAt = nil

		return
	}

	if note.ExpiresAt != nil {
		return
	}

	if note.BurnTTL == 0 && existing.BurnTTL > 0 {
		note.ExpiresAt = nil

		return
	}

	note.ExpiresAt = existing.ExpiresAt
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

	resolveExpiryTime(note, existing)

	if note.ContentType == nil {
		note.ContentType = existing.ContentType
	}

	// Privacy is intentionally absent from the UPDATE column list (COALESCE keeps the stored
	// value when the caller omitted it) and views are never writable — so the response must
	// backfill both from the existing note to report the persisted state, not the caller's
	// defaults. Without this, a PUT on a privacy=owner note would answer "public" with
	// "views": 0. Doing it here (before pinImmutableFields) also lets ValidateOwnership judge
	// the note's real inherited privacy.
	backfillPersistedState(note, existing)

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

	note.Content, err = m.encryptor.Encrypt(note.Content, dbID)
	if err != nil {
		return nil, fmt.Errorf("encrypt content: %w", err)
	}

	err = m.storage.Update(ctx, dbID, note)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	m.renderCache.Delete(dbID)

	m.log.DebugContext(ctx, "note updated", "id", domain.HashSlug(id))

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

	m.renderCache.Delete(dbID)

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

// resolveEditCodeHash fills an empty edit code with a server-generated one and returns its
// stored-hash form. Generated codes are high-entropy, so they take the fast hash; a
// caller-supplied code takes the memory-hard variant because its entropy is not guaranteed.
// On success note.EditCode holds the plaintext code (generated or supplied).
func (m *Manager) resolveEditCodeHash(note *domain.Note) (string, error) {
	if note.EditCode == "" {
		editCode, err := domain.NewEditCode()
		if err != nil {
			return "", fmt.Errorf("generate edit code: %w", err)
		}

		note.EditCode = editCode

		hashed, hashErr := m.hasher.Hash(note.EditCode)
		if hashErr != nil {
			return "", fmt.Errorf("hash edit code: %w", hashErr)
		}

		return hashed, nil
	}

	hashed, err := m.hasher.HashUserProvided(note.EditCode)
	if err != nil {
		return "", fmt.Errorf("hash edit code: %w", err)
	}

	return hashed, nil
}

// maxCachedRenderBytes keeps one huge note from pinning megabytes of HTML in the cache.
const maxCachedRenderBytes = 256 * 1024

// renderNote converts note content to safe HTML, going through the render cache for notes that
// can be re-read: a burn-after-reading note is destroyed by the read that renders it, so a cached
// copy would only keep its plaintext alive in memory after the burn.
// The error names the note by domain.HashSlug(id), never the plaintext slug: the slug is the
// note's capability URL and this error ends up in server logs.
func (m *Manager) renderNote(id string, note *domain.Note) (string, error) {
	dbID := domain.HashSlug(id)
	cacheable := !note.BurnAfterReading && note.BurnTTL == 0

	if cacheable {
		cached, ok := m.renderCache.Get(dbID, note.UpdatedAt)
		if ok {
			return cached, nil
		}
	}

	rendered, err := m.renderContent(dbID, note)
	if err != nil {
		return "", err
	}

	if cacheable && len(rendered) <= maxCachedRenderBytes {
		m.renderCache.Set(dbID, note.UpdatedAt, rendered)
	}

	return rendered, nil
}

// renderContent picks the strategy the content type calls for: plain-text notes are escaped and
// wrapped in <pre> (Renderer.RenderPlain), markdown notes are parsed and sanitized (Renderer.Render).
// Errors name the note by dbID (its hash), never the plaintext slug.
func (m *Manager) renderContent(dbID string, note *domain.Note) (string, error) {
	render := m.renderer.Render
	if note.ContentType != nil && *note.ContentType == domain.ContentTypePlain {
		render = m.renderer.RenderPlain
	}

	rendered, err := render(note.Content)
	if err != nil {
		return "", fmt.Errorf("render note %s: %w", dbID, err)
	}

	return rendered, nil
}

// resolveUniqueSlug resolves note.ID into a concrete slug and confirms no link already claims it.
// Unlike links.Manager.Create, notes never retry on collision: the result is
// domain.ErrSlugConflict. Storage.Create arbitrates any races after this preflight check.
//
// The caller must release the local lock after storage.Create, including error paths. This
// function never returns a non-nil unlock alongside a non-nil error.
func (m *Manager) resolveUniqueSlug(
	ctx context.Context, note *domain.Note,
) (slug string, unlock func(), err error) {
	slug, err = domain.ResolveSlug(note.ID, m.allowCustomSlugs)
	if err != nil {
		return "", nil, fmt.Errorf("resolve slug: %w", err)
	}

	unlock, err = m.slugLock.Lock(ctx, domain.HashSlug(slug))
	if err != nil {
		return "", nil, fmt.Errorf("lock slug: %w", err)
	}

	linkTaken, err := m.links.Exists(ctx, domain.HashSlug(slug))
	if err != nil {
		unlock()

		return "", nil, fmt.Errorf("check link slug: %w", err)
	}

	if linkTaken {
		unlock()

		return "", nil, domain.ErrSlugConflict
	}

	return slug, unlock, nil
}

// decryptContent authenticates the stored record ID and decrypts content. The original
// slug is passed separately only for legacy rows. Failed authentication/key lookup is
// collapsed to ErrNotFound to preserve the existing non-disclosure contract.
//
// A free function rather than a Manager method so both Manager and burner (burn.go) — which
// hold separate copies of the same encryptor/log collaborators — can decrypt without one
// depending on the other.
func decryptContent(ctx context.Context, enc Encryptor, log *slog.Logger, note *domain.Note) error {
	plaintext, err := enc.Decrypt(note.Content, domain.HashSlug(note.ID), note.ID)
	if err != nil {
		if errors.Is(err, domain.ErrMalformedCiphertext) {
			log.ErrorContext(ctx, "stored content is corrupt", "id", domain.HashSlug(note.ID), "err", err)
		} else {
			log.WarnContext(ctx, "content decryption failed", "id", domain.HashSlug(note.ID), "err", err)
		}

		return domain.ErrNotFound
	}

	note.Content = plaintext

	return nil
}
