//go:build integration

package slugs_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	"github.com/partyzanex/padmark/internal/infra/render"
	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

type createGate struct {
	ready   chan struct{}
	proceed chan struct{}
	// once closes ready on the first wait. A manager can call Create again on the same gate: a
	// create with a custom slug tries once more after it removes an expired record.
	once sync.Once
}

func newGate() *createGate {
	return &createGate{ready: make(chan struct{}), proceed: make(chan struct{}), once: sync.Once{}}
}

// openGate returns a gate that does not pause the create.
func openGate() *createGate {
	gate := newGate()
	close(gate.proceed)

	return gate
}

func (g *createGate) wait(ctx context.Context) error {
	g.once.Do(func() { close(g.ready) })

	select {
	case <-g.proceed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type pausedNotes struct {
	notes.Storage

	gate *createGate
}

func (r *pausedNotes) Create(ctx context.Context, note *domain.Note) error {
	err := r.gate.wait(ctx)
	if err != nil {
		return err
	}

	return r.Storage.Create(ctx, note)
}

type pausedLinks struct {
	links.Storage

	gate *createGate
}

func (r *pausedLinks) Create(ctx context.Context, link *domain.Link) error {
	err := r.gate.wait(ctx)
	if err != nil {
		return err
	}

	return r.Storage.Create(ctx, link)
}

//nolint:ireturn // selects the actual backend repositories behind their consumer ports
func repositories(db *bun.DB) (notes.Storage, links.Storage) {
	log := slog.New(slog.DiscardHandler)
	if db.Dialect().Name() == dialect.PG {
		return pgrepo.NewNoteRepository(db), pgrepo.NewLinkRepository(db, log)
	}

	return sqliterepo.NewNoteRepository(db), sqliterepo.NewLinkRepository(db, log)
}

func replica(t *testing.T, db *bun.DB, gate *createGate) *httptest.Server {
	t.Helper()

	noteRepo, linkRepo := repositories(db)
	log := slog.New(slog.DiscardHandler)
	expired := expiredslugs.New(noteRepo, linkRepo)
	hasher := crypto.NewEditCodeHasher(testutil.Argon2Params(), slog.New(slog.DiscardHandler))
	noteManager := notes.NewManager(&notes.Deps{
		Storage: &pausedNotes{Storage: noteRepo, gate: gate}, Renderer: render.NewRenderer(),
		Encryptor: testutil.ContentEncryptor(t, "note"), Hasher: hasher, ExpiredSlugs: expired, Log: log,
		Now: time.Now,
	}, true)
	linkManager := links.NewManager(&links.Deps{
		Storage: &pausedLinks{Storage: linkRepo, gate: gate}, Encryptor: testutil.ContentEncryptor(t, "link"),
		Hasher: hasher, ExpiredSlugs: expired, Log: log,
		Now: time.Now,
	}, true)
	handlerCfg := testHandlerConfig(noteManager, linkManager)
	handlerCfg.Log = log
	handler := newTestHandler(t, handlerCfg)
	router, err := web.NewRouter(handler, &web.RouterOptions{
		MaxBodyBytes: 256 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	})
	require.NoError(t, err)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return server
}

type createResult struct {
	err    error
	status int
}

func postAsync(ctx context.Context, t *testing.T, url, body string) <-chan createResult {
	t.Helper()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	result := make(chan createResult, 1)

	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			result <- createResult{err: requestErr, status: 0}

			return
		}

		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()

		result <- createResult{err: nil, status: response.StatusCode}
	}()

	return result
}

func testConcurrentCreate(t *testing.T, db *bun.DB, open func(*testing.T) *bun.DB, winner string) {
	t.Helper()

	// Separate pools, handlers and listeners model independent replicas. The gate is before each
	// repository INSERT; the managers make no slug check before it.
	noteGate, linkGate := newGate(), newGate()
	noteServer := replica(t, open(t), noteGate)
	linkServer := replica(t, open(t), linkGate)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	slug := uuid.NewString()
	noteResult := postAsync(ctx, t, noteServer.URL+"/notes", fmt.Sprintf(`{"slug":%q,"content":"note"}`, slug))
	linkResult := postAsync(ctx, t, linkServer.URL+"/links",
		fmt.Sprintf(`{"slug":%q,"target_url":"https://example.com/target"}`, slug))

	for _, gate := range []*createGate{noteGate, linkGate} {
		select {
		case <-gate.ready:
		case <-ctx.Done():
			t.Fatal("both creates must reach INSERT")
		}
	}

	first, second := noteResult, linkResult
	firstGate, secondGate := noteGate, linkGate

	if winner == "links" {
		first, second = linkResult, noteResult
		firstGate, secondGate = linkGate, noteGate
	}

	close(firstGate.proceed)

	if winner == "simultaneous" {
		close(secondGate.proceed)

		one, two := <-first, <-second
		require.NoError(t, one.err)
		require.NoError(t, two.err)
		require.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, []int{one.status, two.status})
	} else {
		created := <-first
		require.NoError(t, created.err)
		require.Equal(t, http.StatusCreated, created.status)
		close(secondGate.proceed)

		rejected := <-second
		require.NoError(t, rejected.err)
		require.Equal(t, http.StatusConflict, rejected.status)
	}

	var count int

	require.NoError(t, db.NewRaw(`SELECT COUNT(*) FROM (
		SELECT id FROM notes WHERE id = ? UNION ALL SELECT id FROM links WHERE id = ?
	) AS occupied`, domain.HashSlug(slug), domain.HashSlug(slug)).Scan(t.Context(), &count))
	require.Equal(t, 1, count)
	require.NoError(t, db.NewRaw("SELECT COUNT(*) FROM notes JOIN links ON notes.id = links.id").Scan(t.Context(), &count))
	require.Zero(t, count)
}

// testHandlerConfig returns the web.HandlerConfig of the test stacks: the given managers, no
// database pinger, a discarding logger, no account system, no files, no reveal store and the
// default settings. A test sets the fields of its scenario on the returned value.
func testHandlerConfig(notes web.NoteManager, links web.LinkManager) *web.HandlerConfig {
	return &web.HandlerConfig{
		Notes: notes, Links: links, Pinger: web.NoPinger{}, Log: slog.New(slog.DiscardHandler),
		Accounts: nil, Files: nil, Reveal: nil,
		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, CSRFSecret: nil, SessionTTL: 0,
		FileTransfer:   web.FileTransferLimits{IdleTimeout: 0, MaxUploads: 0, MaxUploadsPerClient: 0},
		DirectRedirect: false,
	}
}

// newTestHandler builds the web.Handler of cfg and stops the test when web.NewHandler fails.
func newTestHandler(t *testing.T, cfg *web.HandlerConfig) *web.Handler {
	t.Helper()

	handler, err := web.NewHandler(cfg)
	require.NoError(t, err)

	return handler
}
