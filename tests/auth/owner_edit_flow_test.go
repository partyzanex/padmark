//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"

	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/tests/internal/testenv"
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

// OwnerFlowSuite runs the flows on the stack of newOwnerFlowEnv. SetupTest builds a new stack
// for each test, with a database named after the test.
type OwnerFlowSuite struct {
	suite.Suite

	env ownerFlowEnv
}

func TestOwnerFlowSuite(t *testing.T) {
	suite.Run(t, new(OwnerFlowSuite))
}

func (s *OwnerFlowSuite) SetupTest() {
	s.env = newOwnerFlowEnv(s.T(), testDBName(s.T()))
}

// testDBName returns the in-memory database name of the test t: each test gets its own database.
func testDBName(t *testing.T) string {
	t.Helper()

	return strings.ReplaceAll(t.Name(), "/", "_")
}

// newOwnerFlowEnv builds that stack over the in-memory database dbName: testenv.NewStack with the
// account system and the link cache, as the server assembles it. opts add the differences of a
// scenario, for example its bootstrap token.
func newOwnerFlowEnv(t *testing.T, dbName string, opts ...testenv.Option) ownerFlowEnv {
	t.Helper()

	stack := testenv.NewStack(t, dbName,
		append([]testenv.Option{testenv.WithAccounts(), testenv.WithLinkCache()}, opts...)...)

	return ownerFlowEnv{db: stack.DB, authMgr: stack.Auth, users: stack.Users, baseURL: stack.Server.URL}
}

// TestAPITokenOwner_EditsAndDeletesWithoutEditCode is the end-to-end regression for
// the owner-bypass feature: a note created by an authenticated API-token caller records that
// caller as owner_id (real SQLite migration + repository), and the exact same caller may then
// PUT/DELETE it with no edit_code at all through the real HTTP stack. A second, different
// authenticated caller must still be rejected without the edit_code — the bypass is strictly
// owner-scoped, never "any authenticated caller".
func (s *OwnerFlowSuite) TestAPITokenOwner_EditsAndDeletesWithoutEditCode() {
	t := s.T()
	ctx := t.Context()

	srvURL := s.env.baseURL

	// ── two accounts: the note's owner, and an unrelated second user ──.
	ownerKey, otherKey := ownerAndOtherKeys(t, s.env)

	// ── owner creates a note over the API-token-authenticated path ──.
	created := postNote(t, srvURL, ownerKey, `{"title":"t","content":"c"}`)

	// ── owner edits it with NO edit_code — the token alone must be enough ──.
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

	// ── a different authenticated user, same request shape, must still be rejected ──.
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

	// ── owner deletes it with NO edit_code ──.
	deleteReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, srvURL+"/notes/"+created.ID, nil)
	require.NoError(t, err)
	deleteReq.Header.Set("Authorization", "Bearer "+ownerKey)

	deleteResp, err := http.DefaultClient.Do(deleteReq)
	require.NoError(t, err)

	defer deleteResp.Body.Close()

	require.Equal(t, http.StatusNoContent, deleteResp.StatusCode,
		"the note's own API-token owner must be able to delete it without edit_code")
}

// ownerAndOtherKeys makes two accounts on env, the first admin "owner" and the invited "other",
// and returns an API key of each.
func ownerAndOtherKeys(t *testing.T, env ownerFlowEnv) (ownerKey, otherKey string) {
	t.Helper()

	ctx := t.Context()
	authMgr, users := env.authMgr, env.users

	_, err := authMgr.AcceptFirstAdmin(ctx, "owner", ownerFlowPassword)
	require.NoError(t, err)

	owner, err := users.GetByUsername(ctx, "owner")
	require.NoError(t, err)

	ownerKey, err = authMgr.CreateAPIToken(ctx, owner.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	inviteTok, err := authMgr.GenerateInvite(ctx, owner.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	})
	require.NoError(t, err)

	_, err = authMgr.AcceptInvite(ctx, inviteTok, "other", ownerFlowPassword)
	require.NoError(t, err)

	other, err := users.GetByUsername(ctx, "other")
	require.NoError(t, err)

	otherKey, err = authMgr.CreateAPIToken(ctx, other.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	return ownerKey, otherKey
}

type createdNote struct {
	ID string `json:"id"`
}

// postNote creates a note as bearerKey and returns the decoded response.
func postNote(t *testing.T, baseURL, bearerKey, body string) createdNote {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
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

// ── GET /edit/{id} privacy check ──.

// editPageCaller is one way of asking for the edit page: which credential is presented, and
// whether the client looks like a browser (Accept: text/html) or an API client.
type editPageCaller struct {
	name   string
	accept string
	bearer string
}

// getEditPage requests the edit page without following redirects, so the response of the privacy
// check is observed rather than whatever /login renders afterwards. Returns the response and its body.
func getEditPage(t *testing.T, baseURL, noteID string, caller editPageCaller) (int, http.Header, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/edit/"+noteID, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", caller.accept)

	if caller.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+caller.bearer)
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: nil, Jar: nil, Timeout: 0,
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, resp.Header, string(raw)
}

// TestEditPage_ContentOnlyForReaders exercises GET /edit/{id} through the real HTTP stack for
// every combination of note privacy and caller. The editor page renders a note's decrypted
// plaintext into the form, so it is a read surface: whoever cannot read a note via GET
// /notes/{id} must not reach its content here either.
//
// Reading the auth middleware alone does not answer this. GET /edit/{id} is deliberately let
// through there (isPublicRoute), and the decision is made further in: EditPage → editNote →
// notes.Manager.Inspect, and denyPrivate for its domain.ErrForbidden. What comes back depends on
// the caller: an anonymous browser gets 303 to /login (signing in is what would grant access), a
// signed-in browser that still may not read gets 403 (it holds the wrong credential, not none),
// and a non-HTML client gets 401.
// Only an end-to-end request shows which of the three applies.
func (s *OwnerFlowSuite) TestEditPage_ContentOnlyForReaders() {
	t := s.T()
	env := s.env
	ownerKey, otherKey := ownerAndOtherKeys(t, env)

	cases := editPageCases(ownerKey, otherKey)

	for privacy, callers := range cases {
		t.Run(privacy, func(t *testing.T) {
			// The marker is unique per privacy level so a body assertion cannot pass by picking up
			// another note's content.
			secret := "SECRET-" + privacy
			note := postNote(t, env.baseURL, ownerKey,
				`{"title":"t","content":"`+secret+`","privacy":"`+privacy+`"}`)

			for _, want := range callers {
				status, header, body := getEditPage(t, env.baseURL, note.ID, want.caller)

				require.Equal(t, want.want, status, "%s on a %s note", want.caller.name, privacy)

				if want.want == http.StatusSeeOther {
					require.Equal(t, "/login?next=%2Fedit%2F"+note.ID, header.Get("Location"),
						"a denied anonymous browser is sent to the login page carrying its original target")
				}

				if want.want == http.StatusForbidden {
					require.Empty(t, header.Get("Location"),
						"a settled 403 must not bounce the caller anywhere")
				}

				require.Equal(t, want.reachesContent, strings.Contains(body, secret),
					"%s must%s reach the plaintext of a %s note",
					want.caller.name, map[bool]string{true: "", false: " not"}[want.reachesContent], privacy)
			}
		})
	}
}

// editPageCase is one caller of the edit page and its expected answer. want is the expected
// status; a 303 additionally carries the login redirect. reachesContent says whether the caller
// is allowed to see the note's plaintext in the response.
type editPageCase struct {
	caller         editPageCaller
	want           int
	reachesContent bool
}

// editPageCases returns the callers of TestEditPage_ContentOnlyForReaders for each note privacy level.
func editPageCases(ownerKey, otherKey string) map[string][]editPageCase {
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

		bearer: "",
	}

	return map[string][]editPageCase{
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
}
