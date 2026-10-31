//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/partyzanex/padmark/internal/usecases/auth"

	"github.com/stretchr/testify/require"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func TestLinkUpdateFlow(t *testing.T) {
	ctx := t.Context()
	env := newOwnerFlowEnv(t, "linkupdateflow")
	_, err := env.authMgr.AcceptFirstAdmin(ctx, "admin", ownerFlowPassword)
	require.NoError(t, err)
	admin, err := env.users.GetByUsername(ctx, "admin")
	require.NoError(t, err)
	adminKey, err := env.authMgr.CreateAPIToken(ctx, admin.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)
	invite, err := env.authMgr.GenerateInvite(ctx, admin.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	})
	require.NoError(t, err)
	_, err = env.authMgr.AcceptInvite(ctx, invite, "owner", ownerFlowPassword)
	require.NoError(t, err)
	owner, err := env.users.GetByUsername(ctx, "owner")
	require.NoError(t, err)
	ownerKey, err := env.authMgr.CreateAPIToken(ctx, owner.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	request := func(method, path, key, body string, status int) []byte {
		t.Helper()

		req, reqErr := http.NewRequestWithContext(ctx, method, env.baseURL+path, strings.NewReader(body))
		require.NoError(t, reqErr)
		req.Header.Set("Content-Type", "application/json")

		req.Header.Set("Accept", "application/json")

		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}

		client := &http.Client{
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },

			Transport: nil, Jar: nil, Timeout: 0,
		}

		resp, doErr := client.Do(req)
		require.NoError(t, doErr)

		defer resp.Body.Close()

		raw, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr)
		require.Equal(t, status, resp.StatusCode, string(raw))

		return raw
	}

	var created struct {
		ID        string    `json:"id"`
		EditCode  string    `json:"edit_code"`
		ExpiresAt time.Time `json:"expires_at"`
		CreatedAt time.Time `json:"created_at"`
	}

	raw := request(http.MethodPost, "/links", ownerKey,

		`{"target_url":"https://example.com/old","ttl":3600,"direct_redirect":true}`, http.StatusCreated)
	require.NoError(t, json.Unmarshal(raw, &created))
	require.NotEmpty(t, created.EditCode)
	path := "/links/" + created.ID
	request(http.MethodGet, "/r/"+created.ID, "", "", http.StatusMovedPermanently)

	// The owner bypasses edit_code while optional fields, counters and creation time survive.
	raw = request(http.MethodPut, path, ownerKey, `{"target_url":"https://example.com/new"}`, http.StatusOK)

	var updated struct {
		ID             string     `json:"id"`
		TargetURL      string     `json:"target_url"`
		Views          int        `json:"views"`
		ExpiresAt      *time.Time `json:"expires_at"`
		CreatedAt      time.Time  `json:"created_at"`
		DirectRedirect *bool      `json:"direct_redirect"`
	}
	require.NoError(t, json.Unmarshal(raw, &updated))
	require.Equal(t, created.ID, updated.ID)
	require.Equal(t, 1, updated.Views)
	require.Equal(t, created.CreatedAt, updated.CreatedAt)
	require.Equal(t, &created.ExpiresAt, updated.ExpiresAt)
	require.NotNil(t, updated.DirectRedirect)
	require.True(t, *updated.DirectRedirect)
	require.NotContains(t, string(raw), "edit_code")

	request(http.MethodPut, path, adminKey, `{"target_url":"https://example.com/admin"}`, http.StatusForbidden)
	request(http.MethodPut, path, "", `{"target_url":"https://example.com/wrong","edit_code":"wrong"}`,
		http.StatusForbidden)

	request(http.MethodPut, path, ownerKey, `{"target_url":"javascript:alert(1)"}`, http.StatusBadRequest)

	// The generated client can use the original code and explicitly clear both nullable fields.
	client, err := padmark.NewClient(env.baseURL)
	require.NoError(t, err)
	target, err := url.Parse("https://example.com/client")

	require.NoError(t, err)

	ttl := padmark.OptNilInt64{
		Value: 0, Set: false, Null: false,
	}
	ttl.SetToNull()

	direct := padmark.OptNilBool{
		Value: false, Set: false, Null: false,
	}
	direct.SetToNull()
	res, err := client.UpdateLink(ctx, &padmark.UpdateLinkRequest{
		TargetURL: *target, EditCode: padmark.NewOptString(created.EditCode), TTL: ttl, DirectRedirect: direct,

		Privacy: padmark.OptUpdateLinkRequestPrivacy{Value: "", Set: false},
	}, padmark.UpdateLinkParams{ID: created.ID})
	require.NoError(t, err)

	link, ok := res.(*padmark.LinkResponse)
	require.True(t, ok, "%T", res)
	require.Equal(t, target.String(), link.TargetURL.String())
	require.Equal(t, 1, link.Views)

	raw = request(http.MethodGet, path, "", "", http.StatusOK)
	updated.ExpiresAt = nil
	updated.DirectRedirect = nil
	require.NoError(t, json.Unmarshal(raw, &updated))
	require.Nil(t, updated.ExpiresAt)
	require.Nil(t, updated.DirectRedirect)

	request(http.MethodPut, path, ownerKey,
		`{"target_url":"https://example.com/private","privacy":"owner","ttl":60,"direct_redirect":false}`, http.StatusOK)
	request(http.MethodPut, path, "", `{"target_url":"https://example.com/blocked","edit_code":"`+created.EditCode+`"}`,
		http.StatusUnauthorized)
	request(http.MethodGet, path, "", "", http.StatusUnauthorized)
	raw = request(http.MethodGet, path, ownerKey, "", http.StatusOK)
	require.NoError(t, json.Unmarshal(raw, &updated))
	require.Equal(t, "https://example.com/private", updated.TargetURL)
	require.NotNil(t, updated.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(time.Minute), *updated.ExpiresAt, 5*time.Second)
	require.NotNil(t, updated.DirectRedirect)
	require.False(t, *updated.DirectRedirect)
	request(http.MethodPut, path, ownerKey, `{"target_url":"https://example.com/expired","ttl":0}`, http.StatusOK)
	request(http.MethodGet, path, ownerKey, "", http.StatusGone)
}
