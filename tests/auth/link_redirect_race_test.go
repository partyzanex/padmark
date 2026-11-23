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

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

// pausedLinkRead lets a separate manager commit an update after the reader fetched its row.
// Neither manager shares a process-local target cache with the other.
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

func (s *StackFlowSuite) TestLinkRedirectPrivacyRace() {
	t := s.T()

	for _, scenario := range []redirectScenario{
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

			t.Run(name, func(t *testing.T) { runRedirectRace(t, scenario, cached) })
		}
	}
}

// The public target of the race link and the private target that the owner sets in the race.
const (
	racePublicURL  = "https://example.com/public"
	racePrivateURL = "https://example.com/private?secret=hidden"
)

// redirectScenario is one link of TestLinkRedirectPrivacyRace: burn-after-click, and an
// implicit (preview) request instead of an explicit one.
type redirectScenario struct {
	name     string
	burn     bool
	implicit bool
}

// raceStack is the stack of one redirect race: the manager that changes the link, a second
// manager that reads it through the paused storage, and the server of the second manager.
type raceStack struct {
	writer  *links.Manager
	repo    *sqliterepo.LinkRepository
	paused  *pausedLinkRead
	server  *httptest.Server
	link    *domain.Link
	ownerID uuid.UUID
	key     string
}

// runRedirectRace starts a redirect, makes the link private to the owner after the redirect
// read the row, and checks that the redirect does not give the private target.
func runRedirectRace(t *testing.T, scenario redirectScenario, cached bool) {
	t.Helper()

	race := newRaceStack(t, scenario.burn, cached)

	var release sync.Once

	resume := func() { release.Do(func() { close(race.paused.proceed) }) }
	t.Cleanup(resume)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, race.server.URL+"/r/"+race.link.ID, nil)
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
	case <-race.paused.read:
	case <-time.After(10 * time.Second):
		t.Fatal("redirect did not reach the initial row read")
	}

	ownerReader := domain.Reader{CallerID: race.ownerID, Authenticated: true, AllowBurn: false, SkipPrivacy: false}
	_, err = race.writer.Update(t.Context(), race.link.ID, "", ownerReader, racePrivateURL,
		links.UpdateOptions{
			Privacy:   new(domain.PrivacyOwner),
			ExpiresAt: nil, DirectRedirect: nil, ClearExpiry: false,
			ResetDirectRedirect: false,
		})
	require.NoError(t, err)
	resume()

	checkRaceResult(t, race, request, client, <-outcome, scenario.burn)
}

// newRaceStack builds the stack of one race and creates the public link of the owner.
func newRaceStack(t *testing.T, burn, cached bool) *raceStack {
	t.Helper()

	env := newOwnerFlowEnv(t, strings.ReplaceAll(t.Name(), "/", "_"))
	_, err := env.authMgr.AcceptFirstAdmin(t.Context(), "owner", ownerFlowPassword)
	require.NoError(t, err)
	owner, err := env.users.GetByUsername(t.Context(), "owner")
	require.NoError(t, err)
	key, err := env.authMgr.CreateAPIToken(t.Context(), owner.ID, auth.SessionIssuer(), 0)
	require.NoError(t, err)

	log := slog.New(slog.DiscardHandler)
	writers := testenv.NewManagers(t, env.db, testenv.WithLinkCache())
	writer := writers.Links
	link, err := writer.Create(t.Context(), racePublicURL, links.CreateOptions{
		OwnerID: &owner.ID, BurnAfterClick: burn,

		ExpiresAt: nil, Privacy: nil, DirectRedirect: nil, CustomHash: "",
		EditCode: "",
	})
	require.NoError(t, err)

	repo := sqliterepo.NewLinkRepository(env.db, log)
	paused := &pausedLinkRead{
		Storage: repo, read: make(chan struct{}), proceed: make(chan struct{}),
		once: sync.Once{},
	}

	reader := links.NewManager(&links.Deps{
		Storage: paused, Encryptor: testutil.ContentEncryptor(t, "link"),
		Hasher:       crypto.NewEditCodeHasher(testArgon2Params, slog.New(slog.DiscardHandler)),
		ExpiredSlugs: expiredslugs.New(sqliterepo.NewNoteRepository(env.db), repo), Log: log,
		Now: time.Now,
	}, false)
	if cached {
		cache := linkcache.NewDefault(log)
		stored, getErr := repo.GetByID(t.Context(), domain.HashSlug(link.ID))
		require.NoError(t, getErr)
		cache.Set(t.Context(), stored.ID, stored.TargetURL, racePublicURL)
		reader.WithCache(cache)
	}

	handlerCfg := testenv.HandlerConfig(writers.Notes, reader)
	handlerCfg.Accounts = env.authMgr
	handlerCfg.DirectRedirect = true
	server := testenv.Serve(t, handlerCfg)

	return &raceStack{
		writer: writer, repo: repo, paused: paused, server: server, link: link, ownerID: owner.ID, key: key,
	}
}

// checkRaceResult checks the answer of the raced redirect and then the access of the owner.
func checkRaceResult(
	t *testing.T, race *raceStack, request *http.Request, client *http.Client, result redirectResult, burn bool,
) {
	t.Helper()

	require.NoError(t, result.err)
	require.NotContains(t, result.header.Get("Location"), racePrivateURL)
	require.NotContains(t, string(result.body), racePrivateURL)
	require.GreaterOrEqual(t, result.status, http.StatusBadRequest)
	current, err := race.repo.GetByID(t.Context(), domain.HashSlug(race.link.ID))
	require.NoError(t, err, "the unauthorized request must not consume the new private row")
	require.Zero(t, current.Views, "the new private row must not receive an anonymous click")

	request.Header.Set("Authorization", "Bearer "+race.key)
	request.Header.Set("Accept", "application/json")
	resp, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
	require.Equal(t, racePrivateURL, resp.Header.Get("Location"), "the owner must retain access")

	if burn {
		_, err = race.repo.GetByID(t.Context(), domain.HashSlug(race.link.ID))
		require.ErrorIs(t, err, domain.ErrNotFound, "the owner consumes the burn link")
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
