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

// errStreamStopped closes a pipe when its reader or writer side is done. The goroutine on the
// other side then ends with this error, which is not a failure.
var errStreamStopped = errors.New("upload stopped")

// Storage keeps file records. Create must atomically claim the ID across notes, links and
// files, and return domain.ErrSlugConflict if another resource has it. Errors are fail-hard.
type Storage interface {
	Create(ctx context.Context, file *domain.File) error
	GetByID(ctx context.Context, id string) (*domain.File, error)
	Exists(ctx context.Context, id string) (bool, error)
	// Delete removes the record and returns it, so that the caller can remove its object.
	Delete(ctx context.Context, id string) (*domain.File, error)
	// Update writes the expiry, privacy and burn rule of the record with ID file.ID.
	// It returns domain.ErrNotFound when the record does not exist.
	Update(ctx context.Context, file *domain.File) error
	// ConsumeVisible atomically checks visibility and expiry or burn eligibility, then deletes
	// and returns the record. With allowBurn=false only an expired record qualifies.
	ConsumeVisible(
		ctx context.Context, id string, callerID uuid.UUID, authenticated, allowBurn bool,
	) (*domain.File, error)
	// IncrementDownloadsVisible refuses a record that is no longer visible to this reader.
	IncrementDownloadsVisible(ctx context.Context, id string, callerID uuid.UUID, authenticated bool) error
	// DeleteExpired removes the records that expired before now and returns their object keys.
	DeleteExpired(ctx context.Context, now time.Time) ([]string, error)
}

// ObjectStore keeps file content. Put, Stat and WriteTo are fail-hard. Delete is fail-soft:
// the record is already gone when Manager calls it, so a failure only leaves an object that
// nobody can read.
type ObjectStore interface {
	Put(ctx context.Context, key string, body io.Reader) error
	// Stat and WriteTo return domain.ErrNotFound when the object does not exist.
	Stat(ctx context.Context, key string) error
	WriteTo(ctx context.Context, key string, dst io.Writer) error
	Delete(ctx context.Context, key string) error
}

// StreamCipher encrypts file content with a key for each file.
type StreamCipher interface {
	NewKey() ([]byte, error)
	Encrypt(key []byte, dst io.Writer, src io.Reader) error
	Decrypt(key []byte, dst io.Writer, src io.Reader) error
}

// Encryptor seals the file metadata with the installation keyring. The record ID is
// authenticated context. Files have no legacy records, so Manager gives an empty legacySlug.
type Encryptor interface {
	Encrypt(plaintext, recordID string) (string, error)
	Decrypt(ciphertext, recordID, legacySlug string) (string, error)
}

// EditCodeHasher hashes and verifies edit codes. HashUserProvided is memory-hard, because the
// entropy of a code that the caller selects is not known.
type EditCodeHasher interface {
	Hash(code string) (string, error)
	HashUserProvided(code string) (string, error)
	Verify(storedHash, code string) bool
}

// SlugLock serializes local slug checks. Share the instance with notes.Manager and
// links.Manager; storage enforces uniqueness between server processes.
type SlugLock interface {
	Lock(ctx context.Context, key string) (unlock func(), err error)
}

// SlugChecker reports whether another resource (notes or links) has an ID.
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
// can consume a burn-after-download file.
type AccessOptions struct {
	CallerID      uuid.UUID
	Authenticated bool
	AllowBurn     bool
}

// sealedMeta is the plaintext of File.SealedMeta. One envelope for the name and the key stops
// a swap of the two values between columns.
type sealedMeta struct {
	Name string `json:"name"`
	Key  []byte `json:"key"`
}

// Download is a file download that is ready to send. Call Send once, then Close.
type Download struct {
	File *domain.File

	send    func(ctx context.Context, dst io.Writer) error
	cleanup func(ctx context.Context)
}

// NewDownload returns a Download of file whose Send calls send. Manager uses it; adapter tests
// use it for a download without object storage.
func NewDownload(file *domain.File, send func(ctx context.Context, dst io.Writer) error) *Download {
	return &Download{File: file, send: send, cleanup: nil}
}

// Send writes the plaintext of the file to dst.
func (d *Download) Send(ctx context.Context, dst io.Writer) error {
	return d.send(ctx, dst)
}

// Close deletes the object of a burn-after-download file. Give a context that is not canceled
// when the client disconnects.
func (d *Download) Close(ctx context.Context) {
	if d.cleanup != nil {
		d.cleanup(ctx)
	}
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

	m.sweepOnUpload(ctx)

	err = m.insert(ctx, file, opts.CustomSlug)
	if err != nil {
		m.deleteObject(ctx, file.ObjectKey)

		return nil, err
	}

	file.EditCode = plainCode

	m.metrics.IncUploads()
	m.metrics.ObserveUploadBytes(file.Size)
	m.deps.Log.DebugContext(ctx, "file created", "id", domain.HashSlug(file.ID), "size", file.Size)

	return file, nil
}

// Peek returns the file with decrypted metadata. It does not apply privacy, expiry or burn
// rules; callers that show a file to a reader use Info or Download.
func (m *Manager) Peek(ctx context.Context, slug string) (*domain.File, error) {
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

// Info returns the file for a reader after the privacy and expiry checks. An expired file is
// deleted and Info returns domain.ErrExpired.
func (m *Manager) Info(ctx context.Context, slug string, opts AccessOptions) (*domain.File, error) {
	file, err := m.Peek(ctx, slug)
	if err != nil {
		return nil, err
	}

	if !file.VisibleTo(opts.CallerID, opts.Authenticated) {
		return nil, domain.ErrForbidden
	}

	if file.Expired(time.Now()) {
		return nil, m.expire(ctx, slug, opts)
	}

	return file, nil
}

// Download prepares the file for a reader. It applies privacy and expiry, counts the download,
// and consumes a burn-after-download file when opts.AllowBurn is true. Without AllowBurn such a
// file stays and Download returns domain.ErrBurnIntentRequired. The object is checked before
// the record changes, so a missing object does not consume the file.
func (m *Manager) Download(ctx context.Context, slug string, opts AccessOptions) (*Download, error) {
	file, err := m.Info(ctx, slug, opts)
	if err != nil {
		return nil, err
	}

	if file.BurnAfterDownload && !opts.AllowBurn {
		return nil, domain.ErrBurnIntentRequired
	}

	err = m.statObject(ctx, file)
	if err != nil {
		return nil, err
	}

	objectKey, contentKey := file.ObjectKey, file.ContentKey
	download := NewDownload(file, func(ctx context.Context, dst io.Writer) error {
		return m.sendObject(ctx, objectKey, contentKey, dst)
	})

	err = m.record(ctx, slug, file, opts, download)
	if err != nil {
		return nil, err
	}

	// The counter has the same moment as the download counter in the record.
	m.metrics.IncDownloads()

	return download, nil
}

// Delete removes a file after a check of the edit code, or of the caller as the owner.
func (m *Manager) Delete(ctx context.Context, slug, editCode string, callerID uuid.UUID) error {
	recordID := domain.HashSlug(slug)

	existing, err := m.deps.Storage.GetByID(ctx, recordID)
	if err != nil {
		return fmt.Errorf("delete file: %w", err)
	}

	if !existing.OwnedBy(callerID) && !m.deps.Hasher.Verify(existing.EditCode, editCode) {
		return domain.ErrInvalidEditCode
	}

	deleted, err := m.deps.Storage.Delete(ctx, recordID)
	if err != nil {
		return fmt.Errorf("delete file: %w", err)
	}

	m.metrics.IncDeleted(DeleteReasonUser)
	m.deleteObject(ctx, deleted.ObjectKey)

	return nil
}

// SweepExpired removes the expired records and their objects. It returns the number of removed
// records. A record error stops the sweep and is returned. An object error is fail-soft: the
// record is already gone, so deleteObject only logs and counts it.
func (m *Manager) SweepExpired(ctx context.Context) (int, error) {
	keys, err := m.deps.Storage.DeleteExpired(ctx, time.Now())
	if err != nil {
		return 0, fmt.Errorf("sweep expired files: %w", err)
	}

	for _, key := range keys {
		m.metrics.IncDeleted(DeleteReasonExpired)
		m.deleteObject(ctx, key)
	}

	return len(keys), nil
}

func (m *Manager) newFile(name string, opts *CreateOptions) (*domain.File, string, error) {
	cleanName, err := domain.CleanFileName(name)
	if err != nil {
		return nil, "", fmt.Errorf("clean file name: %w", err)
	}

	// Files are served at single-segment routes (/{id} and /files/{id}).
	if strings.Contains(opts.CustomSlug, "/") {
		return nil, "", fmt.Errorf("custom file slug must be one path segment: %w", domain.ErrInvalidSlug)
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

// upload encrypts body into the object storage through a pipe. The encryption goroutine ends
// before upload returns, so the size counter is safe to read.
func (m *Manager) upload(ctx context.Context, file *domain.File, body io.Reader) error {
	key, err := m.deps.Cipher.NewKey()
	if err != nil {
		return fmt.Errorf("new file key: %w", err)
	}

	counter := &sizeLimitReader{src: body, limit: m.maxSize, total: 0}
	pipeReader, pipeWriter := io.Pipe()
	encrypted := make(chan error, 1)

	go func() {
		encErr := m.deps.Cipher.Encrypt(key, pipeWriter, counter)
		encrypted <- errors.Join(encErr, pipeWriter.CloseWithError(encErr))
	}()

	putErr := m.deps.Objects.Put(ctx, file.ObjectKey, pipeReader)
	putErr = errors.Join(putErr, pipeReader.CloseWithError(errStreamStopped))

	encErr := <-encrypted

	err = m.uploadError(ctx, counter, putErr, encErr)
	if err != nil {
		// An interrupted multipart upload can leave a part; Delete is safe for a missing key.
		m.deleteObject(ctx, file.ObjectKey)

		return err
	}

	file.ContentKey = key
	file.Size = counter.total

	return nil
}

// uploadError selects the error of an upload. The size limit and a canceled request are the
// caller's errors; any other error is a storage failure.
func (m *Manager) uploadError(ctx context.Context, counter *sizeLimitReader, putErr, encErr error) error {
	switch {
	case counter.total > m.maxSize:
		return domain.ErrFileTooLarge
	case putErr == nil && encErr == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("upload file: %w", ctx.Err())
	case encErr != nil && !errors.Is(encErr, errStreamStopped):
		// The client stopped sending, or the body was not readable.
		return fmt.Errorf("read upload: %w", encErr)
	default:
		m.metrics.IncStorageErrors(StorageOpPut)
		m.deps.Log.ErrorContext(ctx, "upload file", "err", putErr)

		return fmt.Errorf("upload file: %w", errors.Join(domain.ErrStorageUnavailable, putErr))
	}
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

func (m *Manager) slugTaken(ctx context.Context, id string) (bool, error) {
	checkers := []SlugChecker{m.deps.Notes, m.deps.Links, m.deps.Storage}
	for _, checker := range checkers {
		taken, err := checker.Exists(ctx, id)
		if err != nil {
			return false, fmt.Errorf("check slug: %w", err)
		}

		if taken {
			return true, nil
		}
	}

	return false, nil
}

// sweepOnUpload does SweepExpired on each upload. The background sweep can be off, so this
// cleanup stays. A failure only delays the cleanup to the next sweep.
func (m *Manager) sweepOnUpload(ctx context.Context) {
	_, err := m.SweepExpired(ctx)
	if err != nil {
		m.deps.Log.WarnContext(ctx, "file sweep failed", "err", err)
	}
}

// deleteObject removes an object after its record is gone. A failure leaves an object that
// nobody can read, because its key was in the record. The warning and the "delete" storage
// error counter are the signal for the operator. The log line does not contain the object key:
// with the key and the bucket, a reader of the logs can find the content of one file.
func (m *Manager) deleteObject(ctx context.Context, key string) {
	err := m.deps.Objects.Delete(context.WithoutCancel(ctx), key)
	if err != nil {
		m.metrics.IncStorageErrors(StorageOpDelete)
		m.deps.Log.WarnContext(ctx, "delete file object", "err", err)
	}
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

func (m *Manager) expire(ctx context.Context, slug string, opts AccessOptions) error {
	consumed, err := m.deps.Storage.ConsumeVisible(
		ctx, domain.HashSlug(slug), opts.CallerID, opts.Authenticated, false)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}

	if err != nil {
		m.deps.Log.ErrorContext(ctx, "delete expired file", "id", domain.HashSlug(slug), "err", err)

		return domain.ErrExpired
	}

	m.metrics.IncDeleted(DeleteReasonExpired)
	m.deleteObject(ctx, consumed.ObjectKey)

	return domain.ErrExpired
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

// record consumes a burn file or counts the download of another file.
func (m *Manager) record(
	ctx context.Context, slug string, file *domain.File, opts AccessOptions, download *Download,
) error {
	recordID := domain.HashSlug(slug)

	if file.BurnAfterDownload {
		consumed, err := m.deps.Storage.ConsumeVisible(ctx, recordID, opts.CallerID, opts.Authenticated, true)
		if err != nil {
			return fmt.Errorf("consume file: %w", err)
		}

		m.metrics.IncDeleted(DeleteReasonBurn)

		objectKey := consumed.ObjectKey
		download.cleanup = func(ctx context.Context) { m.deleteObject(ctx, objectKey) }

		return nil
	}

	err := m.deps.Storage.IncrementDownloadsVisible(ctx, recordID, opts.CallerID, opts.Authenticated)
	if errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("count download: %w", err)
	}

	// The counter is statistics only: a failure does not stop the download.
	if err != nil {
		m.deps.Log.ErrorContext(ctx, "count file download", "id", recordID, "err", err)
	} else {
		file.Downloads++
	}

	return nil
}

// sizeLimitReader counts bytes and fails with domain.ErrFileTooLarge after limit bytes.
type sizeLimitReader struct {
	src   io.Reader
	limit int64
	total int64
}

func (r *sizeLimitReader) Read(dst []byte) (int, error) {
	if r.total > r.limit {
		return 0, domain.ErrFileTooLarge
	}

	// One byte more than the limit is enough to know that the file is too large.
	chunk := dst
	if remaining := r.limit - r.total + 1; int64(len(chunk)) > remaining {
		chunk = chunk[:remaining]
	}

	n, err := r.src.Read(chunk)
	r.total += int64(n)

	switch {
	case r.total > r.limit:
		return n, domain.ErrFileTooLarge
	case errors.Is(err, io.EOF):
		// The io.Reader contract needs io.EOF itself, not a wrapped error.
		return n, io.EOF
	case err != nil:
		return n, fmt.Errorf("read upload: %w", err)
	default:
		return n, nil
	}
}
