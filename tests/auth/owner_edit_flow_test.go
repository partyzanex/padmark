//go:build integration

package integration

import (
	"context"
	"database/sql"
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
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/render"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const ownerFlowPassword = "OwnerP@ss123!"

// TestOwnerEditFlow_APITokenOwner_EditsAndDeletesWithoutEditCode is the end-to-end regression for
// the owner-bypass feature: a note created by an authenticated API-token caller records that
// caller as owner_id (real SQLite migration + repository), and the exact same caller may then
// PUT/DELETE it with no edit_code at all through the real HTTP stack. A second, different
// authenticated caller must still be rejected without the edit_code — the bypass is strictly
// owner-scoped, never "any authenticated caller".
func TestOwnerEditFlow_APITokenOwner_EditsAndDeletesWithoutEditCode(t *testing.T) {
	ctx := context.Background()

	sqldb, err := sql.Open(sqliteshim.DriverName(),
		"file:ownereditflow?mode=memory&cache=shared&_busy_timeout=5000&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqldb.Close()) })

	db := bun.NewDB(sqldb, sqlitedialect.New())
	_, err = sqliterepo.Migrate(ctx, db)
	require.NoError(t, err)

	log := slog.New(slog.DiscardHandler)
	users := sqliterepo.NewUserRepository(db)

	authMgr, err := auth.NewManager(
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
	require.NoError(t, err)

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
	router, err := adaptershttp.NewRouter(handler, ogen, &adaptershttp.RouterOptions{
		CookieMaxAge: 3600,
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   []byte("padmark-test-csrf-secret-32bytes"),
	})
	require.NoError(t, err)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	// ── two accounts: the note's owner, and an unrelated second user ──
	_, err = authMgr.AcceptFirstAdmin(ctx, "owner", ownerFlowPassword)
	require.NoError(t, err)

	owner, err := users.GetByUsername(ctx, "owner")
	require.NoError(t, err)

	ownerKey, err := authMgr.CreateAPIToken(ctx, owner.ID)
	require.NoError(t, err)

	inviteTok, err := authMgr.GenerateInvite(ctx, owner.ID)
	require.NoError(t, err)

	_, err = authMgr.AcceptInvite(ctx, inviteTok, "other", ownerFlowPassword)
	require.NoError(t, err)

	other, err := users.GetByUsername(ctx, "other")
	require.NoError(t, err)

	otherKey, err := authMgr.CreateAPIToken(ctx, other.ID)
	require.NoError(t, err)

	// ── owner creates a note over the API-token-authenticated path ──
	created := postNote(t, srv.URL, ownerKey, `{"title":"t","content":"c"}`)

	// ── owner edits it with NO edit_code — the token alone must be enough ──
	updateReq, err := http.NewRequestWithContext(ctx, http.MethodPut,
		srv.URL+"/notes/"+created.ID, strings.NewReader(`{"title":"updated","content":"new body"}`))
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
		srv.URL+"/notes/"+created.ID, strings.NewReader(`{"title":"hijacked","content":"x"}`))
	require.NoError(t, err)
	hijackReq.Header.Set("Content-Type", "application/json")
	hijackReq.Header.Set("Authorization", "Bearer "+otherKey)

	hijackResp, err := http.DefaultClient.Do(hijackReq)
	require.NoError(t, err)

	defer hijackResp.Body.Close()

	require.Equal(t, http.StatusForbidden, hijackResp.StatusCode,
		"the owner bypass must not extend to a different authenticated user without the edit_code")

	// ── owner deletes it with NO edit_code ──
	deleteReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, srv.URL+"/notes/"+created.ID, nil)
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
