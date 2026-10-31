//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

func TestLinkCacheReplicas(t *testing.T) {
	env := newOwnerFlowEnv(t, "link-cache-replicas")
	_, err := env.authMgr.AcceptFirstAdmin(t.Context(), "owner", ownerFlowPassword)
	require.NoError(t, err)
	owner, err := env.users.GetByUsername(t.Context(), "owner")
	require.NoError(t, err)
	key, err := env.authMgr.CreateAPIToken(t.Context(), owner.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	log := slog.New(slog.DiscardHandler)
	notes, links := newOwnerFlowManagers(t, env.db, log)
	cache := linkcache.NewDefault(log)
	links.WithCache(cache)
	handler := adaptershttp.NewHandler(notes, links, log).WithAuthManager(env.authMgr)
	ogen := adaptershttp.NewOgenHandler(notes, links, adaptershttp.NoPinger{}, log)
	router, err := adaptershttp.NewRouter(handler, ogen, &adaptershttp.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   []byte("padmark-test-csrf-secret-32bytes"),

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	})
	require.NoError(t, err)

	second := httptest.NewServer(router)
	t.Cleanup(second.Close)

	for _, testCase := range []struct {
		name, fields string
		status       int
	}{
		{"owner", `,"privacy":"owner"`, http.StatusUnauthorized},
		{"authenticated", `,"privacy":"authenticated"`, http.StatusUnauthorized},
		{"new_target", "", http.StatusOK},
		{"expired", `,"ttl":0`, http.StatusGone},
		{"deleted", "", http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
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
			request(second.URL, http.MethodGet, path, "", "", http.StatusOK)

			var ciphertext string

			require.NoError(t, env.db.NewSelect().Table("links").Column("target_url").
				Where("id = ?", domain.HashSlug(created.ID)).Scan(t.Context(), &ciphertext))
			cached, found := cache.Get(domain.HashSlug(created.ID), ciphertext)
			require.True(t, found, "replica B must actually have the old target cached")
			require.Equal(t, before, cached)

			if testCase.name == "deleted" {
				request(env.baseURL, http.MethodDelete, path, key, "", http.StatusNoContent)
			} else {
				request(env.baseURL, http.MethodPut, path, key,
					`{"target_url":"`+after+`"`+testCase.fields+`}`, http.StatusOK)
			}

			_, raw = request(second.URL, http.MethodGet, path, "", "", testCase.status)
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

			header, raw := request(second.URL, http.MethodGet, redirectPath, "", "", redirectStatus)
			if redirectStatus == http.StatusMovedPermanently {
				require.Equal(t, after, header.Get("Location"))
			} else {
				require.Empty(t, header.Get("Location"))
				require.NotContains(t, string(raw), before)
				require.NotContains(t, string(raw), after)
			}

			if testCase.status == http.StatusUnauthorized {
				header, _ = request(second.URL, http.MethodGet, redirectPath, key, "", http.StatusMovedPermanently)
				require.Equal(t, after, header.Get("Location"), "owner still has access")
			}
		})
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
