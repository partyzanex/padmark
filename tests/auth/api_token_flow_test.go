//go:build integration

package integration

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	clicmd "github.com/partyzanex/padmark/internal/adapters/cli"
	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/render"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const apiFlowPassword = "FlowP@ss123!"

// TestAPITokenFlow_AdminIssuesKey_CLICreatesNote is the end-to-end admin→CLI→note flow:
// an admin issues an API key, and the CLI — authenticating with nothing but that Bearer key —
// creates a note through the real HTTP stack (auth middleware → ResolveAPIToken → ogen handler →
// storage). It also guards the composition-root wiring: if the api-token store is not injected
// into auth.Manager, CreateAPIToken returns ErrFeatureNotSupported and this test fails.
func TestAPITokenFlow_AdminIssuesKey_CLICreatesNote(t *testing.T) {
	ctx := context.Background()

	// ── storage: fresh shared-cache in-memory DB with FK enforcement ──
	sqldb, err := sql.Open(sqliteshim.DriverName(),
		"file:apitokenflow?mode=memory&cache=shared&_busy_timeout=5000&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqldb.Close()) })

	db := bun.NewDB(sqldb, sqlitedialect.New())
	_, err = sqliterepo.Migrate(ctx, db)
	require.NoError(t, err)

	// ── wiring (mirrors internal/infra/server assembly) ──
	log := slog.New(slog.DiscardHandler)
	users := sqliterepo.NewUserRepository(db)

	authMgr := auth.NewManager(
		users,
		sqliterepo.NewInviteRepository(db),
		sqliterepo.NewSessionRepository(db),
		sqliterepo.NewAPITokenRepository(db),
		crypto.New(),
		crypto.NewPasswordHasher(testArgon2Params),
		crypto.NewKDF(),
		crypto.NewTOTP(),
		log,
		"padmark-test",
		time.Hour,
	)

	notesMgr := notes.NewManager(
		sqliterepo.NewNoteRepository(db),
		render.NewRenderer(),
		crypto.New(),
		crypto.NewEditCodeHasher(),
		log,
		false,
	)

	handler := adaptershttp.NewHandler(notesMgr, log, nil).WithAuthManager(authMgr)
	ogen := adaptershttp.NewOgenHandler(notesMgr, adaptershttp.NoPinger{}, log)
	router := adaptershttp.NewRouter(handler, ogen, &adaptershttp.RouterOptions{
		CookieMaxAge: 3600,
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   []byte("padmark-test-csrf-secret-32bytes"),
	})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	// ── admin issues an API key ──
	_, err = authMgr.AcceptFirstAdmin(ctx, "admin", apiFlowPassword)
	require.NoError(t, err)

	admin, err := users.GetByUsername(ctx, "admin")
	require.NoError(t, err)
	require.True(t, admin.IsAdmin)

	apiKey, err := authMgr.CreateAPIToken(ctx, admin.ID)
	require.NoError(t, err, "admin must be able to issue an API key — the store must be wired into auth.Manager")
	require.NotEmpty(t, apiKey)

	// The admin panel hands the user an envelope token: the raw key packed together with the
	// server URL. Exercising that exact string proves the whole feature end-to-end.
	envelope, err := domain.EncodeAPITokenEnvelope(srv.URL, apiKey)
	require.NoError(t, err)

	// ── CLI creates a note with only the envelope token — no --url, the URL comes from the token ──
	runErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--token", envelope,
		"create", "--title", "hello", "--content", "world from cli",
	})
	require.NoError(t, runErr, "CLI must create a note using the URL and key packed into the envelope token")

	count, err := db.NewSelect().TableExpr("notes").Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count, "exactly one note created through the API-token-authenticated path")

	// ── negative: a bogus key is rejected and creates nothing ──
	badErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--url", srv.URL, "--token", "not-a-real-key",
		"create", "--title", "nope", "--content", "should be rejected",
	})
	require.Error(t, badErr, "CLI must fail when the API key is invalid")

	count, err = db.NewSelect().TableExpr("notes").Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count, "an invalid API key must not create a note")

	// ── regression: a PRIVATE note must be readable via the CLI's API token ──
	// GET /notes/{id} is a "public" route (auth not required), so the auth middleware must still
	// resolve the API-token Bearer key there — otherwise a private-note read returns 401.
	priv := true
	created, err := notesMgr.Create(ctx, &domain.Note{
		Title:   "secret",
		Content: "private body",
		Private: &priv,
	})
	require.NoError(t, err)

	getErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--token", envelope, "get", created.ID,
	})
	require.NoError(t, getErr,
		"CLI must read a PRIVATE note with its API token (regression: 401 on the public GET route)")

	// ── regression: a missing note must surface as a clean typed "not found" error, not a
	// content-type decode failure — the API error body must be JSON per the OpenAPI spec ──
	missingErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--token", envelope, "get", "no-such-note",
	})
	require.Error(t, missingErr)
	require.Contains(t, missingErr.Error(), "not found",
		"the ogen client must decode the JSON 404 into a typed not-found error")
	require.NotContains(t, missingErr.Error(), "Content-Type",
		"a missing note must not surface as an undecodable-response error")
}
