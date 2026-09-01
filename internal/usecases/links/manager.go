//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=links

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

// Storage defines persistence operations for links.
type Storage interface {
	Create(ctx context.Context, link *domain.Link) error
	GetByID(ctx context.Context, id string) (*domain.Link, error)
	Exists(ctx context.Context, id string) (bool, error)
	IncrementViews(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	Consume(ctx context.Context, id string) (*domain.Link, error)
}

// NotesChecker lets links.Manager verify a candidate slug isn't already taken by a note — slugs
// are globally unique across notes and links. See notes.Manager's mirrored LinksChecker, which
// enforces the same rule in the other direction.
type NotesChecker interface {
	Exists(ctx context.Context, id string) (bool, error)
}

// SlugLock serializes the check-then-create window in resolveUniqueSlug. Notes and links live
// in separate tables (see NotesChecker's doc comment), so the database's own PK constraint only
// protects a same-table collision — a concurrent links.Create and notes.Create targeting the
// same slug would otherwise both observe "not taken" and both write. The concrete
// implementation must be the exact same instance notes.Manager locks against (see notes.
// SlugLock), or this protects nothing. Declared here rather than imported from notes, for the
// same reason as Encryptor above: a usecase package may only import domain + stdlib.
type SlugLock interface {
	Lock(ctx context.Context, key string) (unlock func(), err error)
}

// Encryptor encrypts and decrypts a link's target URL using the slug as key material. Declared
// here, not imported from the notes usecase package: a usecase package may only import domain +
// stdlib, never another usecase package. crypto.New() already satisfies this interface by
// structural typing, so no infra changes are needed to share the one concrete implementation.
type Encryptor interface {
	Encrypt(plaintext, key string) (string, error)
	Decrypt(ciphertext, key string) (string, error)
}

// EditCodeHasher hashes and verifies link edit codes.
type EditCodeHasher interface {
	Hash(code string) (string, error)
	Verify(storedHash, code string) bool
}

// Cache caches decrypted links by their hashed ID (domain.HashSlug) to skip the storage
// round-trip and decrypt for frequently-followed ("hot") links — see WithCache and resolveLink.
// Values passed to Set and returned from Get must be treated as this call's own copy: Manager
// mutates its working copy in place (e.g. incrementing Views), so an implementation sharing the
// same pointer across calls would let that mutation corrupt the cached entry — see
// internal/infra/linkcache's implementation, which clones on both Get and Set for this reason.
type Cache interface {
	Get(id string) (*domain.Link, bool)
	Set(ctx context.Context, id string, link *domain.Link)
	Delete(id string)
}

// Metrics records business events for operational visibility — see WithMetrics. Each method
// corresponds to one counter documented in TODO.md's Phase 3 section:
//   - IncLinksCreated: one successful Manager.Create.
//   - IncRedirects: one successful Manager.Redirect (normal or burn-after-click; error paths —
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
	allowCustomSlugs bool
}

// NewManager creates a new Manager with required dependencies. allowCustomSlugs enables
// user-supplied slugs (aliases); when false, a create request carrying one is rejected with
// domain.ErrCustomSlugDisabled — mirrors notes.NewManager, and both share the same server-wide
// --custom-slugs flag. slugLock must be the same instance passed to the paired notes.Manager —
// see SlugLock's doc comment for why. cache and metrics are optional collaborators that default
// to nil and must be attached via WithCache/WithMetrics at composition time; every code path
// nil-checks them, so a missed attachment leaves caching/metrics disabled (never a nil deref).
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
		metrics:          nil,
		log:              log,
		allowCustomSlugs: allowCustomSlugs,
	}
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

// WithMetrics attaches optional Metrics recording. Nil (the default) disables recording
// entirely: every code path already nil-checks it via the unexported record* helpers below, so
// behavior is identical with or without one — same optional-collaborator shape as WithCache.
// Called by the composition root; do not call after the server starts serving requests.
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

// validateForCreate runs Link.Validate and the ownership invariant (Link.ValidateOwnership).
// OwnerID is already final at this point (set by the HTTP layer before Create is called) —
// mirrors notes' validateForCreate exactly, including why this check is safe here specifically
// (see domain.Link.ValidateOwnership's doc comment for the general timing rule).
func validateForCreate(link *domain.Link) error {
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
// notes.Manager.Create closely; the notable difference is slug resolution needs a bounded retry
// loop here (notes.Manager.Create never retries, since a note's own table is the only place a
// collision can happen — links must also check against notes' table, so a random-slug collision
// is expected to happen occasionally and is retried, not treated as a hard failure immediately).
func (m *Manager) Create(ctx context.Context, targetURL string, opts CreateOptions) (*domain.Link, error) {
	validURL, err := validateTargetURL(targetURL)
	if err != nil {
		return nil, err
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

	err = validateForCreate(link)
	if err != nil {
		return nil, err
	}

	plaintextCode, err := m.resolveAndHashEditCode(link)
	if err != nil {
		return nil, err
	}

	link.CreatedAt = time.Now().UTC()

	slug, unlock, err := m.resolveUniqueSlug(ctx, opts.CustomHash)
	if err != nil {
		return nil, err
	}
	defer unlock()

	err = m.persistLink(ctx, link, slug, validURL, plaintextCode)
	if err != nil {
		return nil, err
	}

	m.log.DebugContext(ctx, "link created", "id", slug)

	if m.metrics != nil {
		m.metrics.IncLinksCreated()
	}

	return link, nil
}

// decryptTargetURL decrypts link.TargetURL in place using the slug (link.ID) as the key. Any
// decryption failure means the caller lacks the correct slug (or the stored data is corrupt) —
// either way the link is inaccessible, so it is collapsed to domain.ErrNotFound (avoids an
// existence oracle). Mirrors notes' decryptContent exactly, including the corrupt-vs-wrong-slug
// log-level split.
func decryptTargetURL(ctx context.Context, enc Encryptor, log *slog.Logger, link *domain.Link) error {
	plaintext, err := enc.Decrypt(link.TargetURL, link.ID)
	if err != nil {
		if errors.Is(err, domain.ErrMalformedCiphertext) {
			log.ErrorContext(ctx, "stored target url is corrupt", "id", link.ID, "err", err)
		} else {
			log.WarnContext(ctx, "target url decryption failed (likely wrong slug)", "id", link.ID, "err", err)
		}

		return domain.ErrNotFound
	}

	link.TargetURL = plaintext

	return nil
}

// Peek fetches a link by slug without incrementing views, consuming burn-after-click, or
// applying expiry policy. Used by the HTTP layer's privacy gate before Redirect's side effects,
// and directly by OgenHandler.GetLink (GET /links/{id}'s metadata read) — mirrors
// notes.Manager.Peek exactly, including intentionally returning expired/burn-pending links as-is.
// May be served from the optional Cache — see resolveLink. Same accepted trade-off as Redirect: a
// cache hit's Views can lag the true stored count by up to the cache's TTL (see
// internal/infra/linkcache.DefaultTTL) if other requests incremented it in the meantime — a
// caller checking GET /links/{id} for an exact live count should not assume it's realtime, the
// same way any cache-fronted view/click counter (e.g. link shorteners generally) isn't.
func (m *Manager) Peek(ctx context.Context, slug string) (*domain.Link, error) {
	link, err := m.resolveLink(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("peek link: %w", err)
	}

	return link, nil
}

// Redirect resolves slug to its target URL, applying expiry and burn-after-click policy exactly
// once. Burn-after-click links are atomically consumed (deleted and returned) to prevent races
// — a second concurrent Redirect for the same link sees no row left to consume and gets
// ErrNotFound, giving true "exactly once" semantics. Expired links are deleted and ErrExpired is
// returned. On an ordinary successful redirect the view counter is incremented (skipped for a
// just-consumed burn link, which no longer exists to count) — mirrors notes.Manager.Get/View,
// simplified for links' single burn mode (no TTL grace period, unlike notes' burn-after-reading).
//
// The initial fetch may be served from the optional Cache (see resolveLink) — a hot, frequently
// followed link skips both the storage round-trip and the decrypt on a cache hit. Views is always
// incremented against real storage regardless of cache hit/miss, so the stored count is never
// wrong; only the Views value returned to THIS caller can lag by however many other requests hit
// the cache in between — an accepted trade-off for a "hot links" cache, not a stored-data bug.
func (m *Manager) Redirect(ctx context.Context, slug string) (*domain.Link, error) {
	dbID := domain.HashSlug(slug)

	link, err := m.resolveLink(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("redirect: %w", err)
	}

	if link.Expired(time.Now()) {
		delErr := m.storage.Delete(ctx, dbID)
		if delErr != nil {
			m.log.ErrorContext(ctx, "delete expired link", "id", slug, "err", delErr)
		}

		m.evictCache(dbID)

		return nil, domain.ErrExpired
	}

	if link.BurnAfterClick {
		return m.consumeLink(ctx, slug, dbID)
	}

	incErr := m.storage.IncrementViews(ctx, dbID)
	if incErr != nil {
		m.log.ErrorContext(ctx, "increment link views", "id", slug, "err", incErr)
	} else {
		link.Views++
	}

	if m.metrics != nil {
		m.metrics.IncRedirects()
	}

	return link, nil
}

// Delete removes a link by slug after verifying the edit code, or that the caller is the link's
// owner — mirrors notes.Manager.Delete exactly.
func (m *Manager) Delete(ctx context.Context, slug, editCode string, callerID uuid.UUID) error {
	dbID := domain.HashSlug(slug)

	existing, err := m.storage.GetByID(ctx, dbID)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}

	if !existing.OwnedBy(callerID) && !m.hasher.Verify(existing.EditCode, editCode) {
		return domain.ErrInvalidEditCode
	}

	err = m.storage.Delete(ctx, dbID)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}

	m.evictCache(dbID)

	return nil
}

// persistLink mutates link in place for persistence (encrypt the target URL, store the hashed
// slug as the PK) and writes it to storage. The defer restores the caller-facing plaintext
// fields and the slug ID on every return path, so no branch — including a failed storage.Create
// — can leave the link in an encrypted state.
func (m *Manager) persistLink(
	ctx context.Context, link *domain.Link, slug, validURL, plaintextCode string,
) error {
	defer func() {
		link.ID = slug
		link.TargetURL = validURL
		link.EditCode = plaintextCode
	}()

	encrypted, encErr := m.encryptor.Encrypt(validURL, slug)
	if encErr != nil {
		return fmt.Errorf("encrypt target url: %w", encErr)
	}

	link.TargetURL = encrypted
	link.ID = domain.HashSlug(slug)

	err := m.storage.Create(ctx, link)
	if err != nil {
		return fmt.Errorf("create link: %w", err)
	}

	return nil
}

// resolveLink fetches and decrypts a link by dbID, consulting the optional Cache first — a hit
// skips both the storage round-trip and the decrypt. Only a link that is neither burn-after-click
// nor already expired is cached (the Set call below):
//   - caching a burn-after-click link would risk a second, concurrent request being served a
//     stale "not yet consumed" copy instead of relying on storage.Consume's atomicity;
//   - caching an already-expired link would be pure churn — Redirect is about to delete it and
//     evict the cache again (see evictCache) the moment it checks ExpiresAt, so populating the
//     cache here first just to immediately invalidate it serves no purpose.
//
// See the Cache interface's doc comment for the cloning contract this relies on.
//
// link.ID must be pinned to the plaintext slug BEFORE decryptTargetURL runs, not after — the
// slug is the decryption key material (see decryptTargetURL/domain.Link's doc comment), and
// storage.GetByID returns the row with ID still set to the hashed dbID. Pinning it late would
// decrypt with the wrong key and every call would collapse to domain.ErrNotFound.
func (m *Manager) resolveLink(ctx context.Context, slug string) (*domain.Link, error) {
	dbID := domain.HashSlug(slug)

	if m.cache != nil {
		if cached, ok := m.cache.Get(dbID); ok {
			return cached, nil
		}
	}

	link, err := m.storage.GetByID(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("get link: %w", err)
	}

	link.ID = slug

	err = decryptTargetURL(ctx, m.encryptor, m.log, link)
	if err != nil {
		return nil, err
	}

	expired := link.ExpiresAt != nil && time.Now().After(*link.ExpiresAt)
	if m.cache != nil && !link.BurnAfterClick && !expired {
		m.cache.Set(ctx, dbID, link)
	}

	return link, nil
}

// evictCache removes dbID from the optional Cache, if one is attached. Called after Delete and
// after Redirect deletes an expired link, so a subsequent request can never be served a stale hit
// for a link that no longer exists in storage.
func (m *Manager) evictCache(dbID string) {
	if m.cache != nil {
		m.cache.Delete(dbID)
	}
}

// resolveAndHashEditCode generates a random edit code when link.EditCode is empty, then hashes
// whichever value ends up set (caller-supplied or generated) into link.EditCode in place,
// returning the plaintext so Create can hand it back to the caller once, like notes.Manager.Create.
func (m *Manager) resolveAndHashEditCode(link *domain.Link) (string, error) {
	if link.EditCode == "" {
		editCode, err := domain.NewEditCode()
		if err != nil {
			return "", fmt.Errorf("generate edit code: %w", err)
		}

		link.EditCode = editCode
	}

	plaintext := link.EditCode

	hashed, err := m.hasher.Hash(link.EditCode)
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
	// Unlike notes, links are only served at single-segment routes (GET /r/{id} plus the ogen
	// GET /links/{id} / DELETE /links/{id}), so a multi-segment custom alias would be created
	// successfully yet unroutable and unreachable through the API. Reject it here, at the
	// resource that owns the routing constraint, rather than narrowing the shared
	// domain.ValidCustomSlug that multi-segment notes legitimately rely on.
	if strings.Contains(customHash, "/") {
		return "", nil, fmt.Errorf("custom link slug %q must be a single path segment (links are served at /r/{id}): %w",
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

			return "", nil, err
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
		if m.metrics != nil {
			m.metrics.IncCollisions()
		}
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

func (m *Manager) consumeLink(ctx context.Context, slug, dbID string) (*domain.Link, error) {
	consumed, err := m.storage.Consume(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("redirect: burn after click: %w", err)
	}

	consumed.ID = slug

	err = decryptTargetURL(ctx, m.encryptor, m.log, consumed)
	if err != nil {
		return nil, fmt.Errorf("redirect: burn after click: %w", err)
	}

	m.log.DebugContext(ctx, "link burned", "id", slug)

	if m.metrics != nil {
		m.metrics.IncRedirects()
	}

	return consumed, nil
}
