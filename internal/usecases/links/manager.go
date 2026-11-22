//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=links -write_package_comment=false

package links

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// maxHashRetries bounds random-slug collision retries in Create — a bounded loop, never an
// unbounded goto/loop. Custom aliases never retry: a collision there is an immediate
// ErrSlugConflict, since the caller chose that exact value.
const maxHashRetries = 5

// Storage defines persistence operations for links. Create must atomically claim the ID
// across notes and links, returning domain.ErrSlugConflict if another resource owns it.
// Update writes only the fields that patch sets and returns the row as stored, so a concurrent
// change of a field that this update omits stays.
// DeleteExpired removes a row only while it is still expired, and returns domain.ErrNotFound
// otherwise: an expiry decision from an old snapshot never deletes a link that its owner
// extended, or a live link that took the ID after the decision. Exists first removes an expired
// link in the same way, so an expired link does not hold its slug.
//
// Update and Delete change the row only while its edit_code still equals storedEditCode, the
// stored hash of the link that the caller checked. Each link gets a hash with a new random salt
// and no write changes it, so the hash identifies one link version. If the link was deleted and
// a new link took the same ID after the check, they return domain.ErrNotFound and do not touch
// the new link.
type Storage interface {
	Create(ctx context.Context, link *domain.Link) error
	Update(ctx context.Context, id, storedEditCode string, patch *domain.LinkPatch) (*domain.Link, error)
	GetByID(ctx context.Context, id string) (*domain.Link, error)
	Exists(ctx context.Context, id string) (bool, error)
	IncrementViews(ctx context.Context, id string) error
	Delete(ctx context.Context, id, storedEditCode string) error
	DeleteExpired(ctx context.Context, id string) error
	Consume(ctx context.Context, id string) (*domain.Link, error)
	// ConsumeVisible atomically checks current visibility and expiry/burn eligibility before
	// deleting and returning a row. allowBurn=false permits only expired rows.
	ConsumeVisible(
		ctx context.Context, id string, callerID uuid.UUID, authenticated, allowBurn bool,
	) (*domain.Link, error)
	// IncrementViewsVisible refuses a row that is no longer visible to this reader.
	IncrementViewsVisible(ctx context.Context, id string, callerID uuid.UUID, authenticated bool) error
}

// NotesChecker lets links.Manager verify a candidate slug isn't already taken by a note — slugs
// are globally unique across notes and links. See notes.Manager's mirrored LinksChecker, which
// enforces the same rule in the other direction.
type NotesChecker interface {
	Exists(ctx context.Context, id string) (bool, error)
}

// SlugLock serializes local preflight checks in resolveUniqueSlug. Share the same instance
// with notes.Manager. Storage independently enforces cross-resource uniqueness between
// server processes. Declared here rather than imported from notes: usecases depend only on
// domain and stdlib.
// Updates and target-cache fills also share this lock to coordinate local eviction.
type SlugLock interface {
	Lock(ctx context.Context, key string) (unlock func(), err error)
}

// Encryptor writes content using installation keys and the stored record ID as authenticated
// context. legacySlug is only for reading old records; new ciphertext needs no slug to decrypt.
// The interface lives at its consumer to keep the usecase independent of crypto infrastructure.
type Encryptor interface {
	Encrypt(plaintext, recordID string) (string, error)
	Decrypt(ciphertext, recordID, legacySlug string) (string, error)
}

// EditCodeHasher hashes and verifies link edit codes. Hash is for server-generated
// high-entropy codes; HashUserProvided is for caller-chosen codes, which need a
// memory-hard hash because their entropy is not guaranteed.
type EditCodeHasher interface {
	Hash(code string) (string, error)
	HashUserProvided(code string) (string, error)
	Verify(storedHash, code string) bool
}

// Cache stores only decrypted targets, bound to the current stored ciphertext.
// Metadata and authorization always come from storage, including on cache hits.
// Get must miss when ciphertext differs so a replica never reuses an old target
// after another replica updates the record.
type Cache interface {
	Get(id, ciphertext string) (string, bool)
	Set(ctx context.Context, id, ciphertext, targetURL string)
	Delete(id string)
}

// Metrics records business events for operational visibility — see WithMetrics. Metrics are
// fail-soft: the methods return no errors. Each method corresponds to one counter:
//   - IncLinksCreated: one successful Manager.Create.
//   - IncRedirects: one successful Manager.Follow (normal or burn-after-click; error paths —
//     not found, expired, denied — do not count, matching "requests actually served").
//   - IncCollisions: one random-slug retry inside resolveUniqueSlug hitting an already-taken
//     slug. Deliberately scoped to the random-generation retry case only, not a rejected
//     custom-alias request — a collision here is a signal about slug-space health (retries
//     climbing over time would mean the keyspace is filling up), whereas a user picking an
//     already-taken alias is normal, expected behavior with nothing operational to signal.
type Metrics interface {
	IncLinksCreated()
	IncRedirects()
	IncCollisions()
}

// noopMetrics is the default Metrics. It records nothing, so Manager needs no nil checks.
type noopMetrics struct{}

func (noopMetrics) IncLinksCreated() {}
func (noopMetrics) IncRedirects()    {}
func (noopMetrics) IncCollisions()   {}

// ExpiredFileReleaser removes an expired file that still holds a slug, because the file sweep
// did not run yet. files.Manager implements it; the default releases nothing. Errors are
// fail-soft: Create keeps its slug conflict and logs the error.
type ExpiredFileReleaser interface {
	// ReleaseExpiredSlug removes the expired file with the record ID id. It returns true when
	// it removed the file.
	ReleaseExpiredSlug(ctx context.Context, id string) (bool, error)
}

// noExpiredFiles is the default ExpiredFileReleaser, for a server without file publishing.
type noExpiredFiles struct{}

func (noExpiredFiles) ReleaseExpiredSlug(context.Context, string) (bool, error) { return false, nil }

// Manager implements short-link business logic: URL validation, slug resolution, and encryption
// coordination — the links analogue of notes.Manager.
type Manager struct {
	storage          Storage
	notes            NotesChecker
	slugLock         SlugLock
	encryptor        Encryptor
	hasher           EditCodeHasher
	cache            Cache
	metrics          Metrics
	log              *slog.Logger
	expiredFiles     ExpiredFileReleaser
	allowCustomSlugs domain.CustomSlugSetting
}

// NewManager creates a new Manager with required dependencies. allowCustomSlugs enables
// user-supplied slugs (aliases); when false, a create request carrying one is rejected with
// domain.ErrCustomSlugDisabled — mirrors notes.NewManager, and both share the same server-wide
// --custom-slugs flag. slugLock must be the same instance passed to the paired notes.Manager —
// see SlugLock's doc comment for why. cache and metrics are optional collaborators attached via
// WithCache/WithMetrics at composition time: cache defaults to nil (no cache, and no slug lock on
// reads — see resolveLink), metrics to noopMetrics.
// All fields of Manager are explicitly initialised here so a future field added to the struct
// cannot silently land in an undefined state — the compiler forces every caller through this
// constructor.
func NewManager(
	storage Storage, notes NotesChecker, slugLock SlugLock, encryptor Encryptor, hasher EditCodeHasher,
	log *slog.Logger, allowCustomSlugs bool,
) *Manager {
	return &Manager{
		storage:          storage,
		notes:            notes,
		slugLock:         slugLock,
		encryptor:        encryptor,
		hasher:           hasher,
		cache:            nil,
		metrics:          noopMetrics{},
		log:              log,
		expiredFiles:     noExpiredFiles{},
		allowCustomSlugs: domain.CustomSlugSetting(allowCustomSlugs),
	}
}

// WithExpiredFiles attaches the file manager, so that an expired file that the sweep did not
// remove yet does not block a link with the same slug. Call it at composition time.
func (m *Manager) WithExpiredFiles(releaser ExpiredFileReleaser) *Manager {
	m.expiredFiles = releaser

	return m
}

// WithCache attaches an optional Cache for frequently-followed links. Nil (the default — Manager
// never sets m.cache itself) disables caching entirely: every code path already nil-checks it, so
// behavior is identical with or without one — this is a pure performance optimization, never a
// correctness dependency. Mirrors Handler.WithRevealStore's optional-collaborator shape on the
// HTTP side. Called by the composition root; do not call after the server starts serving requests.
func (m *Manager) WithCache(cache Cache) *Manager {
	m.cache = cache

	return m
}

// WithMetrics attaches Metrics recording; the default records nothing. Give a value that is not
// nil. Called by the composition root; do not call after the server starts serving requests.
func (m *Manager) WithMetrics(metrics Metrics) *Manager {
	m.metrics = metrics

	return m
}

// CreateOptions carries Create parameters that aren't the target URL itself.
type CreateOptions struct {
	ExpiresAt *time.Time
	Privacy   *domain.Privacy
	OwnerID   *uuid.UUID
	// DirectRedirect overrides the server-wide --direct-redirect default for this one link; nil
	// inherits it. See domain.Link.EffectiveDirectRedirect.
	DirectRedirect *bool
	CustomHash     string
	EditCode       string
	BurnAfterClick bool
}

// validateTargetURL enforces the http/https scheme whitelist: rejects javascript:, data:,
// file:, any other scheme, schemeless/relative values, and URLs with no host. Returns the
// normalized (re-serialized) URL string.
func validateTargetURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", domain.ErrInvalidURL
	}

	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", domain.ErrInvalidURL
	}

	if parsed.Host == "" {
		return "", domain.ErrInvalidURL
	}

	return parsed.String(), nil
}

// validateLink checks fields and ownership after OwnerID has been fixed by Create
// or inherited from the existing record by Update.
func validateLink(link *domain.Link) error {
	err := link.Validate()
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	err = link.ValidateOwnership()
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	return nil
}

// Create validates a target URL and persists a new short link, returning it with the plaintext
// target URL and plaintext edit code populated — both are stored encrypted/hashed. Mirrors
// notes.Manager.Create closely. Link slug resolution retries randomly generated candidates
// rejected by preflight; a collision during the final atomic insert returns ErrSlugConflict.
func (m *Manager) Create(ctx context.Context, targetURL string, opts CreateOptions) (*domain.Link, error) {
	validURL, err := validateTargetURL(targetURL)
	if err != nil {
		return nil, fmt.Errorf("validate target url: %w", err)
	}

	link := &domain.Link{
		CreatedAt:      time.Time{},
		ExpiresAt:      opts.ExpiresAt,
		Privacy:        opts.Privacy,
		OwnerID:        opts.OwnerID,
		DirectRedirect: opts.DirectRedirect,
		ID:             "",
		TargetURL:      "",
		EditCode:       opts.EditCode,
		Views:          0,
		BurnAfterClick: opts.BurnAfterClick,
	}

	err = validateLink(link)
	if err != nil {
		return nil, fmt.Errorf("validate link: %w", err)
	}

	// A caller-chosen code must meet the edit-code policy before it is hashed; an empty one is
	// generated below. Update never accepts a replacement code, so this is the only check site.
	if link.EditCode != "" {
		err = domain.ValidateEditCode(link.EditCode)
		if err != nil {
			return nil, fmt.Errorf("validate edit code: %w", err)
		}
	}

	plaintextCode, err := m.resolveAndHashEditCode(link)
	if err != nil {
		return nil, fmt.Errorf("resolve and hash edit code: %w", err)
	}

	link.CreatedAt = time.Now().UTC()

	slug, unlock, err := m.resolveUniqueSlug(ctx, opts.CustomHash)
	if err != nil {
		return nil, fmt.Errorf("resolve unique slug: %w", err)
	}
	defer unlock()

	err = m.persistLink(ctx, link, slug, validURL, plaintextCode)
	if err != nil {
		return nil, fmt.Errorf("persist link: %w", err)
	}

	m.log.DebugContext(ctx, "link created", "id", domain.HashSlug(slug))

	m.metrics.IncLinksCreated()

	return link, nil
}

// decryptTargetURL authenticates the record ID and resource type before decrypting.
// The slug is supplied separately only for legacy records. Errors map to ErrNotFound
// as on the note path, so callers cannot use key failures as an existence oracle.
func decryptTargetURL(ctx context.Context, enc Encryptor, log *slog.Logger, link *domain.Link) error {
	plaintext, err := enc.Decrypt(link.TargetURL, domain.HashSlug(link.ID), link.ID)
	if err != nil {
		if errors.Is(err, domain.ErrMalformedCiphertext) {
			log.ErrorContext(ctx, "stored target url is corrupt", "id", domain.HashSlug(link.ID), "err", err)
		} else {
			log.WarnContext(ctx, "target url decryption failed", "id", domain.HashSlug(link.ID), "err", err)
		}

		return domain.ErrNotFound
	}

	link.TargetURL = plaintext

	return nil
}

// Peek fetches a link by slug without any access rule: no privacy check, no expiry, no burn and
// no view count. It returns expired and burn links as they are. A caller that shows a link to a
// reader uses Inspect or Follow. Every call reads current metadata from storage. Only target
// decryption may use the optional Cache; cached entries never supply privacy, ownership or
// lifecycle state.
func (m *Manager) Peek(ctx context.Context, slug string) (*domain.Link, error) {
	link, err := m.resolveLink(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("peek link: %w", err)
	}

	return link, nil
}

// Delete removes a link by slug with the rules of checkMutation, as notes.Manager.Delete does.
func (m *Manager) Delete(ctx context.Context, slug, editCode string, reader domain.Reader) error {
	dbID := domain.HashSlug(slug)

	existing, err := m.storage.GetByID(ctx, dbID)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}

	err = m.checkMutation(existing, editCode, reader)
	if err != nil {
		return err
	}

	// The stored hash limits the DELETE to the link checked above, not a new link with this ID.
	err = m.storage.Delete(ctx, dbID, existing.EditCode)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}

	m.evictCache(dbID)

	return nil
}

// checkMutation applies the rules of a change to the stored link existing, as
// notes.Manager does: the reader must see the link (domain.ErrForbidden), and must be its owner
// or give its edit code (domain.ErrInvalidEditCode).
func (m *Manager) checkMutation(existing *domain.Link, editCode string, reader domain.Reader) error {
	if !existing.VisibleTo(reader) {
		return domain.ErrForbidden
	}

	if !existing.OwnedBy(reader.CallerID) && !m.hasher.Verify(existing.EditCode, editCode) {
		return domain.ErrInvalidEditCode
	}

	return nil
}

// persistLink writes link to storage under slug. The storage form is a separate value with the
// hashed slug, the encrypted target URL and the edit-code hash; link itself gets the slug, the
// plaintext target URL and the plaintext edit code only after the write.
func (m *Manager) persistLink(
	ctx context.Context, link *domain.Link, slug, validURL, plaintextCode string,
) error {
	recordID := domain.HashSlug(slug)

	encrypted, err := m.encryptor.Encrypt(validURL, recordID)
	if err != nil {
		return fmt.Errorf("encrypt target url: %w", err)
	}

	stored := *link
	stored.ID = recordID
	stored.TargetURL = encrypted

	err = m.createReleasingExpired(ctx, &stored)
	if err != nil {
		return fmt.Errorf("create link: %w", err)
	}

	link.ID = slug
	link.TargetURL = validURL
	link.EditCode = plaintextCode

	return nil
}

// resolveLink reads the current row before consulting the decrypted-target cache.
// Privacy, ownership, deletion, TTL and burn state must be visible across replicas
// without waiting for local cache expiry. The cache can only reuse a public URL
// when its ciphertext matches this read's snapshot.
func (m *Manager) resolveLink(ctx context.Context, slug string) (*domain.Link, error) {
	dbID := domain.HashSlug(slug)

	if m.cache != nil {
		// Freshness, including across replicas, comes from the ciphertext fingerprint:
		// a stale entry never matches the row read below. This in-process lock is memory
		// hygiene only — Update holds it through persistence and eviction, so a concurrent
		// cache miss cannot re-fill the cache with the plaintext target of a link that was
		// just updated. The cost is serialising reads of one link. (Delete does not take
		// it: a racing read may re-cache a deleted link's target until TTL eviction, but
		// that entry is never served because the row lookup below fails first.)
		unlock, err := m.slugLock.Lock(ctx, dbID)
		if err != nil {
			return nil, fmt.Errorf("lock link: %w", err)
		}
		defer unlock()
	}

	link, err := m.storage.GetByID(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("get link: %w", err)
	}

	link.ID = slug
	ciphertext := link.TargetURL

	cacheable := m.cache != nil && link.EffectivePrivacy() == domain.PrivacyPublic &&
		!link.BurnAfterClick && !link.Expired(time.Now())
	if cacheable {
		if target, ok := m.cache.Get(dbID, ciphertext); ok {
			link.TargetURL = target

			return link, nil
		}
	}

	err = decryptTargetURL(ctx, m.encryptor, m.log, link)
	if err != nil {
		return nil, fmt.Errorf("decrypt target url: %w", err)
	}

	if cacheable {
		m.cache.Set(ctx, dbID, ciphertext, link.TargetURL)
	}

	return link, nil
}

// evictCache removes dbID from the optional Cache, if one is attached. Called after Update/Delete and
// after Redirect deletes an expired link. Other replicas also check storage and
// ciphertext before using their own cache, so correctness does not rely on eviction.
func (m *Manager) evictCache(dbID string) {
	if m.cache != nil {
		m.cache.Delete(dbID)
	}
}

// resolveAndHashEditCode generates a random edit code when link.EditCode is empty, then hashes
// whichever value ends up set (caller-supplied or generated) into link.EditCode in place,
// returning the plaintext so Create can hand it back to the caller once, like notes.Manager.Create.
// Caller-supplied codes are hashed with the memory-hard variant: their entropy is not
// guaranteed, so the stored hash must resist offline brute-force.
func (m *Manager) resolveAndHashEditCode(link *domain.Link) (string, error) {
	userSuppliedCode := link.EditCode != ""

	if link.EditCode == "" {
		editCode, err := domain.NewEditCode()
		if err != nil {
			return "", fmt.Errorf("generate edit code: %w", err)
		}

		link.EditCode = editCode
	}

	plaintext := link.EditCode

	var (
		hashed string
		err    error
	)

	if userSuppliedCode {
		hashed, err = m.hasher.HashUserProvided(link.EditCode)
	} else {
		hashed, err = m.hasher.Hash(link.EditCode)
	}

	if err != nil {
		return "", fmt.Errorf("hash edit code: %w", err)
	}

	link.EditCode = hashed

	return plaintext, nil
}

// resolveUniqueSlug resolves customHash into a slug via domain.ResolveSlug, then confirms it
// isn't already taken by a note or another link (slugs are globally unique across both). A
// custom-alias collision is an immediate ErrSlugConflict — the caller chose that exact value,
// there is nothing to retry. A random-slug collision retries up to maxHashRetries before giving
// up with ErrHashCollision.
//
// On success the returned unlock holds m.slugLock for the winning candidate — the caller must
// call it exactly once, after persistLink (see SlugLock's doc comment for why the lock has to
// stay held past the check). Every candidate rejected during a retry releases its own lock
// before the next attempt, so at most one lock is ever held at a time.
func (m *Manager) resolveUniqueSlug(
	ctx context.Context, customHash string,
) (slug string, unlock func(), err error) {
	// Unlike notes, links are only served at single-segment routes (GET /{id} and GET /r/{id} plus the ogen
	// GET /links/{id} / DELETE /links/{id}), so a multi-segment custom alias would be created
	// successfully yet unroutable and unreachable through the API. Reject it here, at the
	// resource that owns the routing constraint, rather than narrowing the shared
	// domain.ValidCustomSlug that multi-segment notes legitimately rely on.
	if strings.Contains(customHash, "/") {
		return "", nil, fmt.Errorf(
			"custom link slug %q must be a single path segment (links are served at /{id}): %w",
			customHash, domain.ErrInvalidSlug)
	}

	for range maxHashRetries {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return "", nil, fmt.Errorf("resolve unique slug: %w", ctxErr)
		}

		slug, err := domain.ResolveSlug(customHash, m.allowCustomSlugs)
		if err != nil {
			return "", nil, fmt.Errorf("resolve slug: %w", err)
		}

		unlock, lockErr := m.slugLock.Lock(ctx, domain.HashSlug(slug))
		if lockErr != nil {
			return "", nil, fmt.Errorf("lock slug: %w", lockErr)
		}

		taken, err := m.slugTaken(ctx, domain.HashSlug(slug))
		if err != nil {
			unlock()

			return "", nil, fmt.Errorf("slug taken: %w", err)
		}

		if !taken {
			return slug, unlock, nil
		}

		unlock()

		if customHash != "" {
			return "", nil, domain.ErrSlugConflict
		}

		// Reached only for a random-slug retry collision, never a rejected custom alias (that
		// returns above) — see the Metrics interface's doc comment for why the distinction matters.
		m.metrics.IncCollisions()
	}

	return "", nil, domain.ErrHashCollision
}

// slugTaken reports whether dbID (HashSlug(slug)) already names a note or a link.
func (m *Manager) slugTaken(ctx context.Context, dbID string) (bool, error) {
	notesExist, err := m.notes.Exists(ctx, dbID)
	if err != nil {
		return false, fmt.Errorf("check note slug: %w", err)
	}

	if notesExist {
		return true, nil
	}

	linksExist, err := m.storage.Exists(ctx, dbID)
	if err != nil {
		return false, fmt.Errorf("check link slug: %w", err)
	}

	return linksExist, nil
}

// consumeLink deletes the burn link that reader follows and returns it with the plaintext target.
func (m *Manager) consumeLink(ctx context.Context, slug, dbID string, reader domain.Reader) (*domain.Link, error) {
	consumed, err := m.consume(ctx, dbID, reader)
	if err != nil {
		return nil, fmt.Errorf("burn after click: %w", err)
	}

	consumed.ID = slug

	err = decryptTargetURL(ctx, m.encryptor, m.log, consumed)
	if err != nil {
		return nil, fmt.Errorf("burn after click: %w", err)
	}

	m.log.DebugContext(ctx, "link burned", "id", domain.HashSlug(slug))
	m.metrics.IncRedirects()

	return consumed, nil
}

// consume atomically deletes a burn link. Storage checks the privacy of the current row for
// reader in the DELETE; with SkipPrivacy (accounts off) no privacy level applies, so storage
// checks only the burn and expiry rule.
func (m *Manager) consume(ctx context.Context, dbID string, reader domain.Reader) (*domain.Link, error) {
	if reader.SkipPrivacy {
		consumed, err := m.storage.Consume(ctx, dbID)
		if err != nil {
			return nil, fmt.Errorf("consume link: %w", err)
		}

		return consumed, nil
	}

	consumed, err := m.storage.ConsumeVisible(ctx, dbID, reader.CallerID, reader.Authenticated, true)
	if err != nil {
		return nil, fmt.Errorf("consume link: %w", err)
	}

	return consumed, nil
}

// createReleasingExpired calls storage.Create. On a slug conflict, it removes an expired link or
// an expired file with the same ID and tries once more, as notes.Manager does. Thus the address
// of an expired record does not depend on a read after the expiry. The storage triggers check
// the slug again, so the retry is safe across server processes.
func (m *Manager) createReleasingExpired(ctx context.Context, link *domain.Link) error {
	err := m.storage.Create(ctx, link)
	if errors.Is(err, domain.ErrSlugConflict) && m.releaseExpiredSlug(ctx, link.ID) {
		err = m.storage.Create(ctx, link)
	}

	if err != nil {
		return fmt.Errorf("store link: %w", err)
	}

	return nil
}

// releaseExpiredSlug reports whether it removed an expired link or an expired file with id. One
// record at most holds a slug, so the file manager is asked only when no link was removed.
func (m *Manager) releaseExpiredSlug(ctx context.Context, id string) bool {
	return m.releaseExpiredLink(ctx, id) || m.releaseExpiredFile(ctx, id)
}

// releaseExpiredLink reports whether it removed the expired link with id. DeleteExpired checks
// the expiry in the DELETE, so a link that its owner extended after it expired stays. Errors are
// fail-soft: Create keeps its slug conflict.
func (m *Manager) releaseExpiredLink(ctx context.Context, id string) bool {
	err := m.storage.DeleteExpired(ctx, id)
	if err == nil {
		m.log.DebugContext(ctx, "expired link released its slug", "id", id)
		m.evictCache(id)

		return true
	}

	if !errors.Is(err, domain.ErrNotFound) {
		m.log.WarnContext(ctx, "release expired link slug", "id", id, "err", err)
	}

	return false
}

// releaseExpiredFile reports whether the file manager removed an expired file with id.
func (m *Manager) releaseExpiredFile(ctx context.Context, id string) bool {
	released, err := m.expiredFiles.ReleaseExpiredSlug(ctx, id)
	if err != nil {
		m.log.WarnContext(ctx, "release expired file slug", "id", id, "err", err)

		return false
	}

	return released
}
