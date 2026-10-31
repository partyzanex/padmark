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
	"github.com/partyzanex/padmark/internal/errorcontext"
)

// maxHashRetries bounds random-slug collision retries in Create — a bounded loop, never an
// unbounded goto/loop. Custom aliases never retry: a collision there is an immediate
// ErrSlugConflict, since the caller chose that exact value.
const maxHashRetries = 5

// Storage defines persistence operations for links. Create must atomically claim the ID
// across notes and links, returning domain.ErrSlugConflict if another resource owns it.
type Storage interface {
	Create(ctx context.Context, link *domain.Link) error
	Update(ctx context.Context, id string, link *domain.Link) (*domain.Link, error)
	GetByID(ctx context.Context, id string) (*domain.Link, error)
	Exists(ctx context.Context, id string) (bool, error)
	IncrementViews(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	Consume(ctx context.Context, id string) (*domain.Link, error)
	// ConsumeVisible atomically checks current visibility and expiry/burn eligibility before
	// deleting and returning a row. allowBurn=false permits only expired rows.
	ConsumeVisible(ctx context.Context, id string, callerID uuid.UUID, authenticated, allowBurn bool) (*domain.Link, error)
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

	if m.metrics != nil {
		m.metrics.IncLinksCreated()
	}

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

// Peek fetches a link by slug without incrementing views, consuming burn-after-click, or
// applying expiry policy. Used by the HTTP layer's privacy gate before Redirect's side effects,
// and directly by OgenHandler.GetLink (GET /links/{id}'s metadata read) — mirrors
// notes.Manager.Peek exactly, including intentionally returning expired/burn-pending links as-is.
// Every call reads current metadata from storage. Only target decryption may use
// the optional Cache; cached entries never supply privacy, ownership or lifecycle state.
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
// The initial fetch always reads current storage metadata. For public links the
// optional Cache can skip decryption after verifying the current ciphertext.
// This operation does not authorize a reader; adapters with accounts enabled use Follow.
func (m *Manager) Redirect(ctx context.Context, slug string) (*domain.Link, error) {
	operationResult, operationErr := m.redirect(ctx, slug, true)
	if operationErr != nil {
		return operationResult, fmt.Errorf("redirect: %w", operationErr)
	}

	return operationResult, nil
}

// RedirectWithoutBurn is Redirect for an access that did not deliberately follow the link (a
// link-preview fetch, a mail scanner, a HEAD probe, an embedded resource): a burn_after_click
// link is left intact and domain.ErrBurnIntentRequired is returned, decided on the same row
// snapshot the rest of the policy uses, so a concurrent switch to burn cannot slip through. Any
// other link is followed exactly as by Redirect.
func (m *Manager) RedirectWithoutBurn(ctx context.Context, slug string) (*domain.Link, error) {
	operationResult, operationErr := m.redirect(ctx, slug, false)
	if operationErr != nil {
		return operationResult, fmt.Errorf("redirect: %w", operationErr)
	}

	return operationResult, nil
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

	encrypted, encErr := m.encryptor.Encrypt(validURL, domain.HashSlug(slug))
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

// redirect is Redirect/RedirectWithoutBurn's shared body; allowBurn says whether this access may
// consume a burn_after_click link.
func (m *Manager) redirect(ctx context.Context, slug string, allowBurn bool) (*domain.Link, error) {
	link, err := m.resolveLink(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("redirect: %w", err)
	}

	operationResult, operationErr := m.redirectLink(ctx, slug, link, allowBurn, nil)
	if operationErr != nil {
		return operationResult, fmt.Errorf("redirect link: %w", operationErr)
	}

	return operationResult, nil
}

func (m *Manager) redirectLink(
	ctx context.Context, slug string, link *domain.Link, allowBurn bool, reader *FollowOptions,
) (*domain.Link, error) {
	dbID := domain.HashSlug(slug)

	if link.Expired(time.Now()) {
		return nil, errorcontext.Wrap(m.expireLink(ctx, slug, dbID, reader), "handle redirect link")
	}

	if link.BurnAfterClick {
		if !allowBurn {
			return nil, fmt.Errorf("redirect: %w", domain.ErrBurnIntentRequired)
		}

		operationResult, operationErr := m.consumeLink(ctx, slug, dbID, reader)
		if operationErr != nil {
			return operationResult, fmt.Errorf("consume link: %w", operationErr)
		}

		return operationResult, nil
	}

	incErr := m.incrementRedirectViews(ctx, dbID, link, reader)
	if incErr != nil {
		return nil, fmt.Errorf("increment redirect views: %w", incErr)
	}

	if m.metrics != nil {
		m.metrics.IncRedirects()
	}

	return link, nil
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

func (m *Manager) consumeLink(
	ctx context.Context, slug, dbID string, reader *FollowOptions,
) (*domain.Link, error) {
	var consumed *domain.Link

	var err error

	if reader == nil {
		consumed, err = m.storage.Consume(ctx, dbID)
	} else {
		consumed, err = m.storage.ConsumeVisible(ctx, dbID, reader.CallerID, reader.Authenticated, true)
	}

	if err != nil {
		return nil, fmt.Errorf("redirect: burn after click: %w", err)
	}

	consumed.ID = slug

	err = decryptTargetURL(ctx, m.encryptor, m.log, consumed)
	if err != nil {
		return nil, fmt.Errorf("redirect: burn after click: %w", err)
	}

	m.log.DebugContext(ctx, "link burned", "id", domain.HashSlug(slug))

	if m.metrics != nil {
		m.metrics.IncRedirects()
	}

	return consumed, nil
}
