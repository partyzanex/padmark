//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	"github.com/partyzanex/padmark/internal/infra/lock"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

// pausedLinkRead lets a separate manager commit an update after the reader fetched its row.
// Neither manager shares a process-local slug lock or target cache with the other.
type pausedLinkRead struct {
	links.Storage

	once    sync.Once
	read    chan struct{}
	proceed chan struct{}
}

func (s *pausedLinkRead) GetByID(ctx context.Context, id string) (*domain.Link, error) {
	link, err := s.Storage.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	s.once.Do(func() {
		close(s.read)

		select {
		case <-s.proceed:
		case <-ctx.Done():
		}
	})

	return link, ctx.Err()
}

func TestLinkRedirectPrivacyRace(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		burn     bool
		implicit bool
	}{
		{
			name: "ordinary",
			burn: false, implicit: false,
		},
		{
			name: "burn", burn: true,
			implicit: false,
		},
		{
			name: "preview", implicit: true,
			burn: false,
		},
		{name: "burn_preview", burn: true, implicit: true},
	} {
		for _, cached := range []bool{false, true} {
			name := scenario.name + "/uncached"
			if cached {
				name = scenario.name + "/cached"
			}

			t.Run(name, func(t *testing.T) {
				env := newOwnerFlowEnv(t, strings.ReplaceAll(t.Name(), "/", "_"))
				_, err := env.authMgr.AcceptFirstAdmin(t.Context(), "owner", ownerFlowPassword)
				require.NoError(t, err)
				owner, err := env.users.GetByUsername(t.Context(), "owner")
				require.NoError(t, err)
				key, err := env.authMgr.CreateAPIToken(t.Context(), owner.ID, auth.SessionIssuer(), 0)
				require.NoError(t, err)

				log := slog.New(slog.DiscardHandler)
				notes, writer := newOwnerFlowManagers(t, env.db, log)
				publicURL, privateURL := "https://example.com/public", "https://example.com/private?secret=hidden"
				link, err := writer.Create(t.Context(), publicURL, links.CreateOptions{
					OwnerID: &owner.ID, BurnAfterClick: scenario.burn,

					ExpiresAt: nil, Privacy: nil, DirectRedirect: nil, CustomHash: "",
					EditCode: "",
				})
				require.NoError(t, err)

				repo := sqliterepo.NewLinkRepository(env.db, log)
				paused := &pausedLinkRead{
					Storage: repo, read: make(chan struct{}), proceed: make(chan struct{}),
					once: sync.Once{},
				}

				reader := links.NewManager(paused, sqliterepo.NewNoteRepository(env.db), lock.NewLocker(),
					testutil.ContentEncryptor(t, "link"), crypto.NewEditCodeHasher(testArgon2Params), log, false)
				if cached {
					cache := linkcache.NewDefault(log)
					stored, getErr := repo.GetByID(t.Context(), domain.HashSlug(link.ID))
					require.NoError(t, getErr)
					cache.Set(t.Context(), stored.ID, stored.TargetURL, publicURL)
					reader.WithCache(cache)
				}

				handler := web.NewHandler(notes, reader, log).WithAuthManager(env.authMgr)
				ogen := web.NewOgenHandler(notes, reader, web.NoPinger{}, log)
				router, err := web.NewRouter(handler, ogen, &web.RouterOptions{
					MaxBodyBytes: 256 * 1024, DirectRedirect: true,

					ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, CSRFSecret: nil,
					AllowedHosts: nil, RateLimit: 0, RateBurst: 0, SessionTTL: 0,
					DisableAPI: false,
				})
				require.NoError(t, err)

				server := httptest.NewServer(router)
				t.Cleanup(server.Close)

				var release sync.Once

				resume := func() { release.Do(func() { close(paused.proceed) }) }
				t.Cleanup(resume)

				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/r/"+link.ID, nil)
				require.NoError(t, err)
				request.Header.Set("Accept", "application/json")

				if scenario.implicit {
					request.Header.Set("Accept", "*/*")
				}

				client := &http.Client{
					Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
						return http.ErrUseLastResponse
					},
					Transport: nil, Jar: nil,
				}
				outcome := make(chan redirectResult, 1)

				go func() { outcome <- doRedirect(client, request) }()

				select {
				case <-paused.read:
				case <-time.After(10 * time.Second):
					t.Fatal("redirect did not reach the initial row read")
				}

				_, err = writer.Update(t.Context(), link.ID, "", owner.ID, privateURL,
					links.UpdateOptions{
						Privacy:   new(domain.PrivacyOwner),
						ExpiresAt: nil, DirectRedirect: nil, ClearExpiry: false,
						ResetDirectRedirect: false,
					})
				require.NoError(t, err)
				resume()

				result := <-outcome
				require.NoError(t, result.err)
				require.NotContains(t, result.header.Get("Location"), privateURL)
				require.NotContains(t, string(result.body), privateURL)
				require.GreaterOrEqual(t, result.status, http.StatusBadRequest)
				current, err := repo.GetByID(t.Context(), domain.HashSlug(link.ID))
				require.NoError(t, err, "the unauthorized request must not consume the new private row")
				require.Zero(t, current.Views, "the new private row must not receive an anonymous click")

				request.Header.Set("Authorization", "Bearer "+key)
				request.Header.Set("Accept", "application/json")
				resp, err := client.Do(request)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
				require.Equal(t, privateURL, resp.Header.Get("Location"), "the owner must retain access")

				if scenario.burn {
					_, err = repo.GetByID(t.Context(), domain.HashSlug(link.ID))
					require.ErrorIs(t, err, domain.ErrNotFound, "the owner consumes the burn link")
				}
			})
		}
	}
}

// redirectResult is a read and closed response, so the request goroutine can hand it to the test.
type redirectResult struct {
	header http.Header
	err    error
	body   []byte
	status int
}

func doRedirect(client *http.Client, request *http.Request) redirectResult {
	response, err := client.Do(request)
	if err != nil {
		return redirectResult{header: nil, err: err, body: nil, status: 0}
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()

	return redirectResult{
		header: response.Header, err: errors.Join(readErr, closeErr), body: body, status: response.StatusCode,
	}
}
