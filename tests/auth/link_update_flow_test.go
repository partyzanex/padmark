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

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/usecases/auth"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

func (s *OwnerFlowSuite) TestLinkUpdateFlow() {
	f := newLinkUpdateFlow(s.T(), s.env)
	created := f.createAndUpdate()
	f.clearWithClient(created)
	f.restrictAndExpire(created)
}

// createdLink is the create answer of a short link.
type createdLink struct {
	ID        string    `json:"id"`
	EditCode  string    `json:"edit_code"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// updatedLink is the read and update answer of a short link.
type updatedLink struct {
	ID             string     `json:"id"`
	TargetURL      string     `json:"target_url"`
	Views          int        `json:"views"`
	ExpiresAt      *time.Time `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
	DirectRedirect *bool      `json:"direct_redirect"`
}

// linkUpdateFlow holds the stack and the API keys of TestLinkUpdateFlow: the admin and the
// invited owner of the link.
type linkUpdateFlow struct {
	t        *testing.T
	baseURL  string
	adminKey string
	ownerKey string
}

func newLinkUpdateFlow(t *testing.T, env ownerFlowEnv) *linkUpdateFlow {
	t.Helper()

	ctx := t.Context()
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

	return &linkUpdateFlow{t: t, baseURL: env.baseURL, adminKey: adminKey, ownerKey: ownerKey}
}

// request sends one JSON request without following redirects, checks its status and returns the
// body.
func (f *linkUpdateFlow) request(method, path, key, body string, status int) []byte {
	f.t.Helper()

	req, reqErr := http.NewRequestWithContext(f.t.Context(), method, f.baseURL+path, strings.NewReader(body))
	require.NoError(f.t, reqErr)
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
	require.NoError(f.t, doErr)

	defer resp.Body.Close()

	raw, readErr := io.ReadAll(resp.Body)
	require.NoError(f.t, readErr)
	require.Equal(f.t, status, resp.StatusCode, string(raw))

	return raw
}

// createAndUpdate creates the link as the owner and checks the owner update without the edit
// code and the refused updates. It returns the create answer.
func (f *linkUpdateFlow) createAndUpdate() createdLink {
	t := f.t

	var created createdLink

	raw := f.request(http.MethodPost, "/links", f.ownerKey,
		`{"target_url":"https://example.com/old","ttl":3600,"direct_redirect":true}`, http.StatusCreated)
	require.NoError(t, json.Unmarshal(raw, &created))
	require.NotEmpty(t, created.EditCode)
	path := "/links/" + created.ID
	f.request(http.MethodGet, "/r/"+created.ID, "", "", http.StatusMovedPermanently)

	// The owner bypasses edit_code while optional fields, counters and creation time survive.
	raw = f.request(http.MethodPut, path, f.ownerKey, `{"target_url":"https://example.com/new"}`, http.StatusOK)

	var updated updatedLink

	require.NoError(t, json.Unmarshal(raw, &updated))
	require.Equal(t, created.ID, updated.ID)
	require.Equal(t, 1, updated.Views)
	require.Equal(t, created.CreatedAt, updated.CreatedAt)
	require.Equal(t, &created.ExpiresAt, updated.ExpiresAt)
	require.NotNil(t, updated.DirectRedirect)
	require.True(t, *updated.DirectRedirect)
	require.NotContains(t, string(raw), "edit_code")

	f.request(http.MethodPut, path, f.adminKey, `{"target_url":"https://example.com/admin"}`, http.StatusForbidden)
	f.request(http.MethodPut, path, "", `{"target_url":"https://example.com/wrong","edit_code":"wrong"}`,
		http.StatusForbidden)

	f.request(http.MethodPut, path, f.ownerKey, `{"target_url":"javascript:alert(1)"}`, http.StatusBadRequest)

	return created
}

// clearWithClient clears the expiry and the redirect mode with the generated client and the
// original edit code.
func (f *linkUpdateFlow) clearWithClient(created createdLink) {
	t := f.t
	path := "/links/" + created.ID

	// The generated client can use the original code and explicitly clear both nullable fields.
	client, err := padmark.NewClient(f.baseURL)
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
	res, err := client.UpdateLink(t.Context(), &padmark.UpdateLinkRequest{
		TargetURL: *target, EditCode: padmark.NewOptString(created.EditCode), TTL: ttl, DirectRedirect: direct,

		Privacy: padmark.OptUpdateLinkRequestPrivacy{Value: "", Set: false},
	}, padmark.UpdateLinkParams{ID: created.ID})
	require.NoError(t, err)

	link, ok := res.(*padmark.LinkResponse)
	require.True(t, ok, "%T", res)
	require.Equal(t, target.String(), link.TargetURL.String())
	require.Equal(t, 1, link.Views)

	// The bare slug serves the same link: no note has this id in the shared namespace.
	f.request(http.MethodGet, "/"+created.ID, "", "", http.StatusMovedPermanently)

	raw := f.request(http.MethodGet, path, "", "", http.StatusOK)

	var updated updatedLink

	require.NoError(t, json.Unmarshal(raw, &updated))
	require.Nil(t, updated.ExpiresAt)
	require.Nil(t, updated.DirectRedirect)
}

// restrictAndExpire makes the link private to the owner, then expires it.
func (f *linkUpdateFlow) restrictAndExpire(created createdLink) {
	t := f.t
	path := "/links/" + created.ID

	f.request(http.MethodPut, path, f.ownerKey,
		`{"target_url":"https://example.com/private","privacy":"owner","ttl":60,"direct_redirect":false}`, http.StatusOK)
	f.request(http.MethodPut, path, "", `{"target_url":"https://example.com/blocked","edit_code":"`+created.EditCode+`"}`,
		http.StatusUnauthorized)
	f.request(http.MethodGet, path, "", "", http.StatusUnauthorized)
	raw := f.request(http.MethodGet, path, f.ownerKey, "", http.StatusOK)

	var updated updatedLink

	require.NoError(t, json.Unmarshal(raw, &updated))
	require.Equal(t, "https://example.com/private", updated.TargetURL)
	require.NotNil(t, updated.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(time.Minute), *updated.ExpiresAt, 5*time.Second)
	require.NotNil(t, updated.DirectRedirect)
	require.False(t, *updated.DirectRedirect)
	f.request(http.MethodPut, path, f.ownerKey, `{"target_url":"https://example.com/expired","ttl":0}`, http.StatusOK)
	f.request(http.MethodGet, path, f.ownerKey, "", http.StatusGone)
}
