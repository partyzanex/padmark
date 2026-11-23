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
// across notes, links and files, returning domain.ErrSlugConflict if another resource owns it:
// it is the only slug uniqueness check (docs/adr/0003-storage-slug-uniqueness.md).
// Update writes only the fields that patch sets and returns the row as stored, so a concurrent
// change of a field that this update omits stays.
// DeleteExpired removes a row only while it is still expired at now, and returns
// domain.ErrNotFound otherwise: an expiry decision from an old snapshot never deletes a link that
// its owner extended, or a live link that took the ID after the decision. Each method that
// compares times gets now from Manager and does not read the clock itself; Create removes other
// links that expired at link.CreatedAt.
//
// Update and Delete change the row only while its edit_code still equals storedEditCode, the
// stored hash of the link that the caller checked. Each link gets a hash with a new random salt
// and no write changes it, so the hash identifies one link version. If the link was deleted and
// a new link took the same ID after the check, they return domain.ErrNotFound and do not touch
// the new link. Update also changes the row only while it is not expired at now, as
// files.Storage.Update does: a link that expired after the check of Manager.Update gets
// domain.ErrNotFound, and its expiry stays.
type Storage interface {
	Create(ctx context.Context, link *domain.Link) error
	Update(
		ctx context.Context, id, storedEditCode string, patch *domain.LinkPatch, now time.Time,
	) (*domain.Link, error)
	GetByID(ctx context.Context, id string) (*domain.Link, error)
	IncrementViews(ctx context.Context, id string) error
	Delete(ctx context.Context, id, storedEditCode string) error
	DeleteExpired(ctx context.Context, id string, now time.Time) error
	Consume(ctx context.Context, id string, now time.Time) (*domain.Link, error)
	// ConsumeVisible atomically checks current visibility and expiry/burn eligibility at now
	// before deleting and returning a row. allowBurn=false permits only expired rows.
	ConsumeVisible(
		ctx context.Context, id string, callerID uuid.UUID, authenticated, allowBurn bool, now time.Time,
	) (*domain.Link, error)
	// IncrementViewsVisible refuses a row that is no longer visible to this reader.
	IncrementViewsVisible(ctx context.Context, id string, callerID uuid.UUID, authenticated bool) error
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
//   - IncCollisions: one random slug that Storage.Create refused because it is already taken.
//     Deliberately scoped to the random-generation retry case only, not a rejected
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
	Encryptor    Encryptor
	Hasher       EditCodeHasher
	ExpiredSlugs ExpiredSlugReleaser
	Log          *slog.Logger
	// Now is the time source of each expiry decision and timestamp. The composition root gives
	// time.Now; tests give a fixed clock.
	Now func() time.Time
}

// Manager implements short-link business logic: URL validation, slug resolution, and encryption
// coordination — the links analogue of notes.Manager.
type Manager struct {
	storage          Storage
	encryptor        Encryptor
	hasher           EditCodeHasher
	expiredSlugs     ExpiredSlugReleaser
	cache            Cache
	metrics          Metrics
	log              *slog.Logger
	now              func() time.Time
	allowCustomSlugs domain.CustomSlugSetting
}

// NewManager creates a new Manager. allowCustomSlugs enables user-supplied slugs (aliases); when
// false, a create request carrying one is rejected with domain.ErrCustomSlugDisabled, as for
// notes (both use the server-wide --custom-slugs flag). cache and metrics are optional
// collaborators attached via WithCache/WithMetrics at composition time: cache defaults to nil
// (no cache), metrics to noopMetrics.
func NewManager(deps *Deps, allowCustomSlugs bool) *Manager {
	return &Manager{
		storage:          deps.Storage,
		encryptor:        deps.Encryptor,
		hasher:           deps.Hasher,
		expiredSlugs:     deps.ExpiredSlugs,
		cache:            nil,
		metrics:          noopMetrics{},
		log:              deps.Log,
		now:              deps.Now,
		allowCustomSlugs: domain.CustomSlugSetting(allowCustomSlugs),
	}
}

// WithCache attaches an optional Cache for frequently-followed links. Nil (the default — Manager
// never sets m.cache itself) disables caching entirely: every code path already nil-checks it, so
// behavior is identical with or without one — this is a pure performance optimization, never a
// correctness dependency. Called by the composition root; do not call after the server starts
// serving requests.
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
// notes.Manager.Create closely; see store for the slug conflict rules.
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

	err = validateLink(link)
	if err != nil {
		return nil, err
	}

	// A caller-chosen code must meet the edit-code policy before it is hashed; an empty one is
	// generated below. Update never accepts a replacement code, so this is the only check site.
	if link.EditCode != "" {
		err = domain.ValidateEditCode(link.EditCode)
		if err != nil {
			return nil, fmt.Errorf("validate edit code: %w", err)
		}
	}

	// The slug check goes before the memory-hard edit-code hash, so an invalid slug costs little.
	slug, err := m.resolveSlug(opts.CustomHash)
	if err != nil {
		return nil, err
	}

	plaintextCode, err := m.resolveAndHashEditCode(link)
	if err != nil {
		return nil, err
	}

	link.CreatedAt = m.now().UTC()

	slug, err = m.store(ctx, link, validURL, opts.CustomHash, slug)
	if err != nil {
		return nil, err
	}

	// link gets the slug, the plaintext target URL and the plaintext edit code only after the
	// write; the storage form was a separate value.
	link.ID = slug
	link.TargetURL = validURL
	link.EditCode = plaintextCode

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
	return m.resolveLink(ctx, slug)
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

// store writes link under the slug first and returns the slug that storage accepted. The storage form is a
// separate value with the hashed slug and the encrypted target URL; link already holds the
// edit-code hash. Storage.Create is the only uniqueness check. A random slug that is taken gets a
// new random slug, at most maxHashRetries times. A custom slug that is taken stays taken, except
// when an expired record holds it: then store removes that record and tries once more, as
// notes.Manager does. The storage triggers check the slug again, so the second attempt is safe
// across server processes.
func (m *Manager) store(ctx context.Context, link *domain.Link, validURL, customHash, first string) (string, error) {
	custom, slug := customHash != "", first

	for range maxHashRetries {
		recordID := domain.HashSlug(slug)

		encrypted, err := m.encryptor.Encrypt(validURL, recordID)
		if err != nil {
			return "", fmt.Errorf("encrypt target url: %w", err)
		}

		stored := *link
		stored.ID = recordID
		stored.TargetURL = encrypted

		err = m.storage.Create(ctx, &stored)
		if custom && errors.Is(err, domain.ErrSlugConflict) && m.releaseExpiredHolder(ctx, recordID, link.CreatedAt) {
			err = m.storage.Create(ctx, &stored)
		}

		if err == nil {
			return slug, nil
		}

		if custom || !errors.Is(err, domain.ErrSlugConflict) {
			return "", fmt.Errorf("store link: %w", err)
		}

		// Only a random slug comes here, never a custom one: see the Metrics doc comment.
		m.metrics.IncCollisions()

		slug, err = domain.ResolveSlug("", m.allowCustomSlugs)
		if err != nil {
			return "", fmt.Errorf("resolve slug: %w", err)
		}
	}

	return "", domain.ErrHashCollision
}

// resolveLink reads the current row before consulting the decrypted-target cache.
// Privacy, ownership, deletion, TTL and burn state must be visible across replicas
// without waiting for local cache expiry. The cache can only reuse a public URL
// when its ciphertext matches this read's snapshot.
//
// Freshness, including across replicas, comes from the ciphertext fingerprint: a stale entry
// never matches the row read below. No lock serializes reads, updates and cache fills: a read
// that races an Update or a Delete can store the old target after the eviction. That entry is
// never served, and the next read of the link or the cache TTL replaces it.
func (m *Manager) resolveLink(ctx context.Context, slug string) (*domain.Link, error) {
	dbID := domain.HashSlug(slug)

	link, err := m.storage.GetByID(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("get link: %w", err)
	}

	link.ID = slug
	ciphertext := link.TargetURL

	cacheable := m.cache != nil && link.EffectivePrivacy() == domain.PrivacyPublic &&
		!link.BurnAfterClick && !link.Expired(m.now())
	if cacheable {
		if target, ok := m.cache.Get(dbID, ciphertext); ok {
			link.TargetURL = target

			return link, nil
		}
	}

	err = decryptTargetURL(ctx, m.encryptor, m.log, link)
	if err != nil {
		return nil, err
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

// resolveSlug resolves customHash into a slug via domain.ResolveSlug. It does not check that the
// slug is free: Storage.Create does that (see store).
func (m *Manager) resolveSlug(customHash string) (string, error) {
	// Unlike notes, links are only served at single-segment routes (GET /{id} and GET /r/{id} plus the ogen
	// GET /links/{id} / DELETE /links/{id}), so a multi-segment custom alias would be created
	// successfully yet unroutable and unreachable through the API. Reject it here, at the
	// resource that owns the routing constraint, rather than narrowing the shared
	// domain.ValidCustomSlug that multi-segment notes legitimately rely on.
	if strings.Contains(customHash, "/") {
		return "", fmt.Errorf(
			"custom link slug %q must be a single path segment (links are served at /{id}): %w",
			customHash, domain.ErrInvalidSlug)
	}

	slug, err := domain.ResolveSlug(customHash, m.allowCustomSlugs)
	if err != nil {
		return "", fmt.Errorf("resolve slug: %w", err)
	}

	return slug, nil
}

// consumeLink deletes the burn link that reader follows and returns it with the plaintext target.
func (m *Manager) consumeLink(ctx context.Context, slug, dbID string, reader domain.Reader) (*domain.Link, error) {
	consumed, err := m.consume(ctx, dbID, reader)
	if err != nil {
		return nil, err
	}

	consumed.ID = slug

	err = decryptTargetURL(ctx, m.encryptor, m.log, consumed)
	if err != nil {
		return nil, err
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
		consumed, err := m.storage.Consume(ctx, dbID, m.now())
		if err != nil {
			return nil, fmt.Errorf("burn link: %w", err)
		}

		return consumed, nil
	}

	consumed, err := m.storage.ConsumeVisible(ctx, dbID, reader.CallerID, reader.Authenticated, true, m.now())
	if err != nil {
		return nil, fmt.Errorf("burn link: %w", err)
	}

	return consumed, nil
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
		// A link that had this ID can have a cached target; the new record must not keep it alive.
		m.evictCache(id)
		m.log.DebugContext(ctx, "expired record released its slug", "id", id)
	}

	return released
}
