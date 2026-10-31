//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// Finding H-1: the burn-after-reading guarantee is enforced only when content negotiation picks
// HTML (the reveal interstitial), so any read that does not negotiate HTML destroys the note or
// bypasses burn entirely. These tests describe the DESIRED behaviour and fail until H-1 is fixed.

const burnGuaranteeContent = "h1-burn-note-payload-7f3a"

// browserImageAccept is what Chromium sends for an <img> subresource; it ends in */*.
const browserImageAccept = "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"

var imgSrcPattern = regexp.MustCompile(`<img[^>]*\ssrc="([^"]*)"`)

// newBurnGuaranteeEnv wires the stack the way serverAction does it with --enable-accounts:
// the reveal-token store is attached (it always is in production), so the HTML interstitial is
// active and any remaining bypass is a real one.
func newBurnGuaranteeEnv(t *testing.T, dbName string) ownerFlowEnv {
	t.Helper()

	db, err := sqliterepo.Open("file:"+dbName+"?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = sqliterepo.Migrate(t.Context(), db)
	require.NoError(t, err)

	log := slog.New(slog.DiscardHandler)
	users := sqliterepo.NewUserRepository(db)

	authMgr, err := auth.NewManager(
		auth.Stores{
			Users:     users,
			Invites:   sqliterepo.NewInviteRepository(db, log),
			Sessions:  sqliterepo.NewSessionRepository(db, log),
			APITokens: sqliterepo.NewAPITokenRepository(db),
		},
		crypto.NewPasswordHasher(testArgon2Params),
		crypto.NewSealer(testArgon2Params),
		crypto.NewTOTP(),
		log,
		"padmark-test",
		time.Hour,
	)
	require.NoError(t, err)

	notesMgr, linksMgr := newOwnerFlowManagers(t, db, log)

	handler := web.NewHandler(notesMgr, linksMgr, log).
		WithRevealStore(sqliterepo.NewRevealRepository(db, log)).
		WithAuthManager(authMgr)
	ogen := web.NewOgenHandler(notesMgr, linksMgr, web.NoPinger{}, log)
	router, err := web.NewRouter(handler, ogen, &web.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   []byte("padmark-test-csrf-secret-32bytes"),
		// The burn-link flows create their links anonymously and follow them with a 301; anonymous
		// links get the interstitial by default, so this models an operator running
		// --direct-redirect=true.
		DirectRedirect: true,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DisableAPI: false,
	})
	require.NoError(t, err)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return ownerFlowEnv{db: db, authMgr: authMgr, users: users, baseURL: srv.URL}
}

// burnCreateNote creates a note through the public JSON API and returns its slug.
func burnCreateNote(t *testing.T, env ownerFlowEnv, bearer, body string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.baseURL+"/notes",
		strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	var created struct {
		ID string `json:"id"`
	}

	require.NoError(t, json.Unmarshal(raw, &created))
	require.NotEmpty(t, created.ID)

	return created.ID
}

// burnHTTPGet issues a GET exactly as given — no implicit Accept — and never follows redirects.
func burnHTTPGet(t *testing.T, url string, headers map[string]string, sessionID string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	if sessionID != "" {
		req.Header.Set("Cookie", "padmark_session="+sessionID)
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     nil, Jar: nil, Timeout: 0,
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(raw)
}

// burnNoteCount counts stored rows for slug matching an optional extra condition.
func burnNoteCount(t *testing.T, env ownerFlowEnv, slug, condition string) int {
	t.Helper()

	query := env.db.NewSelect().Table("notes").Where("id = ?", domain.HashSlug(slug))
	if condition != "" {
		query = query.Where(condition)
	}

	count, err := query.Count(t.Context())
	require.NoError(t, err)

	return int(count)
}

// burnOwnerSession bootstraps the first admin and returns a browser session ID and an API token.
func burnOwnerSession(t *testing.T, env ownerFlowEnv) (sessionID, apiToken string) {
	t.Helper()

	ctx := t.Context()

	_, err := env.authMgr.AcceptFirstAdmin(ctx, "owner", ownerFlowPassword)
	require.NoError(t, err)

	owner, err := env.users.GetByUsername(ctx, "owner")
	require.NoError(t, err)

	rawSecret, _, err := crypto.NewSealer(testArgon2Params).
		Open(owner.TOTPSecret, []byte(ownerFlowPassword), owner.KDFSalt)
	require.NoError(t, err)

	code, err := totp.GenerateCode(rawSecret, time.Now())
	require.NoError(t, err)

	sessionID, err = env.authMgr.Login(ctx, "owner", ownerFlowPassword, code, "Mozilla/5.0", "127.0.0.1", "")
	require.NoError(t, err)

	apiToken, err = env.authMgr.CreateAPIToken(ctx, owner.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	return sessionID, apiToken
}

// TestBurnGuarantee_NonExplicitReadDoesNotConsume — H-1 bypass 1. Link unfurlers, mail
// security gateways and plain HTTP libraries send no Accept or Accept: */*. negotiate()
// maps both to JSON, the interstitial is skipped, and GET returns the plaintext while
// storage.Consume destroys the note: the recipient then sees 404. A read that does not
// explicitly ask for the note must neither receive the content nor consume it.
func TestBurnGuarantee_NonExplicitReadDoesNotConsume(t *testing.T) {
	env := newBurnGuaranteeEnv(t, "burn-guarantee-non-explicit")

	for _, testCase := range []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{"no Accept header (Go net/http, most libraries)", "/", map[string]string{}},
		{"curl default", "/", map[string]string{"Accept": "*/*", "User-Agent": "curl/8.7.1"}},
		{"chat link unfurler", "/", map[string]string{
			"Accept": "*/*", "User-Agent": "Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)",
		}},
		{"mail link scanner on /notes/{id}", "/notes/", map[string]string{
			"Accept": "*/*", "User-Agent": "Mozilla/5.0 (compatible; SafeLinks)",
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			slug := burnCreateNote(t, env, "",
				`{"content":"`+burnGuaranteeContent+`","burn_after_reading":true}`)

			status, body := burnHTTPGet(t, env.baseURL+testCase.path+slug, testCase.headers, "")

			assert.NotContains(t, body, burnGuaranteeContent,
				"a non-explicit read (status %d) must not receive the burn note's content", status)
			assert.Equal(t, 1, burnNoteCount(t, env, slug, ""),
				"a non-explicit read must not consume the burn note; the recipient would get 404")
		})
	}
}

// TestBurnGuarantee_ImageSubresourceDoesNotTriggerBurn — H-1 bypass 2 (read-CSRF). Raw HTML
// is allowed in markdown, bluemonday's UGC policy keeps a relative <img src> and the CSP allows
// img-src 'self', so an attacker's note can embed <img src="/<victim-slug>">. Every viewer's
// browser then fetches the victim note as a same-origin image (Accept ends in */*, so JSON is
// negotiated) with its SameSite=Strict session cookie attached, and the burn fires — even on an
// owner-only note the attacker can never read. Either the chain is broken (no same-origin
// <img> survives rendering) or such a fetch must have no read side effect.
func TestBurnGuarantee_ImageSubresourceDoesNotTriggerBurn(t *testing.T) {
	env := newBurnGuaranteeEnv(t, "burn-guarantee-img-csrf")
	ownerSession, ownerToken := burnOwnerSession(t, env)

	for _, testCase := range []struct {
		name          string
		bearer        string
		fields        string
		viewerSession string
		// intact is the SQL condition the victim row must still satisfy after the attack.
		intact string
	}{
		{
			name:   "public immediate-burn note, anonymous viewer",
			fields: `"burn_after_reading":true`,
			intact: "burn_after_reading",

			bearer: "", viewerSession: "",
		},
		{
			name:          "owner-only immediate-burn note, owner's own browser",
			bearer:        ownerToken,
			fields:        `"burn_after_reading":true,"privacy":"owner"`,
			viewerSession: ownerSession,
			intact:        "burn_after_reading",
		},
		{
			name:   "grace-period burn note: timer must not start",
			fields: `"burn_after_reading":true,"ttl":3600`,
			intact: "burn_after_reading AND expires_at IS NULL",

			bearer: "", viewerSession: "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			victim := burnCreateNote(t, env, testCase.bearer,
				`{"content":"`+burnGuaranteeContent+`",`+testCase.fields+`}`)

			if testCase.viewerSession != "" {
				// Precondition: the attacker cannot read the owner-only note itself.
				status, body := burnHTTPGet(t, env.baseURL+"/"+victim,
					map[string]string{"Accept": "application/json"}, "")
				require.NotEqual(t, http.StatusOK, status)
				require.NotContains(t, body, burnGuaranteeContent)
			}

			attacker := burnCreateNote(t, env, "", `{"title":"cute cat","content":"look ![cat](/`+victim+`)"}`)

			// The viewer's browser navigates to the attacker's note ...
			status, page := burnHTTPGet(t, env.baseURL+"/"+attacker, map[string]string{
				"Accept":         "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
				"Sec-Fetch-Dest": "document",
				"Sec-Fetch-Mode": "navigate",
				"Sec-Fetch-Site": "none",
			}, testCase.viewerSession)
			require.Equal(t, http.StatusOK, status, page)

			// ... and loads every same-origin image the rendered note embeds.
			for _, match := range imgSrcPattern.FindAllStringSubmatch(page, -1) {
				src := match[1]
				if !strings.HasPrefix(src, "/") || strings.HasPrefix(src, "//") {
					continue
				}

				burnHTTPGet(t, env.baseURL+src, map[string]string{
					"Accept":         browserImageAccept,
					"Sec-Fetch-Dest": "image",
					"Sec-Fetch-Mode": "no-cors",
					"Sec-Fetch-Site": "same-origin",
					"Referer":        env.baseURL + "/" + attacker,
				}, testCase.viewerSession)
			}

			assert.Equal(t, 1, burnNoteCount(t, env, victim, testCase.intact),
				"viewing someone else's note must not burn (or start the burn timer of) the victim note")
		})
	}
}

// TestBurnGuarantee_EditPageHonoursBurnAndExpiry — H-1 bypass 3. GET /edit/{id} loads the note
// with Manager.Peek (no burn, no expiry policy) and pre-fills the editor with the plaintext, so
// anyone holding a burn note's URL reads it any number of times via /edit/<slug>, and an expired
// note stays readable there until something hits the view route (no sweeper).
func TestBurnGuarantee_EditPageHonoursBurnAndExpiry(t *testing.T) {
	env := newBurnGuaranteeEnv(t, "burn-guarantee-edit-page")
	browser := map[string]string{"Accept": "text/html,application/xhtml+xml,*/*;q=0.8"}

	t.Run("immediate-burn note is not readable via the editor", func(t *testing.T) {
		slug := burnCreateNote(t, env, "", `{"content":"`+burnGuaranteeContent+`","burn_after_reading":true}`)

		for attempt := range 2 {
			_, body := burnHTTPGet(t, env.baseURL+"/edit/"+slug, browser, "")
			assert.NotContains(t, body, burnGuaranteeContent,
				"editor read #%d must not pre-fill a burn note's content", attempt+1)
		}
	})

	t.Run("expired note is gone, not served by the editor", func(t *testing.T) {
		slug := burnCreateNote(t, env, "", `{"content":"`+burnGuaranteeContent+`"}`)

		_, err := env.db.NewUpdate().Table("notes").
			Set("expires_at = ?", time.Now().Add(-time.Hour)).
			Where("id = ?", domain.HashSlug(slug)).
			Exec(t.Context())
		require.NoError(t, err)

		status, body := burnHTTPGet(t, env.baseURL+"/edit/"+slug, browser, "")

		assert.Equal(t, http.StatusGone, status, "an expired note must answer 410 on the editor route")
		assert.NotContains(t, body, burnGuaranteeContent, "an expired note's content must not be served")
		assert.Equal(t, 0, burnNoteCount(t, env, slug, ""), "an expired note must be deleted on access")
	})
}

// burnCreateLink creates a burn-after-click link through the JSON API and returns its slug.
func burnCreateLink(t *testing.T, env ownerFlowEnv, target string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.baseURL+"/links",
		strings.NewReader(`{"target_url":"`+target+`","burn_after_click":true,"direct_redirect":true}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	var created struct {
		ID string `json:"id"`
	}

	require.NoError(t, json.Unmarshal(raw, &created))
	require.NotEmpty(t, created.ID)

	return created.ID
}

// burnLinkCount counts stored rows of the link with the given slug.
func burnLinkCount(t *testing.T, env ownerFlowEnv, slug string) int {
	t.Helper()

	count, err := env.db.NewSelect().Table("links").Where("id = ?", domain.HashSlug(slug)).Count(t.Context())
	require.NoError(t, err)

	return int(count)
}

// burnHTTPDo is burnHTTPGet for an arbitrary method, returning the Location header as well.
func burnHTTPDo(t *testing.T, method, url string, headers map[string]string) (int, string, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	require.NoError(t, err)

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     nil, Jar: nil, Timeout: 0,
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, resp.Header.Get("Location"), string(raw)
}

// TestBurnGuarantee_LinkRedirectRequiresExplicitIntent — the H-1 class on short links: GET
// /r/{id} consumes a burn_after_click link on any request, so a chat unfurler, a mail scanner, a
// HEAD probe or an <img src="/r/<slug>"> in someone else's note follows (and destroys) it before
// the recipient does. Only a deliberate navigation may follow a burn link.
func TestBurnGuarantee_LinkRedirectRequiresExplicitIntent(t *testing.T) {
	env := newBurnGuaranteeEnv(t, "burn-guarantee-link-redirect")
	target := "https://example.com/h1-burn-link-target"

	for _, testCase := range []struct {
		name    string
		method  string
		headers map[string]string
	}{
		{"no Accept header", http.MethodGet, map[string]string{}},
		{"curl default", http.MethodGet, map[string]string{"Accept": "*/*", "User-Agent": "curl/8.7.1"}},
		{"chat link unfurler", http.MethodGet, map[string]string{
			"Accept": "*/*", "User-Agent": "Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)",
		}},
		{"HEAD probe", http.MethodHead, map[string]string{"Accept": "text/html"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			slug := burnCreateLink(t, env, target)

			_, location, body := burnHTTPDo(t, testCase.method, env.baseURL+"/r/"+slug, testCase.headers)

			assert.NotContains(t, location+body, target, "an implicit request must not learn the target")
			assert.Equal(t, 1, burnLinkCount(t, env, slug), "an implicit request must not consume the burn link")
		})
	}

	t.Run("img subresource from another note", func(t *testing.T) {
		slug := burnCreateLink(t, env, target)
		attacker := burnCreateNote(t, env, "", `{"content":"look ![cat](/r/`+slug+`)"}`)

		status, page := burnHTTPGet(t, env.baseURL+"/"+attacker, map[string]string{
			"Accept": "text/html", "Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate",
		}, "")
		require.Equal(t, http.StatusOK, status, page)

		for _, match := range imgSrcPattern.FindAllStringSubmatch(page, -1) {
			if src := match[1]; strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "//") {
				burnHTTPDo(t, http.MethodGet, env.baseURL+src, map[string]string{
					"Accept": browserImageAccept, "Sec-Fetch-Dest": "image", "Sec-Fetch-Mode": "no-cors",
				})
			}
		}

		assert.Equal(t, 1, burnLinkCount(t, env, slug), "an embedded image must not consume the burn link")
	})

	t.Run("deliberate navigation still follows and burns", func(t *testing.T) {
		slug := burnCreateLink(t, env, target)

		status, location, _ := burnHTTPDo(t, http.MethodGet, env.baseURL+"/r/"+slug, map[string]string{
			"Accept":         "text/html,application/xhtml+xml,*/*;q=0.8",
			"Sec-Fetch-Dest": "document",
			"Sec-Fetch-Mode": "navigate",
		})

		require.Equal(t, http.StatusMovedPermanently, status)
		require.Equal(t, target, location)
		require.Equal(t, 0, burnLinkCount(t, env, slug), "following a burn link consumes it")
	})
}
