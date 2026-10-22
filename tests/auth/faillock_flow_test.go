//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFailLockoutPrincipals(t *testing.T) {
	ctx := t.Context()
	env := newOwnerFlowEnv(t, "faillock-principals")
	_, err := env.authMgr.AcceptFirstAdmin(ctx, "owner", ownerFlowPassword)
	require.NoError(t, err)
	owner, err := env.users.GetByUsername(ctx, "owner")
	require.NoError(t, err)
	ownerKey, err := env.authMgr.CreateAPIToken(ctx, owner.ID)
	require.NoError(t, err)
	invite, err := env.authMgr.GenerateInvite(ctx, owner.ID)
	require.NoError(t, err)
	_, err = env.authMgr.AcceptInvite(ctx, invite, "attacker", ownerFlowPassword)
	require.NoError(t, err)
	attacker, err := env.users.GetByUsername(ctx, "attacker")
	require.NoError(t, err)
	attackerKey, err := env.authMgr.CreateAPIToken(ctx, attacker.ID)
	require.NoError(t, err)
	rotatedKey, err := env.authMgr.CreateAPIToken(ctx, attacker.ID)
	require.NoError(t, err)

	for _, testCase := range []struct{ resource, body string }{
		{"notes", `{"content":"updated content"}`},
		{"links", `{"target_url":"https://example.com/new"}`},
	} {
		t.Run(testCase.resource, func(t *testing.T) {
			request := func(method, path, bearer, body string, status int) []byte {
				t.Helper()

				req, reqErr := http.NewRequestWithContext(ctx, method, env.baseURL+path, strings.NewReader(body))
				require.NoError(t, reqErr)
				req.Header.Set("Content-Type", "application/json")

				if bearer != "" {
					req.Header.Set("Authorization", "Bearer "+bearer)
				}

				resp, doErr := http.DefaultClient.Do(req)
				require.NoError(t, doErr)

				defer resp.Body.Close()

				raw, readErr := io.ReadAll(resp.Body)
				require.NoError(t, readErr)
				require.Equal(t, status, resp.StatusCode, string(raw))

				return raw
			}
			raw := request(http.MethodPost, "/"+testCase.resource, ownerKey, testCase.body, http.StatusCreated)

			var created struct {
				ID string `json:"id"`
			}

			require.NoError(t, json.Unmarshal(raw, &created))
			path := "/" + testCase.resource + "/" + created.ID
			wrongBody := strings.TrimSuffix(testCase.body, "}") + `,"edit_code":"wrong"}`

			for range 10 {
				request(http.MethodPut, path, "", wrongBody, http.StatusForbidden)
			}

			request(http.MethodPut, path, "", wrongBody, http.StatusTooManyRequests)
			request(http.MethodPut, path, "bogus-key", wrongBody, http.StatusTooManyRequests)
			request(http.MethodPut, path, ownerKey, testCase.body, http.StatusOK)
			request(http.MethodDelete, path, "", "", http.StatusTooManyRequests)

			// A signed-in attacker remains limited, across all their API tokens.
			for range 10 {
				request(http.MethodPut, path, attackerKey, wrongBody, http.StatusForbidden)
			}

			request(http.MethodPut, path, attackerKey, wrongBody, http.StatusTooManyRequests)
			request(http.MethodPut, path, rotatedKey, wrongBody, http.StatusTooManyRequests)
			request(http.MethodDelete, path, rotatedKey, "", http.StatusTooManyRequests)
			request(http.MethodPut, path, ownerKey, testCase.body, http.StatusOK)
			request(http.MethodPut, path, attackerKey, wrongBody, http.StatusTooManyRequests)
			request(http.MethodDelete, path, ownerKey, "", http.StatusNoContent)
		})
	}
}
