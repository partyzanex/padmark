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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/lock"
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
}

func (g *createGate) wait(ctx context.Context) error {
	close(g.ready)

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

type noteRepository interface {
	notes.Storage
	links.NotesChecker
}

//nolint:ireturn // selects the actual backend repositories behind their consumer ports
func repositories(db *bun.DB) (noteRepository, links.Storage) {
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
	localLock := lock.NewLocker()
	hasher := crypto.NewEditCodeHasher(crypto.DefaultArgon2Params())
	noteManager := notes.NewManager(&pausedNotes{Storage: noteRepo, gate: gate}, linkRepo, localLock,
		render.NewRenderer(), testutil.ContentEncryptor(t, "note"), hasher, log, true)
	linkManager := links.NewManager(&pausedLinks{Storage: linkRepo, gate: gate}, noteRepo, localLock,
		testutil.ContentEncryptor(t, "link"), hasher, log, true)
	handler := adhttp.NewHandler(noteManager, linkManager, log)
	ogen := adhttp.NewOgenHandler(noteManager, linkManager, adhttp.NoPinger{}, log)
	router, err := adhttp.NewRouter(handler, ogen, &adhttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, CSRFSecret: nil,
		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, SessionTTL: 0, DisableAPI: false,
		DirectRedirect: false,
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

	// Separate pools, locks, handlers and listeners model independent replicas. The gate is
	// after both managers' preflight checks and before their actual repository INSERT.
	noteGate := &createGate{ready: make(chan struct{}), proceed: make(chan struct{})}
	linkGate := &createGate{ready: make(chan struct{}), proceed: make(chan struct{})}
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
			t.Fatal("both creates must reach INSERT after observing the slug as free")
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
