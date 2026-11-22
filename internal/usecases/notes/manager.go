//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=notes -write_package_comment=false

package notes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
)

// Storage defines persistence operations for notes. Create must atomically claim the ID
// across notes, links and files, returning domain.ErrSlugConflict if another resource owns it:
// it is the only slug uniqueness check (docs/adr/0003-storage-slug-uniqueness.md).
// Consume and SetBurnExpiry change the row only while its privacy still equals expected, the
// level the reader was checked against; otherwise they return domain.ErrNotFound, so a burn
// read can never return or delete a version whose access level changed after the check.
// DeleteExpired removes a row only while it is still expired at now, and returns
// domain.ErrNotFound otherwise: an expiry decision from an old snapshot never deletes a note that
// was extended. A read of an expired note and a Create with a slug conflict both use it.
// Each method that compares times gets now from Manager and does not read the clock itself.
//
// Update, Delete, Consume and SetBurnExpiry change the row only while its edit_code still equals
// the stored hash of the note that the caller checked: note.EditCode for Update, storedEditCode
// for the others. Each note gets a hash with a new random salt and no write changes it, so the
// hash identifies one note version. If the note was deleted and a new note took the same ID after
// the check, these methods return domain.ErrNotFound and do not touch the new note. Update also
// changes the row only while it is not expired at now, as files.Storage.Update does: a note that
// expired after the check of Manager.Update gets domain.ErrNotFound, and its expiry stays.
type Storage interface {
	Create(ctx context.Context, note *domain.Note) error
	Get(ctx context.Context, id string) (*domain.Note, error)
	Consume(
		ctx context.Context, id string, expected domain.Privacy, storedEditCode string, now time.Time,
	) (*domain.Note, error)
	SetBurnExpiry(
		ctx context.Context, id string, expiresAt time.Time, expected domain.Privacy, storedEditCode string,
	) (*domain.Note, error)
	Update(ctx context.Context, id string, note *domain.Note, now time.Time) error
	Delete(ctx context.Context, id, storedEditCode string) error
	DeleteExpired(ctx context.Context, id string, now time.Time) error
	IncrementViews(ctx context.Context, id string) error
}

// Renderer converts note content to safe HTML. Render handles markdown; RenderPlain handles
// domain.ContentTypePlain notes (escape + wrap, no markdown parsing). Both live behind this one
// port so the HTML-specific mechanics (escaping, sanitizing, wrapping) stay in the adapter that
// implements it (internal/infra/render) — Manager only decides which content type calls for which.
// Strategy: fail-hard; an error means the note cannot be shown. An image that the sanitizer
// removes is not an error: Render reports a reason for it in droppedImages, and the page shows
// the note without that image.
type Renderer interface {
	Render(content string) (rendered string, droppedImages []string, err error)
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

// ExpiredSlugReleaser removes an expired note, link or file that still holds the record ID id,
// because no read and no sweep removed it yet. It returns true when it removed a record. It must
// remove a record only while it is still expired at now, so a record that its owner extended
// stays. Errors are fail-soft: Create keeps its slug conflict and logs the error.
type ExpiredSlugReleaser interface {
	ReleaseExpiredSlug(ctx context.Context, id string, now time.Time) (bool, error)
}

// Deps holds the dependencies of Manager. All fields are mandatory.
type Deps struct {
	Storage      Storage
	Renderer     Renderer
	Encryptor    Encryptor
	Hasher       EditCodeHasher
	ExpiredSlugs ExpiredSlugReleaser
	Log          *slog.Logger
	// Now is the time source of each expiry decision and timestamp. The composition root gives
	// time.Now; tests give a fixed clock.
	Now func() time.Time
}

// maxSlugRetries limits the attempts with a new random slug after a slug conflict. A custom slug
// gets no new slug: the caller chose that exact value.
const maxSlugRetries = 5

// Manager implements note business logic: CRUD, slug resolution, and encryption coordination.
// Burn-after-reading / on-read expiry policy is delegated to burn (see burn.go) so that state
// machine stays independently readable and testable instead of growing inline here.
type Manager struct {
	storage      Storage
	renderer     Renderer
	encryptor    Encryptor
	hasher       EditCodeHasher
	expiredSlugs ExpiredSlugReleaser
	log          *slog.Logger
	now          func() time.Time
	burn         *burner
	// renderCache defaults to noRenderCache; attach a real one with WithRenderCache.
	renderCache RenderCache
	// allowCustomSlugs gates user-supplied (human-readable) slugs. When false, custom slugs are
	// rejected and only server-generated random slugs are issued.
	allowCustomSlugs domain.CustomSlugSetting
	// maxContentLength is the content limit in characters (WithMaxContentLength).
	maxContentLength int
}

// NewManager creates a new Manager. allowCustomSlugs enables user-supplied slugs; when false, a
// create request carrying a slug is rejected with domain.ErrCustomSlugDisabled (custom public
// URLs are easier to guess).
func NewManager(deps *Deps, allowCustomSlugs bool) *Manager {
	return &Manager{
		storage:          deps.Storage,
		renderer:         deps.Renderer,
		encryptor:        deps.Encryptor,
		hasher:           deps.Hasher,
		expiredSlugs:     deps.ExpiredSlugs,
		log:              deps.Log,
		now:              deps.Now,
		burn:             newBurner(deps),
		renderCache:      noRenderCache{},
		allowCustomSlugs: domain.CustomSlugSetting(allowCustomSlugs),
		maxContentLength: domain.DefaultMaxContentLength,
	}
}

// WithMaxContentLength sets the note content limit in characters (--max-content-length). Call it
// at composition time.
func (m *Manager) WithMaxContentLength(limit int) *Manager {
	m.maxContentLength = limit

	return m
}

// WithRenderCache attaches a RenderCache for rendered note HTML. The default caches nothing, so
// behaviour is identical without one. Called by the composition root; do not call after the
// server starts serving requests.
func (m *Manager) WithRenderCache(cache RenderCache) *Manager {
	m.renderCache = cache

	return m
}

// validateNote runs Note.Validate and checks the content length against the configured limit.
func validateNote(note *domain.Note, maxContentLength int) error {
	err := note.Validate()
	if err == nil {
		err = note.ValidateContentLength(maxContentLength)
	}

	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	return nil
}

// validateForCreate runs Note.Validate, the ownership invariant (Note.ValidateOwnership) and,
// when the caller chose one, the edit-code policy (domain.ValidateEditCode) — before anything is
// resolved, hashed or persisted. An empty EditCode is not validated: Create generates it.
// OwnerID is already final at this point (set by the HTTP layer before Create is called), unlike
// Update, where the same ownership check must wait until after OwnerID is pinned from the
// existing note — see the call site in Update for why it cannot share this helper. Update never
// accepts a replacement code, so the edit-code policy only ever applies here.
func validateForCreate(note *domain.Note, maxContentLength int) error {
	err := validateNote(note, maxContentLength)
	if err != nil {
		return err
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

	// An old client does not send the option, so a new note keeps the full-width view.
	if note.ShowContents == nil {
		note.ShowContents = new(false)
	}

	err := validateForCreate(note, m.maxContentLength)
	if err != nil {
		return nil, err
	}

	// The slug check goes before the memory-hard edit-code hash, so an invalid slug costs little.
	slug, err := domain.ResolveSlug(note.ID, m.allowCustomSlugs)
	if err != nil {
		return nil, fmt.Errorf("resolve slug: %w", err)
	}

	plainCode, hashedCode, err := m.resolveEditCodeHash(note.EditCode)
	if err != nil {
		return nil, err
	}

	now := m.now().UTC()

	slug, err = m.store(ctx, note, slug, storedCode{hash: hashedCode, at: now})
	if err != nil {
		return nil, err
	}

	note.ID = slug
	note.EditCode = plainCode
	note.CreatedAt = now
	note.UpdatedAt = now

	m.log.DebugContext(ctx, "note created", "id", domain.HashSlug(slug))

	return note, nil
}

// Peek fetches a note by ID without any access rule: no privacy check, no expiry, no burn and
// no view count. It returns expired notes as they are. A caller that shows a note to a reader
// uses Read, ReadRendered or Inspect, which apply the rules.
func (m *Manager) Peek(ctx context.Context, id string) (*domain.Note, error) {
	note, err := m.storage.Get(ctx, domain.HashSlug(id))
	if err != nil {
		return nil, fmt.Errorf("peek note: %w", err)
	}

	note.ID = id

	err = decryptContent(ctx, m.encryptor, m.log, note)
	if err != nil {
		return nil, err
	}

	return note, nil
}

// Inspect returns the note for reader without a side effect on a live note. It applies the
// first two read rules of Read: privacy (domain.ErrForbidden) and expiry (an expired note is
// deleted and Inspect returns domain.ErrExpired). It never consumes a burn-after-reading note,
// never starts its timer and never counts a view. The editor and the burn confirmation page use it.
func (m *Manager) Inspect(ctx context.Context, id string, reader domain.Reader) (*domain.Note, error) {
	note, err := m.storage.Get(ctx, domain.HashSlug(id))
	if err != nil {
		return nil, fmt.Errorf("inspect note: %w", err)
	}

	note.ID = id

	if !note.VisibleTo(reader) {
		return nil, domain.ErrForbidden
	}

	err = m.burn.enforceExpiry(ctx, id, note)
	if err != nil {
		return nil, err
	}

	err = decryptContent(ctx, m.encryptor, m.log, note)
	if err != nil {
		return nil, err
	}

	return note, nil
}

// Read returns the note for reader and applies all read rules in this order:
//
//  1. Privacy: a reader that cannot see the note gets domain.ErrForbidden.
//  2. Expiry: an expired note is deleted, and Read returns domain.ErrExpired.
//  3. Burn intent: a burn-after-reading note needs reader.AllowBurn; otherwise Read returns
//     domain.ErrBurnIntentRequired and the note stays.
//  4. Burn: an immediate burn note is consumed; a note with a burn TTL starts its timer.
//  5. Views: the view counter goes up, except for a note that this read consumed.
//
// Storage checks the privacy level and the edit-code hash again when it consumes the note or
// starts the timer, so a change after the checks above cannot burn a different note version.
func (m *Manager) Read(ctx context.Context, id string, reader domain.Reader) (*domain.Note, error) {
	note, err := m.Inspect(ctx, id, reader)
	if err != nil {
		return nil, err
	}

	if note.BurnAfterReading && !reader.AllowBurn {
		return nil, domain.ErrBurnIntentRequired
	}

	if note.BurnAfterReading {
		note, err = m.burn.burnNote(ctx, id, note)
		if err != nil {
			return nil, err
		}
	}

	m.burn.applyViewIncrement(ctx, id, note)

	return note, nil
}

// pinOwner carries forward OwnerID, which Update must never let the caller change (it is fixed at
// creation). Only once OwnerID is pinned to its real value does ValidateOwnership's check become
// meaningful: the upfront Validate() call in Update runs before this pin, when the caller-supplied
// OwnerID is always nil regardless of actual ownership, so it cannot reject privacy=owner on a
// note that never had one — this can. The stored edit-code hash goes only to the storage copy.
func pinOwner(note, existing *domain.Note) error {
	note.OwnerID = existing.OwnerID

	err := note.ValidateOwnership()
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	return nil
}

// requestedFields holds the optional update fields that the caller sent. A nil value keeps the
// stored column, because storage writes it through COALESCE.
type requestedFields struct {
	privacy      *domain.Privacy
	contentType  *domain.ContentType
	showContents *bool
}

// requestedOf records the optional fields of note before Update fills them from storage.
func requestedOf(note *domain.Note) requestedFields {
	return requestedFields{privacy: note.Privacy, contentType: note.ContentType, showContents: note.ShowContents}
}

// withRequested returns a copy of note to write with the optional fields that the caller sent.
func withRequested(note *domain.Note, requested requestedFields) *domain.Note {
	write := *note
	write.Privacy = requested.privacy
	write.ContentType = requested.contentType
	write.ShowContents = requested.showContents

	return &write
}

// backfillPersistedState copies the storage-persisted content type, privacy, contents-sidebar
// option and view count into the working note before the update is written.
func backfillPersistedState(note, existing *domain.Note) {
	if note.ContentType == nil {
		note.ContentType = existing.ContentType
	}

	if note.Privacy == nil {
		note.Privacy = existing.Privacy
	}

	if note.ShowContents == nil {
		note.ShowContents = existing.ShowContents
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

// Update replaces a note's editable fields and keeps its immutable metadata. The rules are in
// checkUpdate: the reader must see the note, the note must not be expired, and the reader must
// give the note's edit code or be its owner, so that an authenticated creator (session or API
// token) edits without the code. The check uses the same stored note that the write changes: the
// write names that note version by its stored edit-code hash.
func (m *Manager) Update(
	ctx context.Context, id, editCode string, reader domain.Reader, note *domain.Note,
) (*domain.Note, error) {
	err := validateNote(note, m.maxContentLength)
	if err != nil {
		return nil, err
	}

	dbID := domain.HashSlug(id)

	existing, err := m.storage.Get(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	now := m.now()

	err = m.checkUpdate(existing, editCode, reader, now)
	if err != nil {
		return nil, err
	}

	// Omitted privacy, content type and contents-sidebar option go to storage as nil, so COALESCE
	// keeps the stored values. Writing the values of the existing snapshot back would undo a
	// concurrent change, such as another update that has just made the note owner-only.
	requested := requestedOf(note)

	note.ID = id
	note.CreatedAt = existing.CreatedAt
	note.UpdatedAt = now.UTC()

	resolveExpiryTime(note, existing)

	// Privacy is absent from the UPDATE column list (COALESCE keeps the stored value when the
	// caller omitted it) and views are never writable, so the response backfills both from the
	// existing note: a PUT on a privacy=owner note must not answer "public" with "views": 0.
	// Doing it here (before pinOwner) also lets ValidateOwnership judge the real privacy.
	backfillPersistedState(note, existing)

	err = pinOwner(note, existing)
	if err != nil {
		return nil, err
	}

	encrypted, err := m.encryptor.Encrypt(note.Content, dbID)
	if err != nil {
		return nil, fmt.Errorf("encrypt content: %w", err)
	}

	// write is a separate value with the storage form: the ciphertext and the stored edit-code
	// hash. The hash makes the UPDATE change only the note checked above, not a new note that
	// took this ID after the check. The caller's note keeps the plaintext.
	write := withRequested(note, requested)
	write.Content = encrypted
	write.EditCode = existing.EditCode

	err = m.storage.Update(ctx, dbID, write, now)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	m.renderCache.Delete(dbID)

	m.log.DebugContext(ctx, "note updated", "id", dbID)

	note.EditCode = editCode

	return note, nil
}

// Delete removes a note by ID with the rules of Update (see checkMutation).
func (m *Manager) Delete(ctx context.Context, id, editCode string, reader domain.Reader) error {
	dbID := domain.HashSlug(id)

	existing, err := m.storage.Get(ctx, dbID)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}

	err = m.checkMutation(existing, editCode, reader)
	if err != nil {
		return err
	}

	// The stored hash limits the DELETE to the note checked above, not a new note with this ID.
	err = m.storage.Delete(ctx, dbID, existing.EditCode)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}

	m.renderCache.Delete(dbID)

	return nil
}

// ReadRendered is Read that also returns the content as safe HTML.
func (m *Manager) ReadRendered(ctx context.Context, id string, reader domain.Reader) (*domain.Note, string, error) {
	note, err := m.Read(ctx, id, reader)
	if err != nil {
		return nil, "", err
	}

	rendered, err := m.renderNote(ctx, id, note)
	if err != nil {
		return nil, "", err
	}

	return note, rendered, nil
}

// checkUpdate applies the rules of an update to the stored note existing, in the sequence of
// files.Manager.Update: privacy (domain.ErrForbidden), then expiry (domain.ErrExpired), then the
// rules of checkMutation. Privacy comes first, so that a hidden note gives the same answer in each
// state. An expired note is already gone for readers, so an update must not extend its life.
func (m *Manager) checkUpdate(existing *domain.Note, editCode string, reader domain.Reader, now time.Time) error {
	if !existing.VisibleTo(reader) {
		return domain.ErrForbidden
	}

	if existing.Expired(now) {
		return domain.ErrExpired
	}

	return m.checkMutation(existing, editCode, reader)
}

// checkMutation applies the rules of a change to the stored note existing: the reader must see
// the note (domain.ErrForbidden), and must be its owner or give its edit code
// (domain.ErrInvalidEditCode). An edit code never opens a note that privacy hides from the reader.
func (m *Manager) checkMutation(existing *domain.Note, editCode string, reader domain.Reader) error {
	if !existing.VisibleTo(reader) {
		return domain.ErrForbidden
	}

	if !existing.OwnedBy(reader.CallerID) && !m.hasher.Verify(existing.EditCode, editCode) {
		return domain.ErrInvalidEditCode
	}

	return nil
}

// resolveEditCodeHash returns the plaintext edit code and its stored hash. It generates a code
// when the caller gave none. Generated codes are high-entropy, so they take the fast hash; a
// caller-supplied code takes the memory-hard variant because its entropy is not guaranteed.
func (m *Manager) resolveEditCodeHash(code string) (plain, hashed string, err error) {
	if code != "" {
		hashed, err = m.hasher.HashUserProvided(code)
		if err != nil {
			return "", "", fmt.Errorf("hash edit code: %w", err)
		}

		return code, hashed, nil
	}

	plain, err = domain.NewEditCode()
	if err != nil {
		return "", "", fmt.Errorf("generate edit code: %w", err)
	}

	hashed, err = m.hasher.Hash(plain)
	if err != nil {
		return "", "", fmt.Errorf("hash edit code: %w", err)
	}

	return plain, hashed, nil
}

// maxCachedRenderBytes keeps one huge note from pinning megabytes of HTML in the cache.
const maxCachedRenderBytes = 256 * 1024

// renderNote converts note content to safe HTML, going through the render cache for notes that
// can be re-read: a burn-after-reading note is destroyed by the read that renders it, so a cached
// copy would only keep its plaintext alive in memory after the burn.
// The error names the note by domain.HashSlug(id), never the plaintext slug: the slug is the
// note's capability URL and this error ends up in server logs.
func (m *Manager) renderNote(ctx context.Context, id string, note *domain.Note) (string, error) {
	dbID := domain.HashSlug(id)
	cacheable := !note.BurnAfterReading && note.BurnTTL == 0

	if cacheable {
		cached, ok := m.renderCache.Get(dbID, note.UpdatedAt)
		if ok {
			return cached, nil
		}
	}

	rendered, err := m.renderContent(ctx, dbID, note)
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
func (m *Manager) renderContent(ctx context.Context, dbID string, note *domain.Note) (string, error) {
	if note.ContentType != nil && *note.ContentType == domain.ContentTypePlain {
		rendered, err := m.renderer.RenderPlain(note.Content)
		if err != nil {
			return "", fmt.Errorf("render note %s: %w", dbID, err)
		}

		return rendered, nil
	}

	rendered, droppedImages, err := m.renderer.Render(note.Content)
	if err != nil {
		return "", fmt.Errorf("render note %s: %w", dbID, err)
	}

	if len(droppedImages) > 0 {
		m.log.WarnContext(ctx, "note images not shown",
			"op", "notes.render", "note", dbID, "reasons", countReasons(droppedImages))
	}

	return rendered, nil
}

// countReasons counts each reason, so one log record stays short for a note with many images.
func countReasons(reasons []string) map[string]int {
	counts := make(map[string]int, len(reasons))
	for _, reason := range reasons {
		counts[reason]++
	}

	return counts
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

// storedCode holds the values that every storage attempt of one Create shares.
type storedCode struct {
	at   time.Time
	hash string
}

// store writes the storage form of note under the slug first and returns the slug that storage
// accepted. Storage.Create is the only uniqueness check.
// A random slug that is taken gets a new random slug, at most maxSlugRetries times. A custom slug
// that is taken stays taken, except when an expired record holds it: then store removes that
// record and tries once more. Thus the address of an expired record does not depend on a read
// after the expiry. The storage triggers check the slug again, so the second attempt is safe
// across server processes.
func (m *Manager) store(ctx context.Context, note *domain.Note, first string, code storedCode) (string, error) {
	custom, slug := note.ID != "", first

	for range maxSlugRetries {
		recordID := domain.HashSlug(slug)

		encrypted, err := m.encryptor.Encrypt(note.Content, recordID)
		if err != nil {
			return "", fmt.Errorf("encrypt content: %w", err)
		}

		// stored is a separate value with the storage form of the note: the hashed slug, the
		// ciphertext and the edit-code hash. The caller's note never holds these values.
		stored := *note
		stored.ID = recordID
		stored.Content = encrypted
		stored.EditCode = code.hash
		stored.CreatedAt = code.at
		stored.UpdatedAt = code.at

		err = m.storage.Create(ctx, &stored)
		if custom && errors.Is(err, domain.ErrSlugConflict) && m.releaseExpiredHolder(ctx, recordID, code.at) {
			err = m.storage.Create(ctx, &stored)
		}

		if err == nil {
			return slug, nil
		}

		if custom || !errors.Is(err, domain.ErrSlugConflict) {
			return "", fmt.Errorf("store note: %w", err)
		}

		slug, err = domain.ResolveSlug("", m.allowCustomSlugs)
		if err != nil {
			return "", fmt.Errorf("resolve slug: %w", err)
		}
	}

	return "", domain.ErrHashCollision
}

// releaseExpiredHolder reports whether an expired note, link or file with id was removed. Errors
// are fail-soft: Create keeps its slug conflict.
func (m *Manager) releaseExpiredHolder(ctx context.Context, id string, now time.Time) bool {
	released, err := m.expiredSlugs.ReleaseExpiredSlug(ctx, id, now)
	if err != nil {
		m.log.WarnContext(ctx, "release expired slug", "id", id, "err", err)

		return false
	}

	if released {
		// A note that had this ID can have cached HTML; the new record must not keep it alive.
		m.renderCache.Delete(id)
		m.log.DebugContext(ctx, "expired record released its slug", "id", id)
	}

	return released
}
