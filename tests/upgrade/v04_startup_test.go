//go:build integration

package upgrade_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/server"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/pkg/client"
	"github.com/partyzanex/padmark/tests/internal/servertest"
)

// versionFiles is the goose version of 20261118000001_files.sql, the first v0.5.0 migration.
const versionFiles = 20261118000001

// Test values for the throwaway S3 container only; s3mock accepts any credentials.
const (
	s3AccessKey = "padmark-test"
	s3SecretKey = "padmark-test-secret"
	s3Bucket    = "padmark-files"
	// v05FileSlug is a custom file slug that no v0.4.0 record has.
	v05FileSlug = "v050-file"
)

// v04Record is one note or link of tests/fixtures/v0.4.0. Value is the note content or the
// link target URL.
type v04Record struct {
	Slug     string
	EditCode string
	Value    string
}

// v04Fixture holds the records of one v0.4.0 dump in scenario order.
type v04Fixture struct {
	Notes []v04Record
	Links []v04Record
}

// Indexes of the scenario records in tests/fixtures/v0.4.0/README.md.
const (
	v04NoteUnicode = iota
	v04NotePlain
	v04NoteBurn
	v04NoteTTL
	v04NoteNested
)

const (
	v04LinkCustom = iota
	v04LinkRandom
	v04LinkClick
)

// v04Fixtures returns the records of the v0.4.0 dumps. The random IDs and the generated edit
// codes come from the dump headers; all values belong to throwaway test databases.
func v04Fixtures() map[string]v04Fixture {
	notes := func(plainSlug, plainCode, burnCode, ttlCode, nestedCode string) []v04Record {
		return []v04Record{
			{Slug: "v040-unicode", EditCode: "fixture-code-1", Value: "# Привет 🌍\nPrice: $42; `echo $HOME`"},
			{Slug: plainSlug, EditCode: plainCode, Value: "plain <b>text</b>"},
			{Slug: "v040-burn", EditCode: burnCode, Value: "read once"},
			{Slug: "v040-ttl", EditCode: ttlCode, Value: "expires in ten years"},
			{Slug: "docs/v040-nested", EditCode: nestedCode, Value: "nested slug"},
		}
	}
	links := func(randomSlug, randomCode, clickCode string) []v04Record {
		return []v04Record{
			{Slug: "v040-link", EditCode: "fixture-link-1", Value: "https://example.com/v040?q=1&x=%D0%9F"},
			{Slug: randomSlug, EditCode: randomCode, Value: "https://example.org/random"},
			{Slug: "v040-click", EditCode: clickCode, Value: "https://example.net/once"},
		}
	}

	return map[string]v04Fixture{
		"sqlite": {
			Notes: notes("oVopU3MisC", "osG6eRfQnny5", "MdUSfP1SICsk", "LTZBQjBpGMx4", "mEgQ3cQpDuQV"),
			Links: links("UZmqaTScIE", "vZWrvgyEci43", "0Mi8ZZLK1Rxg"),
		},
		"postgres": {
			Notes: notes("016Rshj5KX", "m8ZByWM85q7q", "6NjqIkzUGT6Q", "FrQ2ThQp8RRw", "2Who6mz005gD"),
			Links: links("pdKizEUhpa", "ykB2yQEoaeOa", "cTrRXfMpHgCd"),
		},
	}
}

// v04Snapshot is the stored state that the v0.5.0 schema migration must keep.
type v04Snapshot struct {
	Notes     []map[string]any
	Links     []map[string]any
	Namespace []map[string]any
}

// V04UpgradeSuite starts the current server on databases written by a real v0.4.0 server. The
// server applies the v0.5.0 files migration at startup; notes, links and the slug namespace
// must stay the same and stay readable.
type V04UpgradeSuite struct {
	suite.Suite

	objects testcontainers.Container
	binary  string
	// s3Args are the serve flags and s3Env the environment that turn file publishing on.
	s3Args []string
	s3Env  []string
}

func TestV04Upgrade(t *testing.T) {
	suite.Run(t, new(V04UpgradeSuite))
}

func (s *V04UpgradeSuite) SetupSuite() {
	ctx := s.T().Context()

	// adobe/s3mock makes the bucket at startup and does not check signatures.
	container, err := testcontainers.Run(ctx, "adobe/s3mock:3.12.0",
		testcontainers.WithEnv(map[string]string{"initialBuckets": s3Bucket}),
		testcontainers.WithExposedPorts("9090/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("9090/tcp").WithStartupTimeout(time.Minute)),
	)
	s.Require().NoError(err)

	s.objects = container

	hostPort, err := container.PortEndpoint(ctx, "9090/tcp", "")
	s.Require().NoError(err)

	host, _, err := net.SplitHostPort(hostPort)
	s.Require().NoError(err)

	s.s3Args = []string{"--s3-endpoint", "http://" + hostPort, "--s3-bucket", s3Bucket, "--s3-path-style"}
	s.s3Env = []string{server.EnvS3AccessKey + "=" + s3AccessKey, server.EnvS3SecretKey + "=" + s3SecretKey}

	// The server refuses plain http:// to a host that is not loopback. A remote Docker host
	// gives such a host; the container is a throwaway test store.
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		s.s3Env = append(s.s3Env, server.EnvS3AllowInsecure+"=true")
	}

	s.binary = servertest.Build(s.T())
}

func (s *V04UpgradeSuite) TearDownSuite() {
	if s.objects != nil {
		s.NoError(testcontainers.TerminateContainer(s.objects))
	}
}

func (s *V04UpgradeSuite) TestSQLite() {
	directory := s.T().TempDir()
	dsn := filepath.Join(directory, "padmark.db")

	// The dump changes connection pragmas, so it runs on a throwaway handle.
	loader, err := sqliterepo.Open(dsn, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.loadDump(loader, "sqlite")
	s.Require().NoError(loader.Close())

	db, err := sqliterepo.Open(dsn, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.NoError(db.Close()) })

	s.runUpgrade(directory, "sqlite", dsn, db)
}

func (s *V04UpgradeSuite) TestPostgres() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("padmark"), tcpostgres.WithUsername("test"), tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies())
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.NoError(testcontainers.TerminateContainer(container)) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	s.Require().NoError(err)

	// pg_dump empties search_path for its session, so the dump runs on a throwaway pool.
	loader := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())
	s.loadDump(loader, "postgres")
	s.Require().NoError(loader.Close())

	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())

	s.T().Cleanup(func() { s.NoError(db.Close()) })
	s.runUpgrade(s.T().TempDir(), "postgres", dsn, db)
}

func (s *V04UpgradeSuite) loadDump(db *bun.DB, storage string) {
	// os.DirFS keeps the read inside the fixture directory.
	dump, err := fs.ReadFile(os.DirFS(filepath.Join("..", "fixtures", "v0.4.0")), storage+".sql")
	s.Require().NoError(err)

	_, err = db.ExecContext(s.T().Context(), string(dump))
	s.Require().NoError(err)
}

func (s *V04UpgradeSuite) runUpgrade(directory, storage, dsn string, db *bun.DB) {
	fixture := v04Fixtures()[storage]

	s.Require().False(s.migrationApplied(db), "the v0.4.0 dump must not have the files migration")

	before := s.snapshot(db)
	s.Require().Len(before.Notes, len(fixture.Notes))
	s.Require().Len(before.Links, len(fixture.Links))
	s.Require().Len(before.Namespace, len(fixture.Notes)+len(fixture.Links))

	keyring := filepath.Join(directory, "padmark-keys.json")
	s.Require().NoError(crypto.WriteContentKeyring(keyring, testutil.ContentKeyring()))

	args := slices.Concat([]string{"serve", "--storage", storage, "--dsn", dsn,
		"--content-keyring-file", keyring, "--rate-limit", "0"}, s.s3Args)

	baseURL, stop := servertest.StartWithEnv(s.T(), s.binary, directory, s.s3Env, args...)
	defer stop()

	s.True(s.migrationApplied(db), "serve must apply the files migration")

	var files int

	s.Require().NoError(db.NewRaw("SELECT COUNT(*) FROM files").Scan(s.T().Context(), &files))
	s.Zero(files)
	s.Equal(before, s.snapshot(db), "the files migration must not change notes, links or slug_namespace")

	s.assertNotes(baseURL, fixture.Notes)
	s.assertLinks(baseURL, fixture.Links)
	s.assertFiles(baseURL, fixture, db)
}

func (s *V04UpgradeSuite) migrationApplied(db *bun.DB) bool {
	var count int

	s.Require().NoError(db.NewRaw("SELECT COUNT(*) FROM goose_db_version WHERE version_id = ? AND is_applied",
		versionFiles).Scan(s.T().Context(), &count))

	return count > 0
}

func (s *V04UpgradeSuite) snapshot(db *bun.DB) v04Snapshot {
	rows := func(table string) []map[string]any {
		var result []map[string]any

		s.Require().NoError(db.NewSelect().Table(table).OrderExpr("id").Scan(s.T().Context(), &result))

		return result
	}

	return v04Snapshot{Notes: rows("notes"), Links: rows("links"), Namespace: rows("slug_namespace")}
}

// assertNotes reads each note through the JSON API, then checks burn and edit codes.
func (s *V04UpgradeSuite) assertNotes(baseURL string, notes []v04Record) {
	for _, note := range notes {
		status, body := s.request(http.MethodGet, baseURL+"/notes/"+note.Slug, "", "")
		s.Require().Equal(http.StatusOK, status, note.Slug)

		var result struct {
			Content   string     `json:"content"`
			ExpiresAt *time.Time `json:"expires_at"`
		}

		s.Require().NoError(json.Unmarshal(body, &result))
		s.Equal(note.Value, result.Content, note.Slug)

		if note.Slug == notes[v04NoteTTL].Slug {
			s.Require().NotNil(result.ExpiresAt, "the ttl note keeps its expiry")
			s.True(result.ExpiresAt.After(time.Now().AddDate(9, 0, 0)), "the expiry is about ten years ahead")
		}
	}

	status, _ := s.request(http.MethodGet, baseURL+"/notes/"+notes[v04NoteBurn].Slug, "", "")
	s.Equal(http.StatusNotFound, status, "a burn-after-reading note burns on its first read")

	unicode := notes[v04NoteUnicode]
	update, err := json.Marshal(map[string]string{"content": "edited after the upgrade", "edit_code": unicode.EditCode})
	s.Require().NoError(err)

	status, _ = s.request(http.MethodPut, baseURL+"/notes/"+unicode.Slug, "", string(update))
	s.Equal(http.StatusOK, status, "the v0.4.0 edit code must still work")

	plain := notes[v04NotePlain]
	status, _ = s.request(http.MethodDelete, baseURL+"/notes/"+plain.Slug, plain.EditCode+"x", "")
	s.Equal(http.StatusForbidden, status, "a wrong edit code must not delete the note")

	status, _ = s.request(http.MethodDelete, baseURL+"/notes/"+plain.Slug, plain.EditCode, "")
	s.Equal(http.StatusNoContent, status)
}

// assertLinks reads each link through the JSON API and deletes one with its v0.4.0 edit code.
func (s *V04UpgradeSuite) assertLinks(baseURL string, links []v04Record) {
	for _, link := range links {
		status, body := s.request(http.MethodGet, baseURL+"/links/"+link.Slug, "", "")
		s.Require().Equal(http.StatusOK, status, link.Slug)

		var result struct {
			TargetURL      string `json:"target_url"`
			BurnAfterClick bool   `json:"burn_after_click"`
		}

		s.Require().NoError(json.Unmarshal(body, &result))
		s.Equal(link.Value, result.TargetURL, link.Slug)
		s.Equal(link.Slug == links[v04LinkClick].Slug, result.BurnAfterClick, link.Slug)
	}

	custom := links[v04LinkCustom]
	status, _ := s.request(http.MethodDelete, baseURL+"/links/"+custom.Slug, custom.EditCode, "")
	s.Equal(http.StatusNoContent, status, "the v0.4.0 link edit code must still work")

	random := links[v04LinkRandom]
	status, _ = s.request(http.MethodGet, baseURL+"/links/"+random.Slug, "", "")
	s.Equal(http.StatusOK, status)
}

// request sends a JSON API request and returns the status and the full body. A non-empty
// editCode goes into the X-Edit-Code header.
func (s *V04UpgradeSuite) request(method, target, editCode, body string) (int, []byte) {
	request, err := http.NewRequestWithContext(s.T().Context(), method, target, strings.NewReader(body))
	s.Require().NoError(err)
	request.Header.Set("Accept", "application/json")

	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}

	if editCode != "" {
		request.Header.Set("X-Edit-Code", editCode)
	}

	client := &http.Client{
		Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: nil, Jar: nil,
	}
	defer client.CloseIdleConnections()

	response, err := client.Do(request)
	s.Require().NoError(err)

	defer func() { s.NoError(response.Body.Close()) }()

	raw, err := io.ReadAll(response.Body)
	s.Require().NoError(err)

	return response.StatusCode, raw
}

// assertFiles uploads files to the upgraded server. A free custom slug works. The slug of a
// v0.4.0 note or link gives 409, and that record stays readable.
func (s *V04UpgradeSuite) assertFiles(baseURL string, fixture v04Fixture, db *bun.DB) {
	api, err := client.NewClient(baseURL, client.WithClient(&http.Client{
		Timeout: 30 * time.Second, Transport: nil, CheckRedirect: nil, Jar: nil,
	}))
	s.Require().NoError(err)

	upload := func(slug string) (*client.CreateFileResponse, error) {
		return api.UploadFile(s.T().Context(), "upgrade.bin", bytes.NewReader([]byte("file after the upgrade")),
			client.UploadFileOptions{TTL: 0, Privacy: "", Slug: slug, EditCode: "", BurnAfterDownload: false})
	}

	created, err := upload(v05FileSlug)
	s.Require().NoError(err)
	s.Equal(v05FileSlug, created.ID)

	note, link := fixture.Notes[v04NoteUnicode], fixture.Links[v04LinkRandom]
	for _, slug := range []string{note.Slug, link.Slug} {
		_, err = upload(slug)

		var apiErr *client.APIError

		s.Require().ErrorAs(err, &apiErr, slug)
		s.Equal(http.StatusConflict, apiErr.StatusCode, "a file must not take the slug %s of a v0.4.0 record", slug)
	}

	var files int

	s.Require().NoError(db.NewRaw("SELECT COUNT(*) FROM files").Scan(s.T().Context(), &files))
	s.Equal(1, files, "only the upload with the free slug is stored")

	status, _ := s.request(http.MethodGet, baseURL+"/notes/"+note.Slug, "", "")
	s.Equal(http.StatusOK, status, "the refused upload must not change the note")
	status, _ = s.request(http.MethodGet, baseURL+"/links/"+link.Slug, "", "")
	s.Equal(http.StatusOK, status, "the refused upload must not change the link")
}
