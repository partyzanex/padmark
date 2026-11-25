//go:build integration

// Package files_test runs the server with SQLite and an S3-compatible container. It checks
// the file life cycle through HTTP, a server restart and a content key rotation.
package files_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/pkg/client"
	"github.com/partyzanex/padmark/tests/internal/servertest"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

const (
	// bigFileSize is larger than one 16 MiB multipart part, so the upload and the download
	// cross a part boundary.
	bigFileSize   = 17<<20 + 123
	smallFileSize = 1<<20 + 7
	mimeBinary    = "application/octet-stream"
	requestLimit  = time.Minute
	fileName      = "отчёт 2026.bin"
)

// FilesSuite runs the server binary against an S3-compatible container.
type FilesSuite struct {
	suite.Suite

	// bucket is the bucket of the shared S3-compatible container; objects reads it directly.
	bucket  testenv.S3
	objects *s3store.Store
	binary  string
}

func TestFilesSuite(t *testing.T) {
	suite.Run(t, new(FilesSuite))
}

func (s *FilesSuite) SetupSuite() {
	s.bucket = testenv.S3Mock(s.T())
	s.objects = s.bucket.Store(s.T())
	s.binary = servertest.Build(s.T())
}

// TestLifecycle uploads a file, reads it through /{id} and the download path, changes its
// rules, deletes it and checks that the record and the object are gone.
func (s *FilesSuite) TestLifecycle() {
	inst := newInstallation(s.T())
	baseURL, _ := startServer(s.T(), s.binary, s.bucket, inst)
	content := randomBytes(s.T(), bigFileSize)

	created := s.upload(baseURL, content)
	s.Equal(fileName, created.Name)
	s.Equal(int64(bigFileSize), created.Size)
	s.NotEmpty(created.EditCode)
	s.False(created.ExpiresAt.Set, "a file without ttl does not expire")

	s.assertReads(baseURL, created.ID, content)
	s.assertUpdate(baseURL, created.ID, created.EditCode)

	objectKey := s.objectKey(inst, created.ID)
	s.Require().NoError(s.objects.Stat(s.T().Context(), objectKey))

	s.assertDelete(baseURL, created.ID, created.EditCode)
	s.Require().ErrorIs(s.objects.Stat(s.T().Context(), objectKey), domain.ErrNotFound,
		"delete removes the object from the bucket")
}

// assertReads reads the file through GET /{id} as a script and as a browser, through the
// download path and through the metadata path.
func (s *FilesSuite) assertReads(baseURL, id string, content []byte) {
	status, header, body := s.request(http.MethodGet, baseURL+"/"+id, mimeBinary, "", nil)
	s.Require().Equal(http.StatusOK, status)
	s.Equal(mimeBinary, header.Get("Content-Type"))
	s.Contains(header.Get("Content-Disposition"), "attachment")
	s.True(bytes.Equal(content, body), "GET /{id} returned other bytes")

	status, header, _ = s.request(http.MethodGet, baseURL+"/"+id, "text/html", "", nil)
	s.Equal(http.StatusOK, status)
	s.Contains(header.Get("Content-Type"), "text/html", "a browser gets the file page")

	s.True(bytes.Equal(content, s.download(baseURL, id)), "the download returned other bytes")

	info := s.metadata(baseURL, id)
	s.Equal(fileName, info.Name)
	s.Equal(int64(len(content)), info.Size)
	s.Equal(2, info.Downloads, "GET /{id} and the download count; metadata and the page do not")
}

// assertUpdate changes the expiry and the burn rule. A wrong edit code changes nothing.
func (s *FilesSuite) assertUpdate(baseURL, id, editCode string) {
	update := []byte(`{"ttl":3600,"burn_after_download":true}`)

	status, _, _ := s.request(http.MethodPut, baseURL+"/files/"+id, "application/json", "wrong-code-123", update)
	s.Equal(http.StatusForbidden, status, "a wrong edit code must not change the file")
	s.False(s.metadata(baseURL, id).BurnAfterDownload)

	status, _, body := s.request(http.MethodPut, baseURL+"/files/"+id, "application/json", editCode, update)
	s.Require().Equal(http.StatusOK, status, string(body))

	var updated fileInfo

	s.Require().NoError(json.Unmarshal(body, &updated))
	s.True(updated.BurnAfterDownload)
	s.Require().NotNil(updated.ExpiresAt)
	s.WithinDuration(time.Now().Add(time.Hour), *updated.ExpiresAt, time.Minute)
	s.Empty(updated.EditCode, "an update does not show the edit code")
	s.Equal(updated.BurnAfterDownload, s.metadata(baseURL, id).BurnAfterDownload, "the change is stored")
}

// assertDelete deletes the file. After that each read path answers 404.
func (s *FilesSuite) assertDelete(baseURL, id, editCode string) {
	status, _, _ := s.request(http.MethodDelete, baseURL+"/files/"+id, "", "wrong-code-123", nil)
	s.Equal(http.StatusForbidden, status)

	status, _, _ = s.request(http.MethodDelete, baseURL+"/files/"+id, "", editCode, nil)
	s.Require().Equal(http.StatusNoContent, status)

	for _, target := range []string{"/files/" + id, "/files/" + id + "/download", "/" + id} {
		status, _, _ = s.request(http.MethodGet, baseURL+target, mimeBinary, "", nil)
		s.Equal(http.StatusNotFound, status, target)
	}
}

// TestRestart checks that a file stays available after the server stops and starts again.
func (s *FilesSuite) TestRestart() {
	inst := newInstallation(s.T())
	baseURL, stop := startServer(s.T(), s.binary, s.bucket, inst)
	content := randomBytes(s.T(), smallFileSize)
	created := s.upload(baseURL, content)

	stop()

	baseURL, _ = startServer(s.T(), s.binary, s.bucket, inst)

	s.Equal(fileName, s.metadata(baseURL, created.ID).Name)
	s.True(bytes.Equal(content, s.download(baseURL, created.ID)), "the file changed after the restart")
}

// TestKeyRotation follows the README rotation procedure from k1 to k2. After key-remove the
// keyring has no k1, so the file is readable only when rewrap changed its sealed metadata.
func (s *FilesSuite) TestKeyRotation() {
	inst := newInstallation(s.T())
	baseURL, stop := startServer(s.T(), s.binary, s.bucket, inst)
	content := randomBytes(s.T(), smallFileSize)
	created := s.upload(baseURL, content)

	stop()

	next := filepath.Join(inst.directory, "keys-next.json")
	active := filepath.Join(inst.directory, "keys-active.json")
	current := filepath.Join(inst.directory, "keys-current.json")

	runEncryption(s.T(), inst, inst.keyring, "key-add", "--key-id", "k2", "--output", next)
	runEncryption(s.T(), inst, next, "register", "--key-id", "k2")
	runEncryption(s.T(), inst, next, "key-activate", "--key-id", "k2", "--output", active)

	var report struct {
		Changed int `json:"changed"`
	}

	s.Require().NoError(json.Unmarshal([]byte(runEncryption(s.T(), inst, active, "rewrap")), &report))
	s.Equal(1, report.Changed, "rewrap changes the sealed metadata of the file")
	runEncryption(s.T(), inst, active, "key-remove", "--key-id", "k1", "--output", current)

	s.Contains(s.sealedMeta(inst, created.ID), "$k2$", "the sealed metadata uses the new key")

	inst.keyring = current
	baseURL, _ = startServer(s.T(), s.binary, s.bucket, inst)

	s.Equal(fileName, s.metadata(baseURL, created.ID).Name)
	s.True(bytes.Equal(content, s.download(baseURL, created.ID)), "the file changed after the rotation")
}

// fileInfo is the JSON body of GET /files/{id} and PUT /files/{id}.
type fileInfo struct {
	ExpiresAt         *time.Time `json:"expires_at"`
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	EditCode          string     `json:"edit_code"`
	Size              int64      `json:"size"`
	Downloads         int        `json:"downloads"`
	BurnAfterDownload bool       `json:"burn_after_download"`
}

func (s *FilesSuite) newClient(baseURL string) *client.Client {
	api, err := client.NewClient(baseURL, client.WithClient(&http.Client{
		Timeout: requestLimit, Transport: nil, CheckRedirect: nil, Jar: nil,
	}))
	s.Require().NoError(err)

	return api
}

func (s *FilesSuite) upload(baseURL string, content []byte) *client.CreateFileResponse {
	created, err := s.newClient(baseURL).UploadFile(s.T().Context(), fileName, bytes.NewReader(content),
		client.UploadFileOptions{TTL: 0, Privacy: "", Slug: "", EditCode: "", BurnAfterDownload: false})
	s.Require().NoError(err)

	return created
}

func (s *FilesSuite) download(baseURL, id string) []byte {
	var out bytes.Buffer

	downloaded, err := s.newClient(baseURL).DownloadFile(s.T().Context(), id, &out)
	s.Require().NoError(err)
	s.Equal(fileName, downloaded.Name)
	s.Equal(int64(out.Len()), downloaded.Size)

	return out.Bytes()
}

func (s *FilesSuite) metadata(baseURL, id string) fileInfo {
	status, _, body := s.request(http.MethodGet, baseURL+"/files/"+id, "application/json", "", nil)
	s.Require().Equal(http.StatusOK, status, string(body))

	var info fileInfo

	s.Require().NoError(json.Unmarshal(body, &info))

	return info
}

// request sends one request and returns the status, the headers and the full body. A JSON body
// gets its Content-Type; a non-empty editCode goes into X-Edit-Code.
func (s *FilesSuite) request(method, target, accept, editCode string, body []byte) (int, http.Header, []byte) {
	request, err := http.NewRequestWithContext(s.T().Context(), method, target, bytes.NewReader(body))
	s.Require().NoError(err)

	if accept != "" {
		request.Header.Set("Accept", accept)
	}

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	if editCode != "" {
		request.Header.Set("X-Edit-Code", editCode)
	}

	httpClient := &http.Client{
		Timeout: requestLimit, Transport: nil, Jar: nil,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer httpClient.CloseIdleConnections()

	response, err := httpClient.Do(request)
	s.Require().NoError(err)

	defer func() { s.NoError(response.Body.Close()) }()

	raw, err := io.ReadAll(response.Body)
	s.Require().NoError(err)

	return response.StatusCode, response.Header, raw
}

// objectKey and sealedMeta read the file record directly. The server keeps running: SQLite
// lets a second connection read the database.
func (s *FilesSuite) objectKey(inst installation, slug string) string {
	return s.fileColumn(inst, slug, "object_key")
}

func (s *FilesSuite) sealedMeta(inst installation, slug string) string {
	return s.fileColumn(inst, slug, "sealed_meta")
}

func (s *FilesSuite) fileColumn(inst installation, slug, column string) string {
	db, err := sqliterepo.Open(inst.dsn, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	defer func() { s.NoError(db.Close()) }()

	var value string

	s.Require().NoError(db.NewSelect().Table("files").Column(column).Where("id = ?", domain.HashSlug(slug)).
		Scan(s.T().Context(), &value))
	s.Require().NotEmpty(value)

	return value
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()

	content := make([]byte, size)

	_, err := rand.Read(content)
	if err != nil {
		t.Fatalf("random content: %v", err)
	}

	return content
}
