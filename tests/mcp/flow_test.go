//go:build integration

// Package mcpintegration drives the padmark-mcp agent surface end-to-end against the real
// server stack: SQLite + migrations + proven router behind httptest, MCP tools through the
// generated ogen client, a real MCP client session on top.
package mcpintegration

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	mcpx "github.com/partyzanex/padmark/internal/adapters/mcp"
	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/lock"
	"github.com/partyzanex/padmark/internal/infra/render"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

const flowPassword = "McpFlowP@ss123!"

var flowArgon2 = crypto.Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1} //nolint:gochecknoglobals // test fixture

// testStack bundles the pieces the tests reach into directly.
type testStack struct {
	users *sqliterepo.UserRepository
	notes *notes.Manager
	links *links.Manager
	auth  *auth.Manager
	srv   *httptest.Server
}

// TestMCPFlow_AuthenticatedOwnerLifecycle drives the full owner lifecycle through the agent
// surface over the bearer-token path: create (the one-time edit code held in the proxy, not
// printed) → update via the vault contract → get → short links → bulk → delete → gone.
func TestMCPFlow_AuthenticatedOwnerLifecycle(t *testing.T) {
	ctx := context.Background()

	stack := startTestPadmark(t)
	envelope := issueEnvelopeToken(ctx, t, stack.auth, stack.users, stack.srv.URL)

	sess := connectMCP(ctx, t, stack.srv.URL, envelope)
	defer sess.Close()

	// ── check_server through the bearer path ──
	status := callOutput(ctx, t, sess, "check_server", map[string]any{})
	require.False(t, status.IsError, toolText(status))
	require.True(t, boolField(status, "live"), "healthz must pass")
	require.True(t, boolField(status, "ready"), "readyz must pass")

	// ── create_note: owner-scoped, the one-time edit code must not be printed ──
	created := callOutput(ctx, t, sess, "create_note", map[string]any{
		"content": "secret runbook", "privacy": "owner",
	})
	require.False(t, created.IsError, toolText(created))
	require.NotContains(t, toolText(created), "edit_code:", "the one-time edit code is not printed by default")

	noteID := resultID(created)
	require.NotEmpty(t, noteID)

	// ── update_note: no edit_code passed — the proxy's vault supplies it ──
	updated := callOutput(ctx, t, sess, "update_note", map[string]any{
		"id": noteID, "content": "secret runbook v2",
	})
	require.False(t, updated.IsError, "update_note must succeed via the vault edit code: %s", toolText(updated))
	require.Contains(t, toolText(updated), "secret runbook v2")

	// ── get_note with the owner token reads the owner-only note ──
	got := callOutput(ctx, t, sess, "get_note", map[string]any{"id": noteID})
	require.False(t, got.IsError, toolText(got))
	require.Contains(t, toolText(got), "secret runbook v2")

	// ── short links: create → metadata (read-only) → delete via the vault ──
	link := callOutput(ctx, t, sess, "create_link", map[string]any{
		"target_url": "https://example.com/long/path",
	})
	require.False(t, link.IsError, toolText(link))

	linkMeta := callOutput(ctx, t, sess, "get_link", map[string]any{"id": resultID(link)})
	require.False(t, linkMeta.IsError, toolText(linkMeta))
	require.Contains(t, toolText(linkMeta), "https://example.com/long/path")

	linkDeleted := callOutput(ctx, t, sess, "delete_link", map[string]any{"id": resultID(link)})
	require.False(t, linkDeleted.IsError, "delete_link must succeed via the vault edit code: %s", toolText(linkDeleted))

	// ── bulk: two valid targets, results in request order (best-effort contract) ──
	bulk := callOutput(ctx, t, sess, "create_links_bulk", map[string]any{
		"target_urls": []string{"https://example.com/one", "https://example.com/two"},
	})
	require.False(t, bulk.IsError, toolText(bulk))

	// ── delete_note: same vault contract; storage-side: the note is really gone ──
	deleted := callOutput(ctx, t, sess, "delete_note", map[string]any{"id": noteID})
	require.False(t, deleted.IsError, toolText(deleted))

	after := callOutput(ctx, t, sess, "get_note", map[string]any{"id": noteID})
	require.True(t, after.IsError, "the note is deleted")
	require.Contains(t, toolText(after), "not found")
}

// TestMCPFlow_Anonymous_PublicCreateAndPrivateAccessDenied exercises the unauthenticated path:
// public create/read work; a privacy=owner note answers the remediation error text.
func TestMCPFlow_Anonymous_PublicCreateAndPrivateAccessDenied(t *testing.T) {
	ctx := context.Background()

	stack := startTestPadmark(t)

	sess := connectMCP(ctx, t, stack.srv.URL, "") // no token at all
	defer sess.Close()

	created := callOutput(ctx, t, sess, "create_note", map[string]any{
		"content": "public note body",
	})
	require.False(t, created.IsError, toolText(created))

	pub := callOutput(ctx, t, sess, "get_note", map[string]any{"id": resultID(created)})
	require.False(t, pub.IsError, toolText(pub))

	gated := domain.PrivacyAuthenticated
	gatedNote, err := stack.notes.Create(ctx, &domain.Note{
		Title: "gated", Content: "owner-only", Privacy: &gated,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, ID: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false,
	})
	require.NoError(t, err)

	denied := callOutput(ctx, t, sess, "get_note", map[string]any{"id": gatedNote.ID})
	require.True(t, denied.IsError, "a privacy=owner note is refused to an anonymous caller")
	require.Contains(t, toolText(denied), "unauthorized",
		"the failure text carries the remediation (configure a token or use edit_code)")
}

// ── wiring (mirrors internal/infra/server's assembly and the tests/auth integration setup) ──

func startTestPadmark(t *testing.T) *testStack {
	t.Helper()

	ctx := context.Background()

	db, err := sqliterepo.Open("file:mcpflow?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = sqliterepo.Migrate(ctx, db)
	require.NoError(t, err)

	users := sqliterepo.NewUserRepository(db)

	authMgr := newTestAuthManager(t, db, users)

	slugLock := lock.NewLocker()

	notesMgr, linksMgr := newTestManagers(t, db, slugLock)

	handler := web.NewHandler(notesMgr, linksMgr, discardTestLogger()).WithAuthManager(authMgr)
	ogen := web.NewOgenHandler(notesMgr, linksMgr, web.NoPinger{}, discardTestLogger())
	router, err := web.NewRouter(handler, ogen, &web.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   []byte("padmark-test-csrf-secret-32bytes"),

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	})
	require.NoError(t, err)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return &testStack{users: users, notes: notesMgr, links: linksMgr, auth: authMgr, srv: srv}
}

func discardTestLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newTestAuthManager(t *testing.T, db *bun.DB, users *sqliterepo.UserRepository) *auth.Manager {
	t.Helper()

	authMgr, err := auth.NewManager(
		auth.Stores{
			Users:     users,
			Invites:   sqliterepo.NewInviteRepository(db, discardTestLogger()),
			Sessions:  sqliterepo.NewSessionRepository(db, discardTestLogger()),
			APITokens: sqliterepo.NewAPITokenRepository(db),
		},
		crypto.NewPasswordHasher(flowArgon2),
		crypto.NewSealer(flowArgon2),
		crypto.NewTOTP(),
		discardTestLogger(),
		"padmark-test",
		time.Hour,
	)
	require.NoError(t, err)

	return authMgr
}

func newTestManagers(t *testing.T, db *bun.DB, slugLock *lock.Locker) (*notes.Manager, *links.Manager) {
	t.Helper()

	notesMgr := notes.NewManager(
		sqliterepo.NewNoteRepository(db),
		sqliterepo.NewLinkRepository(db, discardTestLogger()),
		slugLock,
		render.NewRenderer(),
		testutil.ContentEncryptor(t, "note"),
		crypto.NewEditCodeHasher(crypto.DefaultArgon2Params()),
		discardTestLogger(),
		false,
	)
	linksMgr := links.NewManager(
		sqliterepo.NewLinkRepository(db, discardTestLogger()),
		sqliterepo.NewNoteRepository(db),
		slugLock,
		testutil.ContentEncryptor(t, "link"),
		crypto.NewEditCodeHasher(crypto.DefaultArgon2Params()),
		discardTestLogger(),
		false,
	)

	return notesMgr, linksMgr
}

// issueEnvelopeToken creates the first admin + an API key, returning the envelope the admin
// panel hands the user (the raw key packed together with the server's base URL).
func issueEnvelopeToken(
	ctx context.Context, t *testing.T, authMgr *auth.Manager,
	users *sqliterepo.UserRepository, baseURL string,
) string {
	t.Helper()

	_, err := authMgr.AcceptFirstAdmin(ctx, "admin", flowPassword)
	require.NoError(t, err)

	admin, err := users.GetByUsername(ctx, "admin")
	require.NoError(t, err)

	apiKey, err := authMgr.CreateAPIToken(ctx, admin.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	envelope, err := domain.EncodeAPITokenEnvelope(baseURL, apiKey)
	require.NoError(t, err)

	return envelope
}

// connectMCP wires the padmark-mcp Manager to the test server through the real generated
// client (a bearer token when passed) and attaches an in-memory MCP client session.
func connectMCP(ctx context.Context, t *testing.T, serverURL, token string) *mcpsdk.ClientSession {
	t.Helper()

	padmarkClient := newTestPadmarkClient(t, serverURL, token)
	mgr := mcpx.NewManager(padmarkClient, slog.New(slog.DiscardHandler))

	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()

	srvSession, err := mgr.ConnectSession(ctx, serverTransport)
	require.NoError(t, err)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name: "integration", Version: "0",
		Title: "", Description: "", WebsiteURL: "", Icons: nil,
	}, nil)

	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = session.Close()
		_ = srvSession.Close()
	})

	return session
}

// newTestPadmarkClient mirrors the mcp adapter's token handling: an envelope token is
// unpacked into base URL + raw key (unexported there — adapters do not reach into each
// other), the key goes into the Bearer header.
func newTestPadmarkClient(t *testing.T, serverURL, token string) *padmark.Client {
	t.Helper()

	// An empty token means anonymous: no bearer, the test server URL as base.
	baseURL, rawKey := serverURL, ""
	if url0, key, ok := domain.DecodeAPITokenEnvelope(token); ok {
		baseURL, rawKey = url0, key
	}

	base, err := url.Parse(baseURL)
	require.NoError(t, err)

	client, err := padmark.NewClient(base.String(), padmark.WithClient(&http.Client{
		Transport: &testBearer{base: http.DefaultTransport, token: rawKey},

		CheckRedirect: nil, Jar: nil, Timeout: 0,
	}))
	require.NoError(t, err)

	return client
}

// testBearer injects Authorization: Bearer on every request; an empty token sends no header.
type testBearer struct {
	base  http.RoundTripper
	token string
}

func (tr *testBearer) RoundTrip(req *http.Request) (*http.Response, error) {
	if tr.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+tr.token)
	}

	return tr.base.RoundTrip(req)
}

// ── tool-call helpers ──

func callOutput(
	ctx context.Context, t *testing.T, sess *mcpsdk.ClientSession, name string,
	params map[string]any,
) *mcpsdk.CallToolResult {
	t.Helper()

	res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: name, Arguments: params,
		Meta: nil, InputResponses: nil, RequestState: "",
	})
	require.NoError(t, err, "%s must succeed at the protocol level (tool errors are results, not protocol errors)", name)

	return res
}

func toolText(res *mcpsdk.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}

	if text, ok := res.Content[0].(*mcpsdk.TextContent); ok {
		return text.Text
	}

	return ""
}

func resultID(res *mcpsdk.CallToolResult) string {
	if m, ok := res.StructuredContent.(map[string]any); ok {
		if v, ok := m["id"].(string); ok {
			return v
		}
	}

	return ""
}

func boolField(res *mcpsdk.CallToolResult, key string) bool {
	if m, ok := res.StructuredContent.(map[string]any); ok {
		if v, ok := m[key].(bool); ok {
			return v
		}
	}

	return false
}
