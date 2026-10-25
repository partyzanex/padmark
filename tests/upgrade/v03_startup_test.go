//go:build integration

package upgrade_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/domain"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/tests/internal/servertest"
)

type v03Fixture struct {
	Auth    v03Auth
	Version string
	Notes   []v03Note
}

type v03Auth struct {
	Username   string
	Password   string
	TOTPSecret string
	APIToken   string
	Session    string
}

type v03Note struct {
	Name     string
	ID       string
	EditCode string
	Content  string
}

func TestSQLiteAutomaticUpgradeFromV03(t *testing.T) {
	binary := servertest.Build(t)
	for _, version := range []string{"v0.3.2", "v0.3.3"} {
		t.Run(version, func(t *testing.T) {
			directory := t.TempDir()
			dsn := filepath.Join(directory, "padmark.db")
			db, err := sqliterepo.Open(dsn, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			fixture := loadV03(t, db, version, "sqlite")
			runV03Startup(t, binary, directory, "sqlite", dsn, db, fixture)
		})
	}
}

func TestPostgresAutomaticUpgradeFromV03(t *testing.T) {
	binary := servertest.Build(t)
	container, err := tcpostgres.Run(t.Context(), "postgres:17-alpine",
		tcpostgres.WithDatabase("padmark"), tcpostgres.WithUsername("test"), tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })

	for _, version := range []string{"v0.3.2", "v0.3.3"} {
		t.Run(version, func(t *testing.T) {
			database := strings.ReplaceAll(version, ".", "_")
			code, _, execErr := container.Exec(t.Context(), []string{"createdb", "-U", "test", database})
			require.NoError(t, execErr)
			require.Zero(t, code)
			dsn, connErr := container.ConnectionString(t.Context(), "sslmode=disable")
			require.NoError(t, connErr)

			dsn = strings.Replace(dsn, "/padmark?", "/"+database+"?", 1)
			loader := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())
			fixture := loadV03(t, loader, version, "postgres")
			require.NoError(t, loader.Close(), "pg_dump changes its session search_path")

			db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())

			t.Cleanup(func() { require.NoError(t, db.Close()) })
			runV03Startup(t, binary, t.TempDir(), "postgres", dsn, db, fixture)
		})
	}
}

func loadV03(t *testing.T, db *bun.DB, version, storage string) v03Fixture {
	t.Helper()

	//nolint:gosec // repository-owned fixture paths
	dump, err := os.ReadFile(filepath.Join("..", "fixtures", version, storage+".sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), string(dump))
	require.NoError(t, err)

	fixture, found := v03Fixtures()[version+"/"+storage]
	require.True(t, found)
	require.Equal(t, version, fixture.Version)

	return fixture
}

func v03Contents(t *testing.T, db *bun.DB) map[string]string {
	t.Helper()

	var rows []struct{ ID, Content string }

	require.NoError(t, db.NewSelect().Table("notes").Column("id", "content").OrderExpr("id").Scan(t.Context(), &rows))

	values := make(map[string]string, len(rows))
	for _, row := range rows {
		values[row.ID] = row.Content
	}

	return values
}

func assertV03Ready(t *testing.T, db *bun.DB, notes int) {
	t.Helper()

	var phase string

	require.NoError(t, db.NewRaw("SELECT phase FROM content_encryption_state WHERE id = 1").Scan(t.Context(), &phase))
	require.Equal(t, "ready", phase)
	contents := v03Contents(t, db)
	require.Len(t, contents, notes)

	for _, content := range contents {
		require.True(t, strings.HasPrefix(content, "pmc2$l$"), "v0.3 ciphertext is wrapped at startup")
	}
}

func runV03Startup(t *testing.T, binary, directory, storage, dsn string, db *bun.DB, fixture v03Fixture) {
	t.Helper()

	before := v03Contents(t, db)
	metadataBefore := make(map[string]map[string]any)

	for _, note := range fixture.Notes {
		var row map[string]any

		require.NoError(t, db.NewSelect().Table("notes").Where("id = ?", domain.HashSlug(note.ID)).Scan(t.Context(), &row))
		delete(row, "content")
		delete(row, "private")
		metadataBefore[note.ID] = row
	}

	args := []string{"serve", "--storage", storage, "--dsn", dsn, "--enable-accounts", "--rate-limit", "0"}
	_, stop := servertest.Start(t, binary, directory, args...)
	stop()
	assertV03Ready(t, db, len(fixture.Notes))

	keyfile := filepath.Join(directory, "padmark-keys.json")
	keyBefore, err := os.ReadFile(keyfile) //nolint:gosec // test-created keyring in t.TempDir
	require.NoError(t, err)
	wrapped := v03Contents(t, db)
	require.NotEqual(t, before, wrapped)

	for _, note := range fixture.Notes {
		var row map[string]any

		require.NoError(t, db.NewSelect().Table("notes").Where("id = ?", domain.HashSlug(note.ID)).Scan(t.Context(), &row))

		for column, value := range metadataBefore[note.ID] {
			require.Equal(t, value, row[column], "migration changed %s of %s", column, note.Name)
		}
	}

	baseURL, stop := servertest.Start(t, binary, directory, args...)
	require.Equal(t, wrapped, v03Contents(t, db), "restart must skip re-encryption")

	keyAfter, err := os.ReadFile(keyfile) //nolint:gosec // same test-created keyring
	require.NoError(t, err)
	require.Equal(t, keyBefore, keyAfter, "container restart reuses its persistent key")
	assertV03HTTP(t, baseURL, fixture)
	stop()
}

func v03Request(t *testing.T, method, url, token, cookie, contentType, body string) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Accept", "application/json")

	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: "padmark_session", Value: cookie}) //nolint:gosec // outgoing test cookie
	}

	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })

	return response
}

func assertV03HTTP(t *testing.T, baseURL string, fixture v03Fixture) {
	t.Helper()

	for _, note := range fixture.Notes {
		response := v03Request(t, http.MethodGet, baseURL+"/notes/"+note.ID, fixture.Auth.APIToken, "", "", "")
		require.Equal(t, http.StatusOK, response.StatusCode, note.Name)

		var result struct {
			Content string `json:"content"`
		}

		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		require.Equal(t, note.Content, result.Content)

		if note.Name == "weak_code" {
			body, err := json.Marshal(map[string]string{
				"content": "updated with original edit code", "edit_code": note.EditCode,
			})
			require.NoError(t, err)
			updated := v03Request(t, http.MethodPut, baseURL+"/notes/"+note.ID, "", "", "application/json", string(body))
			require.Equal(t, http.StatusOK, updated.StatusCode)
		}
	}

	sessionRead := v03Request(t, http.MethodGet, baseURL+"/notes/compat-private", "", fixture.Auth.Session, "", "")
	require.Equal(t, http.StatusUnauthorized, sessionRead.StatusCode,
		"the session-hashing migration requires a fresh login")

	code, err := totp.GenerateCode(fixture.Auth.TOTPSecret, time.Now())
	require.NoError(t, err)

	form := url.Values{"username": {fixture.Auth.Username}, "password": {fixture.Auth.Password}, "code": {code}}
	login := v03Login(t, baseURL, form)
	require.Equal(t, http.StatusSeeOther, login.StatusCode)
	require.NotContains(t, login.Header.Get("Location"), "error=1", "the old password and TOTP must still authenticate")
	require.NotEmpty(t, login.Cookies())
	_, err = io.Copy(io.Discard, login.Body)
	require.NoError(t, err)

	created := v03Request(t, http.MethodPost, baseURL+"/links", fixture.Auth.APIToken, "", "application/json",
		`{"target_url":"https://example.com/after-upgrade"}`)
	require.Equal(t, http.StatusCreated, created.StatusCode)
}

func v03Login(t *testing.T, baseURL string, form url.Values) *http.Response {
	t.Helper()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	client := &http.Client{Jar: jar, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/login", nil)
	require.NoError(t, err)
	page, err := client.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(page.Body)
	require.NoError(t, err)
	require.NoError(t, page.Body.Close())

	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindSubmatch(body)
	require.Len(t, match, 2)
	form.Set("csrf_token", string(match[1]))

	request, err = http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/totp-login",
		strings.NewReader(form.Encode()))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", baseURL)
	response, err := client.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })

	return response
}
