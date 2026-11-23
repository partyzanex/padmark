//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

func (s *OwnerFlowSuite) TestLinkCacheReplicas() {
	t := s.T()

	env := s.env
	_, err := env.authMgr.AcceptFirstAdmin(t.Context(), "owner", ownerFlowPassword)
	require.NoError(t, err)
	owner, err := env.users.GetByUsername(t.Context(), "owner")
	require.NoError(t, err)
	key, err := env.authMgr.CreateAPIToken(t.Context(), owner.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	managers := testenv.NewManagers(t, env.db)
	cache := linkcache.NewDefault(slog.New(slog.DiscardHandler))
	managers.Links.WithCache(cache)
	handlerCfg := testenv.HandlerConfig(managers.Notes, managers.Links)
	handlerCfg.Accounts = env.authMgr
	handlerCfg.CSRFSecret = []byte(testenv.CSRFKey)
	second := testenv.Serve(t, handlerCfg)

	for _, testCase := range []replicaCase{
		{"owner", `,"privacy":"owner"`, http.StatusUnauthorized},
		{"authenticated", `,"privacy":"authenticated"`, http.StatusUnauthorized},
		{"new_target", "", http.StatusOK},
		{"expired", `,"ttl":0`, http.StatusGone},
		{"deleted", "", http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runReplicaCase(t, env, replicaB{url: second.URL, cache: cache}, key, testCase)
		})
	}
}

// replicaCase is one change of a cached short link on replica A and the answer of replica B.
type replicaCase struct {
	name, fields string
	status       int
}

// replicaB is the second replica of TestLinkCacheReplicas: its URL and its own link cache.
type replicaB struct {
	cache *linkcache.Cache
	url   string
}

// runReplicaCase warms the caches of both replicas, changes the link through replica A as
// testCase says, and checks the answers of replica B.
func runReplicaCase(t *testing.T, env ownerFlowEnv, replica replicaB, key string, testCase replicaCase) {
	t.Helper()

	request := replicaLinkRequest(t)
	before := "https://example.com/public-" + testCase.name
	after := "https://example.com/changed-" + testCase.name
	_, raw := request(env.baseURL, http.MethodPost, "/links", key,
		`{"target_url":"`+before+`","direct_redirect":true}`, http.StatusCreated)

	var created struct {
		ID string `json:"id"`
	}

	require.NoError(t, json.Unmarshal(raw, &created))
	path, redirectPath := "/links/"+created.ID, "/r/"+created.ID
	// Warm independent caches on both replicas before changing the shared row.
	request(env.baseURL, http.MethodGet, path, "", "", http.StatusOK)
	request(replica.url, http.MethodGet, path, "", "", http.StatusOK)

	var ciphertext string

	require.NoError(t, env.db.NewSelect().Table("links").Column("target_url").
		Where("id = ?", domain.HashSlug(created.ID)).Scan(t.Context(), &ciphertext))
	cached, found := replica.cache.Get(domain.HashSlug(created.ID), ciphertext)
	require.True(t, found, "replica B must actually have the old target cached")
	require.Equal(t, before, cached)

	if testCase.name == "deleted" {
		request(env.baseURL, http.MethodDelete, path, key, "", http.StatusNoContent)
	} else {
		request(env.baseURL, http.MethodPut, path, key,
			`{"target_url":"`+after+`"`+testCase.fields+`}`, http.StatusOK)
	}

	_, raw = request(replica.url, http.MethodGet, path, "", "", testCase.status)
	if testCase.status != http.StatusOK {
		require.NotContains(t, string(raw), before)
		require.NotContains(t, string(raw), after)
	} else {
		require.Contains(t, string(raw), after)
	}

	redirectStatus := testCase.status
	if redirectStatus == http.StatusOK {
		redirectStatus = http.StatusMovedPermanently
	}

	header, raw := request(replica.url, http.MethodGet, redirectPath, "", "", redirectStatus)
	if redirectStatus == http.StatusMovedPermanently {
		require.Equal(t, after, header.Get("Location"))
	} else {
		require.Empty(t, header.Get("Location"))
		require.NotContains(t, string(raw), before)
		require.NotContains(t, string(raw), after)
	}

	if testCase.status == http.StatusUnauthorized {
		header, _ = request(replica.url, http.MethodGet, redirectPath, key, "", http.StatusMovedPermanently)
		require.Equal(t, after, header.Get("Location"), "owner still has access")
	}
}

func replicaLinkRequest(t *testing.T) func(string, string, string, string, string, int) (http.Header, []byte) {
	t.Helper()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     nil, Jar: nil, Timeout: 0,
	}

	return func(baseURL, method, path, bearer, body string, status int) (http.Header, []byte) {
		t.Helper()

		req, err := http.NewRequestWithContext(t.Context(), method, baseURL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")

		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}

		resp, err := client.Do(req)
		require.NoError(t, err)

		defer resp.Body.Close()

		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, status, resp.StatusCode, string(raw))

		return resp.Header, raw
	}
}
