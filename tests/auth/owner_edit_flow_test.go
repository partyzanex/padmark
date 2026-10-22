//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	"github.com/partyzanex/padmark/internal/infra/lock"
	"github.com/partyzanex/padmark/internal/infra/render"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const ownerFlowPassword = "OwnerP@ss123!"

// ownerFlowEnv is the fully wired stack an owner-flow test runs against: a real SQLite database
// with migrations applied, real managers, and the production router behind an httptest server.
type ownerFlowEnv struct {
	db      *bun.DB
	authMgr *auth.Manager
	users   *sqliterepo.UserRepository
	baseURL string
}

// newOwnerFlowEnv builds that stack. Shared by the API-token owner-bypass test and the edit-page
// privacy test so both exercise the same wiring the server actually assembles, rather than each
// standing up its own approximation of it.
// newOwnerFlowManagers builds the note and link managers over db, sharing one slug Locker
// between them exactly as the composition root does — a separate Locker per manager would leave
// the cross-table slug race open.
func newOwnerFlowManagers(t *testing.T, db *bun.DB, log *slog.Logger) (*notes.Manager, *links.Manager) {
	t.Helper()

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
	).WithCache(linkcache.NewDefault(log))

	return notesMgr, linksMgr
}

func newOwnerFlowEnv(t *testing.T, dbName string) ownerFlowEnv {
	t.Helper()

	ctx := context.Background()

	db, err := sqliterepo.Open("file:" + dbName + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = sqliterepo.Migrate(ctx, db)
	require.NoError(t, err)

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

	notesMgr, linksMgr := newOwnerFlowManagers(t, db, log)

	handler := adaptershttp.NewHandler(notesMgr, linksMgr, log).WithAuthManager(authMgr)
	ogen := adaptershttp.NewOgenHandler(notesMgr, linksMgr, adaptershttp.NoPinger{}, log)
	router, err := adaptershttp.NewRouter(handler, ogen, &adaptershttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   []byte("padmark-test-csrf-secret-32bytes"),
	})
	require.NoError(t, err)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return ownerFlowEnv{db: db, authMgr: authMgr, users: users, baseURL: srv.URL}
}

// TestOwnerEditFlow_APITokenOwner_EditsAndDeletesWithoutEditCode is the end-to-end regression for
// the owner-bypass feature: a note created by an authenticated API-token caller records that
// caller as owner_id (real SQLite migration + repository), and the exact same caller may then
// PUT/DELETE it with no edit_code at all through the real HTTP stack. A second, different
// authenticated caller must still be rejected without the edit_code — the bypass is strictly
// owner-scoped, never "any authenticated caller".
func TestOwnerEditFlow_APITokenOwner_EditsAndDeletesWithoutEditCode(t *testing.T) {
	ctx := context.Background()

	env := newOwnerFlowEnv(t, "ownereditflow")
	authMgr, users, srvURL := env.authMgr, env.users, env.baseURL

	// ── two accounts: the note's owner, and an unrelated second user ──
	_, err := authMgr.AcceptFirstAdmin(ctx, "owner", ownerFlowPassword)
	require.NoError(t, err)

	owner, err := users.GetByUsername(ctx, "owner")
	require.NoError(t, err)

	ownerKey, err := authMgr.CreateAPIToken(ctx, owner.ID, auth.Issuer{}, 0)
	require.NoError(t, err)

	inviteTok, err := authMgr.GenerateInvite(ctx, owner.ID, auth.Issuer{})
	require.NoError(t, err)

	_, err = authMgr.AcceptInvite(ctx, inviteTok, "other", ownerFlowPassword)
	require.NoError(t, err)

	other, err := users.GetByUsername(ctx, "other")
	require.NoError(t, err)

	otherKey, err := authMgr.CreateAPIToken(ctx, other.ID, auth.Issuer{}, 0)
	require.NoError(t, err)

	// ── owner creates a note over the API-token-authenticated path ──
	created := postNote(t, srvURL, ownerKey, `{"title":"t","content":"c"}`)

	// ── owner edits it with NO edit_code — the token alone must be enough ──
	updateReq, err := http.NewRequestWithContext(ctx, http.MethodPut,
		srvURL+"/notes/"+created.ID, strings.NewReader(`{"title":"updated","content":"new body"}`))
	require.NoError(t, err)
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("Authorization", "Bearer "+ownerKey)

	updateResp, err := http.DefaultClient.Do(updateReq)
	require.NoError(t, err)

	defer updateResp.Body.Close()

	require.Equal(t, http.StatusOK, updateResp.StatusCode,
		"the note's own API-token owner must be able to update it without edit_code")

	// ── a different authenticated user, same request shape, must still be rejected ──
	hijackReq, err := http.NewRequestWithContext(ctx, http.MethodPut,
		srvURL+"/notes/"+created.ID, strings.NewReader(`{"title":"hijacked","content":"x"}`))
	require.NoError(t, err)
	hijackReq.Header.Set("Content-Type", "application/json")
	hijackReq.Header.Set("Authorization", "Bearer "+otherKey)

	hijackResp, err := http.DefaultClient.Do(hijackReq)
	require.NoError(t, err)

	defer hijackResp.Body.Close()

	require.Equal(t, http.StatusForbidden, hijackResp.StatusCode,
		"the owner bypass must not extend to a different authenticated user without the edit_code")

	// ── owner deletes it with NO edit_code ──
	deleteReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, srvURL+"/notes/"+created.ID, nil)
	require.NoError(t, err)
	deleteReq.Header.Set("Authorization", "Bearer "+ownerKey)

	deleteResp, err := http.DefaultClient.Do(deleteReq)
	require.NoError(t, err)

	defer deleteResp.Body.Close()

	require.Equal(t, http.StatusNoContent, deleteResp.StatusCode,
		"the note's own API-token owner must be able to delete it without edit_code")
}

type createdNote struct {
	ID string `json:"id"`
}

// postNote creates a note as bearerKey and returns the decoded response.
func postNote(t *testing.T, baseURL, bearerKey, body string) createdNote {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		baseURL+"/notes", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearerKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "create note: %s", raw)

	var note createdNote

	require.NoError(t, json.Unmarshal(raw, &note))

	return note
}

// ── GET /edit/{id} privacy gate ──

// editPageCaller is one way of asking for the edit page: which credential is presented, and
// whether the client looks like a browser (Accept: text/html) or an API client.
type editPageCaller struct {
	name   string
	accept string
	bearer string
}

// getEditPage requests the edit page without following redirects, so the gate's own response is
// observed rather than whatever /login renders afterwards. Returns the response and its body.
func getEditPage(t *testing.T, baseURL, noteID string, caller editPageCaller) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/edit/"+noteID, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", caller.accept)

	if caller.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+caller.bearer)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp, string(raw)
}

// TestOwnerEditFlow_EditPage_PrivacyGate exercises GET /edit/{id} through the real HTTP stack for
// every combination of note privacy and caller. The editor page renders a note's decrypted
// plaintext into the form, so it is a read surface: whoever cannot read a note via GET
// /notes/{id} must not reach its content here either.
//
// Reading the auth middleware alone does not answer this. GET /edit/{id} is deliberately let
// through there (isPublicRoute), and the decision is made further in, by EditPage →
// handlePrivateAuth → denyPrivate. What comes back depends on the caller: an anonymous browser
// gets 303 to /login (signing in is what would grant access), a signed-in browser that still may
// not read gets 403 (it holds the wrong credential, not none), and a non-HTML client gets 401.
// Only an end-to-end request shows which of the three applies.
func TestOwnerEditFlow_EditPage_PrivacyGate(t *testing.T) {
	ctx := context.Background()

	env := newOwnerFlowEnv(t, "editpageprivacy")

	_, err := env.authMgr.AcceptFirstAdmin(ctx, "owner", ownerFlowPassword)
	require.NoError(t, err)

	owner, err := env.users.GetByUsername(ctx, "owner")
	require.NoError(t, err)

	ownerKey, err := env.authMgr.CreateAPIToken(ctx, owner.ID, auth.Issuer{}, 0)
	require.NoError(t, err)

	inviteTok, err := env.authMgr.GenerateInvite(ctx, owner.ID, auth.Issuer{})
	require.NoError(t, err)

	_, err = env.authMgr.AcceptInvite(ctx, inviteTok, "other", ownerFlowPassword)
	require.NoError(t, err)

	other, err := env.users.GetByUsername(ctx, "other")
	require.NoError(t, err)

	otherKey, err := env.authMgr.CreateAPIToken(ctx, other.ID, auth.Issuer{}, 0)
	require.NoError(t, err)

	anonBrowser := editPageCaller{name: "anonymous browser", accept: "text/html", bearer: ""}
	anonAPI := editPageCaller{name: "anonymous api client", accept: "application/json", bearer: ""}
	otherBrowser := editPageCaller{name: "non-owner browser", accept: "text/html", bearer: otherKey}
	otherAPI := editPageCaller{name: "non-owner api client", accept: "application/json", bearer: otherKey}
	ownerBrowser := editPageCaller{name: "owner browser", accept: "text/html", bearer: ownerKey}
	// The header a real browser actually sends, rather than the bare "text/html" above: negotiate
	// walks the list and must still pick HTML from it, or a denied navigation would get a bare
	// 401 in place of the login redirect.
	realBrowser := editPageCaller{
		name:   "anonymous browser (full Accept)",
		accept: "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
	}

	// want is the expected status; a 303 additionally carries the login redirect. reachesContent
	// says whether the caller is allowed to see the note's plaintext in the response.
	cases := map[string][]struct {
		caller         editPageCaller
		want           int
		reachesContent bool
	}{
		"owner": {
			{anonBrowser, http.StatusSeeOther, false},
			{realBrowser, http.StatusSeeOther, false},
			{anonAPI, http.StatusUnauthorized, false},
			// Signed in, but as the wrong account: a login page would only send them back here.
			{otherBrowser, http.StatusForbidden, false},
			{otherAPI, http.StatusUnauthorized, false},
			{ownerBrowser, http.StatusOK, true},
		},
		"authenticated": {
			{anonBrowser, http.StatusSeeOther, false},
			{anonAPI, http.StatusUnauthorized, false},
			// "authenticated" means any signed-in caller, owner or not — by definition, not an oversight.
			{otherBrowser, http.StatusOK, true},
			{otherAPI, http.StatusOK, true},
			{ownerBrowser, http.StatusOK, true},
		},
		"public": {
			{anonBrowser, http.StatusOK, true},
			{anonAPI, http.StatusOK, true},
			{otherBrowser, http.StatusOK, true},
			{otherAPI, http.StatusOK, true},
			{ownerBrowser, http.StatusOK, true},
		},
	}

	for privacy, callers := range cases {
		t.Run(privacy, func(t *testing.T) {
			// The marker is unique per privacy level so a body assertion cannot pass by picking up
			// another note's content.
			secret := "SECRET-" + privacy
			note := postNote(t, env.baseURL, ownerKey,
				`{"title":"t","content":"`+secret+`","privacy":"`+privacy+`"}`)

			for _, want := range callers {
				resp, body := getEditPage(t, env.baseURL, note.ID, want.caller)

				require.Equal(t, want.want, resp.StatusCode, "%s on a %s note", want.caller.name, privacy)

				if want.want == http.StatusSeeOther {
					require.Equal(t, "/login?next=%2Fedit%2F"+note.ID, resp.Header.Get("Location"),
						"a denied anonymous browser is sent to the login page carrying its original target")
				}

				if want.want == http.StatusForbidden {
					require.Empty(t, resp.Header.Get("Location"),
						"a settled 403 must not bounce the caller anywhere")
				}

				require.Equal(t, want.reachesContent, strings.Contains(body, secret),
					"%s must%s reach the plaintext of a %s note",
					want.caller.name, map[bool]string{true: "", false: " not"}[want.reachesContent], privacy)
			}
		})
	}
}
