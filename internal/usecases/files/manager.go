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

const (
	// maxSlugRetries limits the retries after a random slug collision. A custom slug never retries.
	maxSlugRetries = 5
	// recordCheckTimeout limits the read of a record after a Create error. The read does not
	// stop with the request, so this limit stops it when the database does not respond.
	recordCheckTimeout = 10 * time.Second
)

// errRecordUnknown tells that Create failed and that the read after it also failed. Thus the
// record can exist, and Manager must not delete its object.
var errRecordUnknown = errors.New("file record state is unknown")

// Storage keeps file records. Errors are fail-hard: Manager returns them to the caller.
//
// Each write names the record by ID and by the object key from the read. The object key is the
// identity of one upload: when a new file has the same slug, a write from an old read finds no
// row and returns domain.ErrNotFound, so it cannot change or delete the new file.
type Storage interface {
	// Create inserts the record. It must atomically claim the ID across notes, links and
	// files, and return domain.ErrSlugConflict if another resource has it: it is the only slug
	// uniqueness check (docs/adr/0003-storage-slug-uniqueness.md). Another error does
	// not prove that the record is absent: the database can write the record and lose the
	// reply. Manager then reads the record with GetByID.
	Create(ctx context.Context, file *domain.File) error
	// GetByID returns the record. While a burn claim that is not stale at now holds the record,
	// it returns domain.ErrNotFound: other readers must not see a file that a download consumes.
	GetByID(ctx context.Context, id string, now time.Time) (*domain.File, error)
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
	// a burn file and is not expired at now. Otherwise it returns domain.ErrNotFound.
	IncrementDownloadsVisible(ctx context.Context, id, objectKey string, reader domain.Reader, now time.Time) error
	// ListExpired returns at most limit records that expired before now and have no claim that
	// is not stale. It orders them by expiry, then by ID, and returns only the records after
	// the cursor. Only ID, ObjectKey and ExpiresAt must be set.
	ListExpired(ctx context.Context, now time.Time, after ExpiredCursor, limit int) ([]*domain.File, error)
	// DeleteExpired removes the record with id and objectKey while it is expired at now and
	// has no claim that is not stale.
	DeleteExpired(ctx context.Context, id, objectKey string, now time.Time) error
}

// ObjectStore keeps file content. Put, Stat and WriteTo are fail-hard. Delete is fail-soft:
// Manager logs and counts a failure. After a user delete or a burn download the record is
// already gone, so a failure leaves an object that nobody can read. An expired file keeps its
// record when Delete fails, so a later sweep tries again.
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

// ExpiredSlugReleaser removes an expired note, link or file that still holds the record ID id,
// because no read and no sweep removed it yet. It returns true when it removed a record. It must
// remove a record only while it is still expired at now, so a record that its owner extended
// stays. Errors are fail-soft: Create keeps its slug conflict and logs the error.
type ExpiredSlugReleaser interface {
	ReleaseExpiredSlug(ctx context.Context, id string, now time.Time) (bool, error)
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
	// ExpiredSlugs can be the composite that also removes files through this Manager's
	// ReleaseExpiredSlug: the composition root connects it before the first request.
	ExpiredSlugs ExpiredSlugReleaser
	Log          *slog.Logger
	// Now is the time source of each expiry decision, burn claim and timestamp. The composition
	// root gives time.Now; tests give a fixed clock.
	Now func() time.Time
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

// BurnClaim identifies one download of a burn-after-download file. Token is random for each
// download, so that only this download can finish or release its claim.
type BurnClaim struct {
	Now       time.Time
	Until     time.Time
	ID        string
	ObjectKey string
	Token     string
	Reader    domain.Reader
}

// sealedMeta is the plaintext of File.SealedMeta. One envelope for the name and the key stops
// a swap of the two values between columns.
type sealedMeta struct {
	Name string `json:"name"`
	Key  []byte `json:"key"`
}

// Manager implements the file life cycle: upload, metadata, download and delete.
type Manager struct {
	metrics Metrics
	deps    *Deps
	// backgroundPos and uploadPos keep the position of the last sweep of each kind; see sweepCursor.
	backgroundPos      *sweepCursor
	uploadPos          *sweepCursor
	maxSize            int64
	uploadSweepTimeout time.Duration
	allowCustomSlugs   domain.CustomSlugSetting
}

// NewManager returns a Manager. maxSize is the largest permitted file in bytes.
// allowCustomSlugs has the same meaning as for notes and links (--custom-slugs).
// The Manager records no metrics until WithMetrics attaches them.
func NewManager(deps *Deps, maxSize int64, allowCustomSlugs bool) *Manager {
	return &Manager{
		metrics:            noopMetrics{},
		deps:               deps,
		backgroundPos:      newSweepCursor(),
		uploadPos:          newSweepCursor(),
		maxSize:            maxSize,
		uploadSweepTimeout: uploadSweepTimeout,
		allowCustomSlugs:   domain.CustomSlugSetting(allowCustomSlugs),
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
// time; the record insert follows (see insert). When the insert fails, Create deletes
// the object only if the record is surely absent; see confirmCreate.
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
		// When the record state is unknown, the object stays: a record without its object is
		// worse than an object without a record. Otherwise the object has no record. The
		// request can be canceled, but the cleanup must still run.
		if !errors.Is(err, errRecordUnknown) {
			m.deleteObject(context.WithoutCancel(ctx), file.ObjectKey)
		}

		return nil, err
	}

	file.EditCode = plainCode

	m.metrics.IncUploads()
	m.metrics.ObserveUploadBytes(file.Size)
	m.deps.Log.DebugContext(ctx, "file created", "id", domain.HashSlug(file.ID), "size", file.Size)

	m.sweepOnUpload(ctx)

	return file, nil
}

// Inspect returns the file for reader without a side effect on a live file, as
// notes.Manager.Inspect does: privacy (domain.ErrForbidden) and expiry (an expired file is
// deleted, and Inspect returns domain.ErrExpired). It never counts or consumes a download.
func (m *Manager) Inspect(ctx context.Context, slug string, reader domain.Reader) (*domain.File, error) {
	file, err := m.readVisible(ctx, slug, reader)
	if err != nil {
		return nil, err
	}

	dropKey(file)

	return file, nil
}

// Settings returns the file for the settings editor without any side effect: privacy
// (domain.ErrForbidden), then expiry (domain.ErrExpired). Unlike Inspect it never deletes an
// expired file: the first reader of the file page gets 410 and deletes it, as before.
func (m *Manager) Settings(ctx context.Context, slug string, reader domain.Reader) (*domain.File, error) {
	file, err := m.readFile(ctx, slug)
	if err != nil {
		return nil, err
	}

	dropKey(file)

	if !file.VisibleTo(reader) {
		return nil, domain.ErrForbidden
	}

	if file.Expired(m.deps.Now()) {
		return nil, domain.ErrExpired
	}

	return file, nil
}

// Download prepares the file for reader, the file counterpart of notes.Manager.Read. It applies
// privacy and expiry, counts the download, and claims a burn-after-download file when
// reader.AllowBurn is true. Without AllowBurn such a file stays and Download returns
// domain.ErrBurnIntentRequired. The object is checked before the record changes, so a missing
// object does not consume the file. A burn file is deleted only when Send completes; see
// Download.Close.
func (m *Manager) Download(ctx context.Context, slug string, reader domain.Reader) (*Download, error) {
	file, err := m.readVisible(ctx, slug, reader)
	if err != nil {
		return nil, err
	}

	if file.BurnAfterDownload && !reader.AllowBurn {
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

	err = m.record(ctx, file, reader, download)
	if err != nil {
		dropKey(file)

		return nil, err
	}

	// The counter has the same moment as the download counter in the record.
	m.metrics.IncDownloads()

	return download, nil
}

// Delete removes a file with the rules of checkMutation. The record goes first, so that no
// reader gets the file after Delete returns. Then the object goes; a failure there is fail-soft
// and leaves an object that nobody can read.
func (m *Manager) Delete(ctx context.Context, slug, editCode string, reader domain.Reader) error {
	recordID := domain.HashSlug(slug)

	existing, err := m.deps.Storage.GetByID(ctx, recordID, m.deps.Now())
	if err != nil {
		return fmt.Errorf("delete file %s: %w", recordID, err)
	}

	err = m.checkMutation(existing, editCode, reader)
	if err != nil {
		return err
	}

	err = m.deps.Storage.Delete(ctx, recordID, existing.ObjectKey)
	if err != nil {
		return fmt.Errorf("delete file %s: %w", recordID, err)
	}

	m.metrics.IncDeleted(DeleteReasonUser)
	// The record is gone. A client that disconnects now must not stop the object delete.
	m.deleteObject(context.WithoutCancel(ctx), existing.ObjectKey)

	return nil
}

// checkMutation applies the rules of a change to the stored file existing, as notes.Manager
// does: the reader must see the file (domain.ErrForbidden), and must be its owner or give its
// edit code (domain.ErrInvalidEditCode).
func (m *Manager) checkMutation(existing *domain.File, editCode string, reader domain.Reader) error {
	if !existing.VisibleTo(reader) {
		return domain.ErrForbidden
	}

	if !existing.OwnedBy(reader.CallerID) && !m.deps.Hasher.Verify(existing.EditCode, editCode) {
		return domain.ErrInvalidEditCode
	}

	return nil
}

// readFile reads the record and decrypts its metadata. The result has the content key.
func (m *Manager) readFile(ctx context.Context, slug string) (*domain.File, error) {
	recordID := domain.HashSlug(slug)

	file, err := m.deps.Storage.GetByID(ctx, recordID, m.deps.Now())
	if err != nil {
		return nil, fmt.Errorf("get file %s: %w", recordID, err)
	}

	err = m.openMeta(ctx, slug, file)
	if err != nil {
		return nil, err
	}

	return file, nil
}

// readVisible is Inspect that keeps the content key in the result, for Download.
func (m *Manager) readVisible(ctx context.Context, slug string, reader domain.Reader) (*domain.File, error) {
	file, err := m.readFile(ctx, slug)
	if err != nil {
		return nil, err
	}

	if !file.VisibleTo(reader) {
		dropKey(file)

		return nil, domain.ErrForbidden
	}

	now := m.deps.Now()
	if file.Expired(now) {
		dropKey(file)

		return nil, m.expire(ctx, file, now)
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
		CreatedAt:         m.deps.Now().UTC(),
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

// insert resolves the slug and writes the record. Storage.Create is the only uniqueness check. A
// random slug that is taken gets a new random slug, at most maxSlugRetries times. A custom slug
// that is taken stays taken, except when an expired record holds it.
func (m *Manager) insert(ctx context.Context, file *domain.File, customSlug string) error {
	slug, err := domain.ResolveSlug(customSlug, m.allowCustomSlugs)
	if err != nil {
		return fmt.Errorf("resolve slug: %w", err)
	}

	for range maxSlugRetries {
		err = m.insertAs(ctx, file, slug)
		// A custom slug that an expired record holds gets one more attempt after the removal, as
		// for notes. The storage triggers check the slug again, so this is safe across processes.
		if customSlug != "" && errors.Is(err, domain.ErrSlugConflict) &&
			m.releaseExpiredHolder(ctx, domain.HashSlug(slug), file.CreatedAt) {
			err = m.insertAs(ctx, file, slug)
		}

		if customSlug != "" || !errors.Is(err, domain.ErrSlugConflict) {
			return err
		}

		slug, err = domain.ResolveSlug("", m.allowCustomSlugs)
		if err != nil {
			return fmt.Errorf("resolve slug: %w", err)
		}
	}

	return domain.ErrHashCollision
}

// insertAs seals the metadata for the record ID of slug and writes the record.
func (m *Manager) insertAs(ctx context.Context, file *domain.File, slug string) error {
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

	// stored is a separate value with the storage form: the record ID is the hashed slug. The
	// caller's file never holds the hash in ID.
	stored := *file
	stored.ID = recordID
	stored.SealedMeta = sealed

	err = m.deps.Storage.Create(ctx, &stored)
	if err != nil && !errors.Is(err, domain.ErrSlugConflict) {
		err = m.confirmCreate(ctx, recordID, file.ObjectKey, err)
	}

	file.ID = slug
	file.SealedMeta = sealed

	if err != nil {
		return fmt.Errorf("create file %s: %w", recordID, err)
	}

	return nil
}

// confirmCreate reads the record after a Create error. The database can write the record and
// lose the reply, for example when the connection breaks or the request is canceled. It returns
// nil when the record with objectKey exists, createErr when it does not, and an error with
// errRecordUnknown when the read fails. A slug conflict needs no read: the database refused the
// row. The object key is new for each upload, so a record with another key is not this upload.
func (m *Manager) confirmCreate(ctx context.Context, recordID, objectKey string, createErr error) error {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordCheckTimeout)
	defer cancel()

	existing, err := m.deps.Storage.GetByID(checkCtx, recordID, m.deps.Now())

	switch {
	case err == nil && existing.ObjectKey == objectKey:
		m.deps.Log.WarnContext(ctx, "file record exists after a create error", "id", recordID, "err", createErr)

		return nil
	case err == nil, errors.Is(err, domain.ErrNotFound):
		return createErr
	default:
		m.deps.Log.ErrorContext(ctx, "check file record after a create error",
			"id", recordID, "create_err", createErr, "err", err)

		return errors.Join(errRecordUnknown, createErr, err)
	}
}

// releaseExpiredHolder reports whether an expired note, link or file with id was removed. Errors
// are fail-soft: Create keeps its slug conflict.
func (m *Manager) releaseExpiredHolder(ctx context.Context, id string, now time.Time) bool {
	released, err := m.deps.ExpiredSlugs.ReleaseExpiredSlug(ctx, id, now)
	if err != nil {
		m.deps.Log.WarnContext(ctx, "release expired slug", "id", id, "err", err)

		return false
	}

	if released {
		m.deps.Log.DebugContext(ctx, "expired record released its slug", "id", id)
	}

	return released
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
