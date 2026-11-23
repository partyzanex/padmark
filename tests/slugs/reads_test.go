//go:build integration

package slugs_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// queryCounter counts the SQL statements of one database handle. A SELECT is a read; each other
// statement (INSERT, UPDATE, DELETE, also with RETURNING) is a write.
type queryCounter struct {
	mu     sync.Mutex
	reads  int
	writes int
}

func (*queryCounter) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (c *queryCounter) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if event.Operation() == "SELECT" {
		c.reads++
	} else {
		c.writes++
	}
}

// take returns the counts since the last call and sets them to zero.
func (c *queryCounter) take() (reads, writes int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	reads, writes = c.reads, c.writes
	c.reads, c.writes = 0, 0

	return reads, writes
}

// pageReads is one GET and the SQL statements that it may run.
type pageReads struct {
	name   string
	path   string
	accept string
	status int
	reads  int
	writes int
}

// readServer serves the router of env with the reveal store of production. Each response sends
// one value to served after the handler returns, so the counts of a request are complete.
func readServer(t *testing.T, env *slugEnv, served chan<- struct{}) *httptest.Server {
	t.Helper()

	log := slog.New(slog.DiscardHandler)

	var reveals auth.RevealTokenStore = sqliterepo.NewRevealRepository(env.db)
	if env.db.Dialect().Name() == dialect.PG {
		reveals = pgrepo.NewRevealRepository(env.db)
	}

	handlerCfg := testHandlerConfig(env.notes, env.links)
	handlerCfg.Log = log
	handlerCfg.Reveal = auth.NewRevealManager(reveals, log, time.Now)
	handlerCfg.Files = env.files
	handlerCfg.CSRFSecret = []byte("record-reads-csrf-secret-32bytes")
	handler := newTestHandler(t, handlerCfg)
	router, err := web.NewRouter(handler, &web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	})
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r)

		served <- struct{}{}
	}))
	t.Cleanup(server.Close)

	return server
}

// readSlugs are the slugs of the records of testRecordReads.
type readSlugs struct {
	note, dirNote, link, file string
	expiredNote, expiredLink  string
	burnJSON, burnHTML        string
}

// createReadRecords creates the records of testRecordReads through the plain handle db, so that
// only the requests go through the counter.
func createReadRecords(t *testing.T, db *bun.DB, objects *s3store.Store) readSlugs {
	t.Helper()

	slug := func(name string) string { return name + "-" + uuid.NewString()[:8] }
	names := readSlugs{
		note: slug("note"), dirNote: "dir/" + slug("note"), link: slug("link"), file: slug("file"),
		expiredNote: slug("expired-note"), expiredLink: slug("expired-link"),
		burnJSON: slug("burn-json"), burnHTML: slug("burn-html"),
	}

	fixtures := newSlugEnv(t, db, objects, openGate())
	records := []struct {
		resource, slug string
		burn           bool
	}{
		{"notes", names.note, false}, {"notes", names.dirNote, false}, {"notes", names.expiredNote, false},
		{"notes", names.burnJSON, true}, {"notes", names.burnHTML, true},
		{"links", names.link, false}, {"links", names.expiredLink, false}, {"files", names.file, false},
	}

	for _, record := range records {
		_, err := fixtures.create(t.Context(), record.resource, record.slug, record.burn)
		require.NoError(t, err, record.slug)
	}

	expireRecord(t, db, "notes", domain.HashSlug(names.expiredNote))
	expireRecord(t, db, "links", domain.HashSlug(names.expiredLink))

	return names
}

// recordPages lists the pages of testRecordReads and the SQL statements that each page may run.
func recordPages(names readSlugs) []pageReads {
	const (
		html  = "text/html"
		jsonT = "application/json"
	)

	page := func(name, path, accept string, status, reads, writes int) pageReads {
		return pageReads{name: name, path: path, accept: accept, status: status, reads: reads, writes: writes}
	}

	return []pageReads{
		page("note_html", "/"+names.note, html, http.StatusOK, 1, 1),
		page("note_json", "/"+names.note, jsonT, http.StatusOK, 1, 1),
		page("note_markdown", "/"+names.note, "text/markdown", http.StatusOK, 1, 1),
		page("note_plain", "/"+names.note, "text/plain", http.StatusOK, 1, 1),
		page("notes_path_html", "/notes/"+names.note, html, http.StatusOK, 1, 1),
		page("notes_path_json", "/notes/"+names.note, jsonT, http.StatusOK, 1, 1),
		page("note_multi_segment", "/"+names.dirNote, html, http.StatusOK, 1, 1),
		page("note_editor", "/edit/"+names.note, html, http.StatusOK, 1, 0),
		page("note_expired", "/"+names.expiredNote, html, http.StatusGone, 1, 1),
		page("note_burn_json", "/"+names.burnJSON, jsonT, http.StatusOK, 1, 1),
		// The confirmation page of a burn note reads the note without the burn intent, then reads
		// it once more for the page (NoteHandler.handleBurnInterstitial); the token is a write.
		page("note_burn_confirm", "/"+names.burnHTML, html, http.StatusOK, 2, 2),
		page("link_redirect", "/"+names.link, html, http.StatusOK, 2, 1),
		page("link_json", "/"+names.link, jsonT, http.StatusOK, 2, 1),
		page("link_editor", "/edit/"+names.link, html, http.StatusOK, 2, 0),
		page("link_expired", "/"+names.expiredLink, html, http.StatusGone, 2, 1),
		page("file_page", "/"+names.file, html, http.StatusOK, 2, 0),
		page("file_content", "/"+names.file, "*/*", http.StatusOK, 2, 1),
		page("file_editor", "/edit/"+names.file, html, http.StatusOK, 2, 0),
		page("missing_html", "/missing-"+uuid.NewString(), html, http.StatusNotFound, 1, 0),
		page("missing_json", "/missing-"+uuid.NewString(), jsonT, http.StatusNotFound, 1, 0),
		page("missing_editor", "/edit/missing-"+uuid.NewString(), html, http.StatusNotFound, 1, 0),
		page("link_on_notes_path", "/notes/"+names.link, jsonT, http.StatusNotFound, 1, 0),
	}
}

// testRecordReads counts the SQL statements of each record page on the GET /{id} and
// GET /edit/{id} routes. A note view and the note editor read the record once. A short link and a
// file read once more for the record of their kind; a slug without a record reads once.
func testRecordReads(t *testing.T, db *bun.DB, objects *s3store.Store) {
	t.Helper()

	names := createReadRecords(t, db, objects)
	counter := new(queryCounter)
	served := make(chan struct{}, 1)
	server := readServer(t, newSlugEnv(t, db.WithQueryHook(counter), objects, openGate()), served)

	for _, page := range recordPages(names) {
		t.Run(page.name, func(t *testing.T) {
			counter.take()

			status := getStatus(t, server.URL+page.path, page.accept)

			<-served

			reads, writes := counter.take()
			t.Logf("%s: status %d, %d reads, %d writes", page.name, status, reads, writes)
			require.Equal(t, page.status, status)
			assert.Equal(t, page.reads, reads, "reads")
			assert.Equal(t, page.writes, writes, "writes")
		})
	}
}

// getStatus sends a GET with accept and returns the status. It does not follow redirects and it
// reads the full body.
func getStatus(t *testing.T, url, accept string) int {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	require.NoError(t, err)
	request.Header.Set("Accept", accept)

	if strings.Contains(accept, "html") {
		request.Header.Set("Sec-Fetch-Dest", "document")
	}

	client := &http.Client{
		Transport:     nil,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Jar:           nil,
		Timeout:       0,
	}

	response, err := client.Do(request)
	require.NoError(t, err)

	defer func() { require.NoError(t, response.Body.Close()) }()

	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)

	return response.StatusCode
}
