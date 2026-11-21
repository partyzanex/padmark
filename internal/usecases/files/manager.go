//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=files -write_package_comment=false

// Package files publishes uploaded files under short slugs. The content goes to object storage,
// encrypted with a key for each file; the database keeps the access rules and the wrapped key.
package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// maxSlugRetries limits the retries after a random slug collision. A custom slug never retries.
const maxSlugRetries = 5

// Storage keeps file records. Errors are fail-hard: Manager returns them to the caller.
//
// Each write names the record by ID and by the object key from the read. The object key is the
// identity of one upload: when a new file has the same slug, a write from an old read finds no
// row and returns domain.ErrNotFound, so it cannot change or delete the new file.
type Storage interface {
	// Create inserts the record. It must atomically claim the ID across notes, links and
	// files, and return domain.ErrSlugConflict if another resource has it.
	Create(ctx context.Context, file *domain.File) error
	// GetByID returns the record. While a burn claim that is not stale holds the record, it
	// returns domain.ErrNotFound: other readers must not see a file that a download consumes.
	GetByID(ctx context.Context, id string) (*domain.File, error)
	// Exists reports whether a record has the ID. A claimed record also holds its ID.
	Exists(ctx context.Context, id string) (bool, error)
	// Delete removes the record with id and objectKey.
	Delete(ctx context.Context, id, objectKey string) error
	// Update writes only the fields that opts sets, while the record is not expired at now.
	Update(ctx context.Context, id, objectKey string, opts UpdateOptions, now time.Time) error
	// ClaimBurn atomically marks a burn record as claimed by claim.Token until claim.Until.
	// The record must be visible to claim.Reader, not expired at claim.Now and without a claim
	// that is not stale. Of parallel calls, only one succeeds; the others get domain.ErrNotFound.
	ClaimBurn(ctx context.Context, claim *BurnClaim) error
	// ReleaseBurn removes the claim, so that the next reader can download the file. After a
	// complete transfer, Manager uses Delete: Send ends before the claim becomes stale, so no
	// other reader can hold the record at that time.
	ReleaseBurn(ctx context.Context, claim *BurnClaim) error
	// IncrementDownloadsVisible counts a download of a record that is visible to reader, is not
	// a burn file and is not expired. Otherwise it returns domain.ErrNotFound.
	IncrementDownloadsVisible(ctx context.Context, id, objectKey string, reader AccessOptions) error
	// ListExpired returns at most limit records that expired before now and have no claim that
	// is not stale. Only ID and ObjectKey must be set.
	ListExpired(ctx context.Context, now time.Time, limit int) ([]*domain.File, error)
	// DeleteExpired removes the record with id and objectKey while it is expired at now and
	// has no claim that is not stale.
	DeleteExpired(ctx context.Context, id, objectKey string, now time.Time) error
}

// ObjectStore keeps file content. Put, Stat and WriteTo are fail-hard. Delete is fail-soft:
// Manager logs and counts a failure. After a user delete or a burn download the record is
// already gone, so a failure leaves an object that nobody can read. An expired file keeps its
// record when Delete fails, so the next sweep tries again.
type ObjectStore interface {
	Put(ctx context.Context, key string, body io.Reader) error
	// Stat and WriteTo return domain.ErrNotFound when the object does not exist.
	Stat(ctx context.Context, key string) error
	WriteTo(ctx context.Context, key string, dst io.Writer) error
	// Delete removes the object. It does not return an error for a key that does not exist.
	Delete(ctx context.Context, key string) error
}

// StreamCipher encrypts file content with a key for each file. Errors are fail-hard: an upload
// or a download with a cipher error stops and returns the error.
type StreamCipher interface {
	NewKey() ([]byte, error)
	Encrypt(key []byte, dst io.Writer, src io.Reader) error
	Decrypt(key []byte, dst io.Writer, src io.Reader) error
}

// Encryptor seals the file metadata with the installation keyring. The record ID is
// authenticated context. Files have no legacy records, so Manager gives an empty legacySlug.
// Encrypt errors are fail-hard: the upload stops. A Decrypt error makes the file not found,
// so that key errors do not show which slugs exist.
type Encryptor interface {
	Encrypt(plaintext, recordID string) (string, error)
	Decrypt(ciphertext, recordID, legacySlug string) (string, error)
}

// EditCodeHasher hashes and verifies edit codes. HashUserProvided is memory-hard, because the
// entropy of a code that the caller selects is not known. Hash errors are fail-hard: the
// upload stops. Verify has no error: a code that it cannot verify is a wrong code.
type EditCodeHasher interface {
	Hash(code string) (string, error)
	HashUserProvided(code string) (string, error)
	Verify(storedHash, code string) bool
}

// SlugLock serializes local slug checks. Share the instance with notes.Manager and
// links.Manager; storage enforces uniqueness between server processes. Errors are fail-hard:
// the operation stops, because it cannot check the slug safely.
type SlugLock interface {
	Lock(ctx context.Context, key string) (unlock func(), err error)
}

// SlugChecker reports whether another resource (notes or links) has an ID. Errors are
// fail-hard: an upload stops, because it cannot check the slug.
type SlugChecker interface {
	Exists(ctx context.Context, id string) (bool, error)
}

// DeleteReason tells why a file was deleted. It is the "reason" label of the deleted counter.
type DeleteReason string

// The reasons of a file deletion.
const (
	DeleteReasonUser    DeleteReason = "user"
	DeleteReasonBurn    DeleteReason = "burn"
	DeleteReasonExpired DeleteReason = "expired"
)

// StorageOp is the object storage operation that failed. It is the "op" label of the storage
// error counter.
type StorageOp string

// The object storage operations.
const (
	StorageOpPut    StorageOp = "put"
	StorageOpStat   StorageOp = "stat"
	StorageOpGet    StorageOp = "get"
	StorageOpDelete StorageOp = "delete"
)

// Metrics records file events. Metrics are fail-soft: the methods do not return errors, and
// they must not block, because Manager calls them on the request path.
type Metrics interface {
	IncUploads()
	ObserveUploadBytes(size int64)
	IncDownloads()
	IncDeleted(reason DeleteReason)
	IncStorageErrors(op StorageOp)
}

// noopMetrics is the default Metrics. It records nothing, so Manager needs no nil checks.
type noopMetrics struct{}

func (noopMetrics) IncUploads()                {}
func (noopMetrics) ObserveUploadBytes(int64)   {}
func (noopMetrics) IncDownloads()              {}
func (noopMetrics) IncDeleted(DeleteReason)    {}
func (noopMetrics) IncStorageErrors(StorageOp) {}

// Deps holds the dependencies of Manager. All fields are mandatory.
type Deps struct {
	Storage   Storage
	Objects   ObjectStore
	Cipher    StreamCipher
	Encryptor Encryptor
	Hasher    EditCodeHasher
	SlugLock  SlugLock
	Notes     SlugChecker
	Links     SlugChecker
	Log       *slog.Logger
}

// CreateOptions holds the Create parameters other than the content.
type CreateOptions struct {
	ExpiresAt         *time.Time
	Privacy           *domain.Privacy
	OwnerID           *uuid.UUID
	CustomSlug        string
	EditCode          string
	BurnAfterDownload bool
}

// AccessOptions identifies the reader. AllowBurn tells if the access is deliberate, so that it
// can consume a burn-after-download file. SkipPrivacy turns off the privacy check of reads, as
// notes and links do when accounts are off. It does not change the owner and edit code checks
// of Update and Delete.
type AccessOptions struct {
	CallerID      uuid.UUID
	Authenticated bool
	AllowBurn     bool
	SkipPrivacy   bool
}

// visible reports whether the reader can see file.
func (o AccessOptions) visible(file *domain.File) bool {
	return o.SkipPrivacy || file.VisibleTo(o.CallerID, o.Authenticated)
}

// BurnClaim identifies one download of a burn-after-download file. Token is random for each
// download, so that only this download can finish or release its claim.
type BurnClaim struct {
	Now       time.Time
	Until     time.Time
	ID        string
	ObjectKey string
	Token     string
	Reader    AccessOptions
}

// sealedMeta is the plaintext of File.SealedMeta. One envelope for the name and the key stops
// a swap of the two values between columns.
type sealedMeta struct {
	Name string `json:"name"`
	Key  []byte `json:"key"`
}

// Manager implements the file life cycle: upload, metadata, download and delete.
type Manager struct {
	metrics          Metrics
	deps             *Deps
	maxSize          int64
	allowCustomSlugs domain.CustomSlugSetting
}

// NewManager returns a Manager. maxSize is the largest permitted file in bytes.
// allowCustomSlugs has the same meaning as for notes and links (--custom-slugs).
// The Manager records no metrics until WithMetrics attaches them.
func NewManager(deps *Deps, maxSize int64, allowCustomSlugs bool) *Manager {
	return &Manager{
		metrics:          noopMetrics{},
		deps:             deps,
		maxSize:          maxSize,
		allowCustomSlugs: domain.CustomSlugSetting(allowCustomSlugs),
	}
}

// WithMetrics attaches metrics and returns m. Call it at composition time, before the first
// request. Give a value that is not nil.
func (m *Manager) WithMetrics(metrics Metrics) *Manager {
	m.metrics = metrics

	return m
}

// MaxSize returns the largest permitted file in bytes.
func (m *Manager) MaxSize() int64 { return m.maxSize }

// Create encrypts body to object storage and records the file. It returns the file with the
// slug as ID and the plaintext edit code. The upload goes first, because it can take a long
// time; the slug lock is held only for the record insert.
func (m *Manager) Create(ctx context.Context, name string, body io.Reader, opts CreateOptions) (*domain.File, error) {
	file, plainCode, err := m.newFile(name, &opts)
	if err != nil {
		return nil, err
	}

	err = m.upload(ctx, file, body)
	if err != nil {
		return nil, err
	}

	err = m.insert(ctx, file, opts.CustomSlug)

	// The key is in the sealed metadata now. The caller does not need it.
	dropKey(file)

	if err != nil {
		// The object is complete and has no record. The request can be canceled, but the
		// cleanup must still run.
		m.deleteObject(context.WithoutCancel(ctx), file.ObjectKey)

		return nil, err
	}

	file.EditCode = plainCode

	m.metrics.IncUploads()
	m.metrics.ObserveUploadBytes(file.Size)
	m.deps.Log.DebugContext(ctx, "file created", "id", domain.HashSlug(file.ID), "size", file.Size)

	m.sweepOnUpload(ctx)

	return file, nil
}

// Peek returns the file with decrypted metadata. It does not apply privacy, expiry or burn
// rules; callers that show a file to a reader use Info or Download.
func (m *Manager) Peek(ctx context.Context, slug string) (*domain.File, error) {
	file, err := m.readFile(ctx, slug)
	if err != nil {
		return nil, err
	}

	dropKey(file)

	return file, nil
}

// Info returns the file for a reader after the privacy and expiry checks. An expired file is
// deleted and Info returns domain.ErrExpired.
func (m *Manager) Info(ctx context.Context, slug string, opts AccessOptions) (*domain.File, error) {
	file, err := m.readVisible(ctx, slug, opts)
	if err != nil {
		return nil, err
	}

	dropKey(file)

	return file, nil
}

// Download prepares the file for a reader. It applies privacy and expiry, counts the download,
// and claims a burn-after-download file when opts.AllowBurn is true. Without AllowBurn such a
// file stays and Download returns domain.ErrBurnIntentRequired. The object is checked before
// the record changes, so a missing object does not consume the file. A burn file is deleted
// only when Send completes; see Download.Close.
func (m *Manager) Download(ctx context.Context, slug string, opts AccessOptions) (*Download, error) {
	file, err := m.readVisible(ctx, slug, opts)
	if err != nil {
		return nil, err
	}

	if file.BurnAfterDownload && !opts.AllowBurn {
		dropKey(file)

		return nil, domain.ErrBurnIntentRequired
	}

	err = m.statObject(ctx, file)
	if err != nil {
		dropKey(file)

		return nil, err
	}

	objectKey, contentKey := file.ObjectKey, file.ContentKey
	download := NewDownload(file, func(ctx context.Context, dst io.Writer) error {
		return m.sendObject(ctx, objectKey, contentKey, dst)
	})

	err = m.record(ctx, file, opts, download)
	if err != nil {
		dropKey(file)

		return nil, err
	}

	// The counter has the same moment as the download counter in the record.
	m.metrics.IncDownloads()

	return download, nil
}

// Delete removes a file after a check of the edit code, or of the caller as the owner. The
// record goes first, so that no reader gets the file after Delete returns. Then the object goes;
// a failure there is fail-soft and leaves an object that nobody can read.
func (m *Manager) Delete(ctx context.Context, slug, editCode string, callerID uuid.UUID) error {
	recordID := domain.HashSlug(slug)

	existing, err := m.deps.Storage.GetByID(ctx, recordID)
	if err != nil {
		return fmt.Errorf("delete file: %w", err)
	}

	if !existing.OwnedBy(callerID) && !m.deps.Hasher.Verify(existing.EditCode, editCode) {
		return domain.ErrInvalidEditCode
	}

	err = m.deps.Storage.Delete(ctx, recordID, existing.ObjectKey)
	if err != nil {
		return fmt.Errorf("delete file: %w", err)
	}

	m.metrics.IncDeleted(DeleteReasonUser)
	// The record is gone. A client that disconnects now must not stop the object delete.
	m.deleteObject(context.WithoutCancel(ctx), existing.ObjectKey)

	return nil
}

// readFile reads the record and decrypts its metadata. The result has the content key.
func (m *Manager) readFile(ctx context.Context, slug string) (*domain.File, error) {
	file, err := m.deps.Storage.GetByID(ctx, domain.HashSlug(slug))
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}

	err = m.openMeta(ctx, slug, file)
	if err != nil {
		return nil, err
	}

	return file, nil
}

// readVisible is Info that keeps the content key in the result, for Download.
func (m *Manager) readVisible(ctx context.Context, slug string, opts AccessOptions) (*domain.File, error) {
	file, err := m.readFile(ctx, slug)
	if err != nil {
		return nil, err
	}

	if !opts.visible(file) {
		dropKey(file)

		return nil, domain.ErrForbidden
	}

	if file.Expired(time.Now()) {
		dropKey(file)

		return nil, m.expire(ctx, file)
	}

	return file, nil
}

func (m *Manager) newFile(name string, opts *CreateOptions) (*domain.File, string, error) {
	cleanName, err := domain.CleanFileName(name)
	if err != nil {
		return nil, "", fmt.Errorf("clean file name: %w", err)
	}

	err = m.checkCustomSlug(opts.CustomSlug)
	if err != nil {
		return nil, "", err
	}

	file := &domain.File{
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         opts.ExpiresAt,
		Privacy:           opts.Privacy,
		OwnerID:           opts.OwnerID,
		ID:                "",
		ObjectKey:         uuid.NewString(),
		SealedMeta:        "",
		Name:              cleanName,
		ContentKey:        nil,
		EditCode:          opts.EditCode,
		Size:              0,
		Downloads:         0,
		BurnAfterDownload: opts.BurnAfterDownload,
	}

	err = file.Validate()
	if err == nil {
		err = file.ValidateOwnership()
	}

	if err != nil {
		return nil, "", fmt.Errorf("validate file: %w", err)
	}

	plainCode, err := m.hashEditCode(file)
	if err != nil {
		return nil, "", err
	}

	return file, plainCode, nil
}

// checkCustomSlug does the static slug checks before the upload: the --custom-slugs setting,
// the slug format and the reserved route names. Thus a bad slug does not cost a full upload.
// Only the uniqueness check waits for the record insert.
func (m *Manager) checkCustomSlug(customSlug string) error {
	if customSlug == "" {
		return nil
	}

	// Files are served at single-segment routes (/{id} and /files/{id}).
	if strings.Contains(customSlug, "/") {
		return fmt.Errorf("custom file slug must be one path segment: %w", domain.ErrInvalidSlug)
	}

	_, err := domain.ResolveSlug(customSlug, m.allowCustomSlugs)
	if err != nil {
		return fmt.Errorf("resolve slug: %w", err)
	}

	return nil
}

// hashEditCode replaces file.EditCode with its hash and returns the plaintext. It generates a
// code when the caller did not give one.
func (m *Manager) hashEditCode(file *domain.File) (string, error) {
	var (
		hashed string
		err    error
	)

	plain := file.EditCode

	if plain == "" {
		plain, err = domain.NewEditCode()
		if err != nil {
			return "", fmt.Errorf("generate edit code: %w", err)
		}

		hashed, err = m.deps.Hasher.Hash(plain)
	} else {
		err = domain.ValidateEditCode(plain)
		if err != nil {
			return "", fmt.Errorf("validate edit code: %w", err)
		}

		hashed, err = m.deps.Hasher.HashUserProvided(plain)
	}

	if err != nil {
		return "", fmt.Errorf("hash edit code: %w", err)
	}

	file.EditCode = hashed

	return plain, nil
}

// insert resolves the slug, seals the metadata for that record ID and writes the record.
func (m *Manager) insert(ctx context.Context, file *domain.File, customSlug string) error {
	slug, unlock, err := m.resolveUniqueSlug(ctx, customSlug)
	if err != nil {
		return err
	}
	defer unlock()

	recordID := domain.HashSlug(slug)

	meta, err := json.Marshal(sealedMeta{Name: file.Name, Key: file.ContentKey})
	if err != nil {
		return fmt.Errorf("marshal file meta: %w", err)
	}

	// The JSON holds the content key in base64.
	defer clear(meta)

	sealed, err := m.deps.Encryptor.Encrypt(string(meta), recordID)
	if err != nil {
		return fmt.Errorf("seal file meta: %w", err)
	}

	file.ID = recordID
	file.SealedMeta = sealed

	err = m.deps.Storage.Create(ctx, file)

	file.ID = slug

	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	return nil
}

func (m *Manager) resolveUniqueSlug(
	ctx context.Context, customSlug string,
) (slug string, unlock func(), err error) {
	for range maxSlugRetries {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return "", nil, fmt.Errorf("resolve unique slug: %w", ctxErr)
		}

		slug, err = domain.ResolveSlug(customSlug, m.allowCustomSlugs)
		if err != nil {
			return "", nil, fmt.Errorf("resolve slug: %w", err)
		}

		unlock, err = m.deps.SlugLock.Lock(ctx, domain.HashSlug(slug))
		if err != nil {
			return "", nil, fmt.Errorf("lock slug: %w", err)
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

		if customSlug != "" {
			return "", nil, domain.ErrSlugConflict
		}
	}

	return "", nil, domain.ErrHashCollision
}

// slugTaken reports whether a note, a link or a file has id. An expired file that the sweep did
// not remove yet does not hold the slug: slugTaken removes it.
func (m *Manager) slugTaken(ctx context.Context, id string) (bool, error) {
	for _, checker := range []SlugChecker{m.deps.Notes, m.deps.Links} {
		taken, err := checker.Exists(ctx, id)
		if err != nil {
			return false, fmt.Errorf("check slug: %w", err)
		}

		if taken {
			return true, nil
		}
	}

	taken, err := m.deps.Storage.Exists(ctx, id)
	if err != nil {
		return false, fmt.Errorf("check slug: %w", err)
	}

	if !taken {
		return false, nil
	}

	released, err := m.ReleaseExpiredSlug(ctx, id)
	if err != nil {
		m.deps.Log.WarnContext(ctx, "release expired file slug", "id", id, "err", err)

		return true, nil
	}

	return !released, nil
}

// openMeta decrypts SealedMeta into Name and ContentKey. A failure is ErrNotFound, as for notes
// and links, so that key errors do not show which slugs exist.
func (m *Manager) openMeta(ctx context.Context, slug string, file *domain.File) error {
	recordID := domain.HashSlug(slug)

	plain, err := m.deps.Encryptor.Decrypt(file.SealedMeta, recordID, "")
	if err != nil {
		m.deps.Log.WarnContext(ctx, "file meta decryption failed", "id", recordID, "err", err)

		return domain.ErrNotFound
	}

	var meta sealedMeta

	err = json.Unmarshal([]byte(plain), &meta)
	if err != nil {
		m.deps.Log.ErrorContext(ctx, "file meta is corrupt", "id", recordID, "err", err)

		return domain.ErrNotFound
	}

	file.ID = slug
	file.Name = meta.Name
	file.ContentKey = meta.Key

	return nil
}

// statObject makes sure that the object of file exists. The log lines identify the file by its
// record ID, as the other file logs do, and not by the object key.
func (m *Manager) statObject(ctx context.Context, file *domain.File) error {
	recordID := domain.HashSlug(file.ID)

	err := m.deps.Objects.Stat(ctx, file.ObjectKey)
	if errors.Is(err, domain.ErrNotFound) {
		m.deps.Log.ErrorContext(ctx, "file object is missing", "id", recordID)

		return domain.ErrNotFound
	}

	if err != nil {
		m.metrics.IncStorageErrors(StorageOpStat)
		m.deps.Log.ErrorContext(ctx, "check file object", "id", recordID, "err", err)

		return fmt.Errorf("check file object: %w", errors.Join(domain.ErrStorageUnavailable, err))
	}

	return nil
}

// sendObject decrypts the object to dst. The object storage writes into a pipe in a goroutine;
// the goroutine ends before sendObject returns.
func (m *Manager) sendObject(ctx context.Context, objectKey string, contentKey []byte, dst io.Writer) error {
	pipeReader, pipeWriter := io.Pipe()
	fetched := make(chan error, 1)

	go func() {
		fetchErr := m.deps.Objects.WriteTo(ctx, objectKey, pipeWriter)
		fetched <- errors.Join(fetchErr, pipeWriter.CloseWithError(fetchErr))
	}()

	decErr := m.deps.Cipher.Decrypt(contentKey, dst, pipeReader)
	decErr = errors.Join(decErr, pipeReader.CloseWithError(errStreamStopped))

	fetchErr := <-fetched
	if errors.Is(fetchErr, errStreamStopped) {
		fetchErr = nil
	}

	// A canceled request is the client's error, not a storage failure.
	if fetchErr != nil && ctx.Err() == nil {
		m.metrics.IncStorageErrors(StorageOpGet)
	}

	err := errors.Join(decErr, fetchErr)
	if err != nil {
		return fmt.Errorf("send file: %w", err)
	}

	return nil
}

// dropKey removes the content key from file and clears its bytes, as the note content cipher
// does with its keys. A key in memory longer than necessary can go to a core dump.
func dropKey(file *domain.File) {
	clear(file.ContentKey)
	file.ContentKey = nil
}
