//go:build integration

package integration

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	clicmd "github.com/partyzanex/padmark/internal/adapters/cli"
	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/lock"
	"github.com/partyzanex/padmark/internal/infra/render"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const apiFlowPassword = "FlowP@ss123!"

// TestAPITokenFlow_AdminIssuesKey_CLICreatesNote is the end-to-end admin→CLI→note flow:
// an admin issues an API key, and the CLI — authenticating with nothing but that Bearer key —
// creates a note through the real HTTP stack (auth middleware → ResolveAPIToken → ogen handler →
// storage). It also guards the composition-root wiring: if the api-token store is not injected
// into auth.Manager, CreateAPIToken returns ErrFeatureNotSupported and this test fails.
func TestAPITokenFlow_AdminIssuesKey_CLICreatesNote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ctx := context.Background()

	// ── storage: fresh shared-cache in-memory DB, pragmas (incl. FK enforcement) via Open ──
	db, err := sqliterepo.Open("file:apitokenflow?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = sqliterepo.Migrate(ctx, db)
	require.NoError(t, err)

	// ── wiring (mirrors internal/infra/server assembly) ──
	log := slog.New(slog.DiscardHandler)
	users := sqliterepo.NewUserRepository(db)

	authMgr, err := auth.NewManager(
		users,
		sqliterepo.NewInviteRepository(db, slog.Default()),
		sqliterepo.NewSessionRepository(db, slog.Default()),
		sqliterepo.NewAPITokenRepository(db),
		crypto.NewPasswordHasher(testArgon2Params),
		crypto.NewSealer(testArgon2Params),
		crypto.NewTOTP(),
		log,
		"padmark-test",
		time.Hour,
	)
	require.NoError(t, err)

	slugLock := lock.NewLocker()

	notesMgr := notes.NewManager(
		sqliterepo.NewNoteRepository(db),
		sqliterepo.NewLinkRepository(db, slog.Default()),
		slugLock,
		render.NewRenderer(),
		testutil.ContentEncryptor(t, "note"),
		crypto.NewEditCodeHasher(crypto.DefaultArgon2Params()),
		log,
		false,
	)
	linksMgr := links.NewManager(
		sqliterepo.NewLinkRepository(db, slog.Default()),
		sqliterepo.NewNoteRepository(db),
		slugLock,
		testutil.ContentEncryptor(t, "link"),
		crypto.NewEditCodeHasher(crypto.DefaultArgon2Params()),
		log,
		false,
	)

	handler := adaptershttp.NewHandler(notesMgr, linksMgr, log).WithAuthManager(authMgr)
	ogen := adaptershttp.NewOgenHandler(notesMgr, linksMgr, adaptershttp.NoPinger{}, log)
	router, err := adaptershttp.NewRouter(handler, ogen, &adaptershttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   []byte("padmark-test-csrf-secret-32bytes"),

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	})
	require.NoError(t, err)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	// ── admin issues an API key ──
	_, err = authMgr.AcceptFirstAdmin(ctx, "admin", apiFlowPassword)
	require.NoError(t, err)

	admin, err := users.GetByUsername(ctx, "admin")
	require.NoError(t, err)
	require.True(t, admin.IsAdmin)

	apiKey, err := authMgr.CreateAPIToken(ctx, admin.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err, "admin must be able to issue an API key — the store must be wired into auth.Manager")
	require.NotEmpty(t, apiKey)

	// The admin panel hands the user an envelope token: the raw key packed together with the
	// server URL. Exercising that exact string proves the whole feature end-to-end.
	envelope, err := domain.EncodeAPITokenEnvelope(srv.URL, apiKey)
	require.NoError(t, err)

	require.NoError(t, clicmd.NewApp().Run(ctx, []string{"padmark-cli", "--token", envelope, "trust"}))

	// ── CLI creates a note with only the envelope token — no --url, the URL comes from the token ──
	runErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--token", envelope,
		"create", "--title", "hello", "--content", "world from cli",
	})
	require.NoError(t, runErr, "CLI must create a note using the URL and key packed into the envelope token")

	count, err := db.NewSelect().TableExpr("notes").Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "exactly one note created through the API-token-authenticated path")

	// ── negative: a bogus key is rejected when the operation requires a credential ──
	// POST /notes is anonymous-allowed by design (an invalid key does not block creating a
	// public note), so the negative case targets a mutation on a privacy=authenticated note —
	// the mutation privacy gate answers 401 before the note is touched.
	gatedPrv := domain.PrivacyAuthenticated
	gated, err := notesMgr.Create(ctx, &domain.Note{
		Title:   "gated",
		Content: "requires login",
		Privacy: &gatedPrv,

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, ID: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	})
	require.NoError(t, err)

	badErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--url", srv.URL, "--token", "not-a-real-key",
		"delete", gated.ID,
	})
	require.Error(t, badErr, "CLI must fail when the API key is invalid")

	count, err = db.NewSelect().TableExpr("notes").Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "an invalid API key must not mutate the privacy-gated note")

	// ── regression: a PRIVATE (authenticated-level) note must be readable via the CLI's API
	// token ── GET /notes/{id} is a "public" route (auth not required), so the auth middleware
	// must still resolve the API-token Bearer key there — otherwise a private-note read returns 401.
	priv := domain.PrivacyAuthenticated
	created, err := notesMgr.Create(ctx, &domain.Note{
		Title:   "secret",
		Content: "private body",
		Privacy: &priv,

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, ID: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
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
