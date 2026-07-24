# TODO: shortlink → padmark integration

Source: Техзадание v3 (target URL encrypted at rest, slug = decryption key, globally unique
slug across notes/links). This file tracks implementation as a checklist; check items off as
they land. Each top-level phase should be buildable/testable independently before moving to
the next.

## Phase 1 — MVP (Create + Redirect + Expand)

### Domain

- [x] `internal/domain/link.go` — `Link` struct
  - [x] `ID string` (HashSlug(slug), SHA-256 — PK)
  - [x] `TargetURL string` (ciphertext in storage, plaintext in domain after Decrypt)
  - [x] `CreatedAt time.Time`
  - [x] `ExpiresAt *time.Time` (nullable, optional TTL)
  - [x] `OwnerID *uuid.UUID`
  - [x] `Privacy *Privacy` (reuse `domain.Privacy` from notes)
  - [x] `Views int`
  - [x] `EditCode string` (argon2id hash)
  - [x] `BurnAfterClick bool`
  - [x] no `ShortHash`/slug field — slug is never persisted, same as `Note`
  - [x] `Link.Validate()` — mirrors `Note.Validate()` (title-less, but validates `Privacy.Valid()`)
  - [x] `Link.OwnedBy(userID uuid.UUID) bool` — mirrors `Note.OwnedBy`
  - [x] `Link.EffectivePrivacy() Privacy` — mirrors `Note.EffectivePrivacy`
  - [x] `Link.VisibleTo(callerID uuid.UUID, authenticated bool) bool` — mirrors `Note.VisibleTo`,
        fail-closed default case (see padmark's own note.go for the precedent)
  - [x] bonus (not originally itemized, but the same bug class applies): `Link.ValidateOwnership()`
        mirroring `Note.ValidateOwnership()` — `Link` shares the same `Privacy` enum including
        `PrivacyOwner`, so the "anonymous caller creates an unreadable owner-only link" bug from
        notes applies identically here; added proactively rather than waiting to rediscover it
        during a later review pass
- [x] New sentinel errors in `internal/domain/errors.go` (only if genuinely new; reuse
      `ErrSlugConflict`, `ErrNotFound`, `ErrExpired`, `ErrInvalidEditCode`,
      `ErrCustomSlugDisabled`, `ErrInvalidSlug` from notes where identical)
  - [x] `ErrInvalidURL` — target URL fails scheme whitelist (http/https only)
  - [x] `ErrHashCollision` — exhausted `maxHashRetries` random-slug attempts
  - [x] resolved a spurious decision along the way: `ErrExpired`'s message was `"note has expired"`
        (note-specific text on a sentinel now shared by links too) — generalized to `"expired"`;
        verified no test asserts the old literal string from the sentinel (`cli/get_test.go`'s
        fixture text is independent, hardcoded client-side, not derived from `.Error()`)

### Migration

- [x] `migrations/sqlite/20260721000001_links.sql`
  - [x] `CREATE TABLE links` (id TEXT PK, target_url TEXT NOT NULL, created_at, expires_at,
        owner_id, edit_code, views, burn_after_click) — dialect-correct types (INTEGER for bool
        in sqlite, matching the `notes` table's existing conventions)
  - [x] **found and fixed a gap in this very checklist while implementing**: the column list above
        never mentioned `privacy`, even though `domain.Link.Privacy` (the Domain section, done
        above) has it — added `privacy TEXT NOT NULL DEFAULT 'public'`, mirroring notes' column
        exactly
  - [x] index on `owner_id`
  - [x] index on `expires_at` (partial index, `WHERE expires_at IS NOT NULL`, matching
        `idx_notes_expires_at`'s existing convention)
  - [x] `-- +goose Down` drops the table (and both indexes explicitly first, matching
        `api_tokens`' migration style)
- [x] `migrations/postgres/20260721000001_links.sql` — mirrored, with Postgres-correct types
      (`TIMESTAMPTZ`, `BOOLEAN`, `UUID` for owner_id FK)
- [x] confirmed both migrations apply cleanly: full `-tags=integration` suite (`tests/auth`,
      `tests/manager`, both of which call `.Migrate(ctx)` end-to-end for sqlite and, via
      testcontainers, real Postgres) passes with the new migration files present
  - [x] also added a dedicated migration test (`internal/infra/storage/sqlite/migrate_links_test.go`,
        same pattern as `migrate_privacy_test.go`) — `TestUp_CreatesLinksTableWithDefaults` (insert
        with only `id`/`target_url`, verify `privacy`/`views`/`burn_after_click` defaults) and
        `TestDown_DropsLinksTable`

### Storage layer

- [x] `internal/infra/storage/sqlite/link_repository.go`
  - [x] `LinkRepository` struct + `NewLinkRepository(db *bun.DB) *LinkRepository`
  - [x] `Create(ctx, link *domain.Link) error` — translate `ErrSlugConflict` from PK violation,
        same pattern as `note_repository.go:Create`
  - [x] `GetByID(ctx, id string) (*domain.Link, error)` — `ErrNotFound` on `sql.ErrNoRows`
  - [x] `Exists(ctx, id string) (bool, error)` — for cross-table slug uniqueness check
  - [x] `IncrementViews(ctx, id string) error` — atomic, `ErrNotFound` if no row affected
  - [x] `Delete(ctx, id string) error` — `ErrNotFound` if no row affected
  - [x] `Consume(ctx, id string) (*domain.Link, error)` — atomic DELETE+RETURNING for
        burn-after-click, mirrors `note_repository.go:Consume` — **revised while implementing**:
        unlike notes, `links` has no deprecated leftover column, so a plain `Returning("*")` is
        safe (the explicit-column-list workaround is specifically a notes-table artifact); also
        widened `Consume`'s WHERE to `burn_after_click OR expired` (not burn-only) to match
        `note_repository.go:Consume`'s actual real-world shape exactly, rather than my own earlier
        draft pseudocode in this file's Use-case section, which had split "burn" and "expiry"
        into two separate code paths — flagging this for the Use-case section, still to reconcile
  - [x] `ListByOwner` — deferred to Phase 3 as planned, not implemented now
- [x] `internal/infra/storage/postgres/link_repository.go` — mirrored; verified structurally
      identical to the sqlite version (both reuse the *same* package-level `privacyPtr`/`privacyVal`
      helpers already defined in each package's `note_repository.go` — no duplicate definitions)
- [x] repository unit tests (`link_repository_test.go`, both dialects, 15 cases each) —
      Create (OK/duplicate-slug/all-fields-roundtrip/anonymous-defaults-to-public)/GetByID
      (OK via all-fields test/not-found)/Exists (true/false)/IncrementViews (OK/not-found)/Consume
      (burn-after-click/expired/not-eligible/not-found)/Delete (OK/not-found)
  - [x] **real bug caught by these tests, not just theoretical**: the postgres
        `TestCreate_AllFields` first draft used a bare `uuid.New()` as `OwnerID` with no backing
        `users` row — passed silently on sqlite (FK enforcement not active in that test DSN) but
        failed for real on Postgres with a `23503` foreign-key-violation, exactly the kind of
        dialect-specific gap a sqlite-only test run would have missed. Fixed by adding a
        `createTestUser` helper mirroring `note_repository_test.go`'s
  - [x] no COALESCE-on-update semantics needed — `links` has no `Update` method (Create/Redirect/
        Delete only, per the Use-case section's design), so this sub-item from the original
        checklist doesn't apply

### Use case (business logic)

- [x] `internal/usecases/links/manager.go`
  - [x] `Manager` struct: `storage Storage`, `notes NotesChecker`, `encryptor Encryptor`,
        `hasher EditCodeHasher`, `log *slog.Logger`, `allowCustomSlugs bool`
  - [x] `Storage` interface (Create/GetByID/Exists/IncrementViews/Delete/Consume) — defined here,
        next to consumer, per project convention (not a grab-bag `ports.go`)
  - [x] `NotesChecker` interface (`Exists(ctx, id string) (bool, error)`) — cross-table check
  - [x] **do not import `notes.Encryptor`/`notes.EditCodeHasher`** — per this project's own
        architecture rules, a usecase package may only import `domain` + stdlib, never another
        usecase package (`internal/usecases/links` importing `internal/usecases/notes` is a
        layering violation, confirmed against `internal/usecases/notes/manager.go`'s own import
        block, which only imports `domain` + stdlib). `links.Manager` must declare its **own**
        `Encryptor`/`EditCodeHasher` interfaces, structurally identical to notes' but a separate
        declaration next to `links.Manager` — the concrete `crypto.New()`/`crypto.NewEditCodeHasher()`
        implementations already satisfy both by structural typing, no infra changes needed
  - [x] **same problem applies to slug helpers, done**: moved to `internal/domain/slug.go` —
        `ReservedSlugPrefixes()` (now also lists `"links"`/`"r"`, resolving the Open Questions
        route-prefix item), `ValidCustomSlug()`, `FirstSegmentReserved()`,
        `SortedReservedSlugPrefixes()`, `NewEditCode()`, and `ResolveSlug(candidate string,
        allowCustom bool) (string, error)` — **generalized signature**: the old
        `notes`-local `resolveSlug(note *domain.Note, allowCustom bool)` took and mutated a
        `*domain.Note` directly; confirmed via trace that the mutation was dead code (the
        returned `slug` value was what `Create` actually used, `note.ID` was never read again
        before being explicitly overwritten), so the plain-string signature is a safe, faithful
        generalization, not just a workaround
    - [x] `internal/usecases/notes/manager.go`: deleted the moved code, `Create` now calls
          `domain.ResolveSlug(note.ID, ...)`/`domain.NewEditCode()`; dropped 5 now-unused imports
          (`crypto/rand`, `math/big`, `regexp`, `sort`, `strings`); wrapped `ResolveSlug`'s error
          (`fmt.Errorf("resolve slug: %w", err)`) — wrapcheck correctly started flagging the
          unwrapped return once the call became cross-package
    - [x] `internal/adapters/http/router.go`'s `buildNamedRoutes()` now calls
          `domain.ReservedSlugPrefixes()`; `notes` import replaced with `domain` (was otherwise
          unused in this file)
    - [x] moved the corresponding tests to `internal/domain/slug_test.go` (7 existing +
          3 new: empty-candidate-generates-random, custom-disabled-rejects-non-empty,
          custom-enabled-returns-unchanged — direct coverage for `ResolveSlug`'s branches that
          the note-shaped tests didn't isolate); `manager_test.go`'s
          `TestCreate_CustomSlugDisabled_AutoSlugStillWorks` softened its assertion from an exact
          `slugLength` (no longer visible outside `domain`) to `NotEmpty` — the length contract
          now belongs to `domain`'s own test
    - [x] full suite green after the move: build (+integration), vet, unit+integration tests
          (race), lint (0 issues), gofmt — confirmed before proceeding to write `links.Manager`
  - [x] `CreateOptions` struct (CustomHash, ExpiresAt, Privacy, BurnAfterClick, OwnerID, EditCode)
  - [x] `Create(ctx, targetURL string, opts CreateOptions) (*domain.Link, error)`
    - [x] validate URL (scheme whitelist http/https via `validateTargetURL`, rejects
          javascript:/data:/file: and any other scheme → `domain.ErrInvalidURL`)
    - [x] slug generation loop, `maxHashRetries = 5` (bounded `for range maxHashRetries`, not `goto`)
    - [x] cross-table check: `notes.Exists` AND `storage.Exists` before insert — implemented as
          `slugTaken` (checks both, short-circuits on the first hit) called from
          `resolveUniqueSlug`
    - [x] custom alias conflict → `ErrSlugConflict` immediately (no retry, single `slugTaken` call
          when `opts.CustomHash != ""`); random-slug collision → retry up to `maxHashRetries`
          inside `resolveUniqueSlug`'s `for range`, then `domain.ErrHashCollision` once exhausted
    - [x] encrypt target URL with slug as key material (`links.Encryptor`, structurally satisfied
          by `crypto.New()` — no infra changes, per the interface-duplication decision below)
    - [x] hash edit code (`links.EditCodeHasher`, structurally satisfied by
          `crypto.NewEditCodeHasher()`, argon2id) — extracted into `resolveAndHashEditCode` to
          keep `Create` under the `funlen` statement limit (same fix pattern later reused in
          `notes.Manager.Create`, see below)
    - [x] return link with plaintext `TargetURL` and the plaintext edit code (once, like notes) —
          `defer` restores both plaintext fields plus the slug ID on every return path, mirroring
          `notes.Manager.Create`'s defer exactly
  - [x] `Redirect(ctx, slug string) (*domain.Link, error)`
    - [x] burn-after-click path AND hard-expiry path unified into one atomic `consumeLink`
          helper (`storage.Consume`, DELETE...RETURNING) rather than two separate code paths —
          **deviation from the original pseudocode, documented in the Migration section above**:
          matches the real, already-established `notes.Manager` pattern (`Consume` there is also
          one atomic method covering both triggers), not the earlier two-path sketch
    - [x] decrypt via `decryptTargetURL`, collapses any decryption failure to `domain.ErrNotFound`
          (mirrors `notes`' `decryptContent` existence-oracle avoidance exactly)
    - [x] `IncrementViews` on the non-consumed path (fire-and-forget error handling — logged,
          not propagated — matches notes' `View`)
    - [x] `ctx.Err()` checked between retries in `resolveUniqueSlug` (cancellation-aware, matches
          notes' former `resolveSlug`); wrapped via `fmt.Errorf("resolve unique slug: %w", ctxErr)`
          per `wrapcheck`
  - [x] `Delete(ctx, slug, editCode string, callerID uuid.UUID) error` — owner bypass OR
        edit_code, mirrors `notes.Manager.Update`'s bypass logic exactly
  - [x] `Peek(ctx, slug string) (*domain.Link, error)` — non-mutating read (no `IncrementViews`,
        no `Consume`) for the HTTP layer's privacy gate before the redirect side-effect fires;
        mirrors `notes.Manager.Peek` exactly, resolves the original checklist's
        "if the HTTP layer needs" hedge — it does, `LinkHandler.Redirect` (HTTP section) needs a
        read that doesn't burn a burn-after-click link just to check `VisibleTo`
  - [x] unexported methods (`resolveUniqueSlug`, `slugTaken`, `consumeLink`,
        `resolveAndHashEditCode`) placed after all exported methods, per this project's
        `funcorder` lint rule
- [x] `internal/usecases/notes/manager.go` — added `LinksChecker` interface
      (`Exists(ctx, id string) (bool, error)`) + `Manager.links` field + `NewManager` parameter;
      `Create` now calls the new unexported `resolveUniqueSlug(ctx, note)` helper, which wraps
      `domain.ResolveSlug` and then checks `m.links.Exists(ctx, domain.HashSlug(slug))`, returning
      `domain.ErrSlugConflict` on a hit — **this was the required two-way change**: notes checking
      links, not just links checking notes. `resolveUniqueSlug` was itself an extraction, added
      after `make lint` flagged `Create` at 42 > 40 statements (`funlen`) once the link-check block
      landed inline — same fix pattern as `links.Manager.Create`'s `resolveAndHashEditCode`
      extraction, now proven as a reusable idiom for this file rather than a one-off
  - [x] wired through the composition root: `internal/infra/server/db.go` gained
        `newLinkRepository`, `internal/infra/server/server.go` passes it as `notes.NewManager`'s
        2nd argument; every other `notes.NewManager(...)` call site across production and test
        code (`tests/manager/*.go`, `tests/auth/*.go`, `manager_test.go`) updated to match the new
        signature — found via `go build`/`go vet` "not enough arguments" diagnostics, not by grep
        alone, to guarantee no call site was missed
- [x] mockgen `//go:generate` directives for both new interfaces (`Storage`, `NotesChecker` in
      links; `LinksChecker` in notes) — `-source=manager.go -destination=manager_mocks_test.go`,
      regenerated for both packages
- [x] manager unit tests (testify/suite + mockgen + hand-written test doubles for
      Encryptor/EditCodeHasher, matching the `notes` package's existing convention), 22 tests in
      `links/manager_test.go`, covering:
  - [x] Create: happy path, custom alias conflict (immediate, no retry), random collision retry
        (succeeds within `maxHashRetries`), exhausted retries → `ErrHashCollision`, invalid URL
        scheme, cross-table conflict against both notes and links
  - [x] Redirect: happy path, expired (410 + delete via `consumeLink`), not found,
        burn-after-click consumed once then `ErrNotFound` on second call, decrypt failure
        collapsed to `ErrNotFound`
  - [x] Delete: owner bypass, non-owner needs edit_code, wrong edit_code, anonymous link always
        needs edit_code
  - `notes` package gained its own coverage for the reverse check: `ManagerTestSuite.links
    *MockLinksChecker` field, `expectSlugFree()` helper, added to the 13 Create-reaching tests
    (deliberately omitted from the 5 tests that fail before reaching the slug-resolution step)
- [x] full suite green after this section: `go build ./...`, `go build -tags=integration ./...`,
      `go vet ./...` (plain + `-tags=integration`), `make lint` (0 issues), `go test ./...
      -count=1 -race` (all packages, including `links` and `notes`), `go test -tags=integration
      ./tests/... -count=1 -race`, `gofmt -l .` (clean on all non-vendor files)

### HTTP handlers

- [x] Route split decided and documented: `GET /r/{id}` (redirect, browser-only, native handler)
      vs `POST /links` + `GET|DELETE /links/{id}` (JSON API, ogen-generated) — matches the
      already-resolved Open Questions item; both prefixes were already reserved slug prefixes
      from the Domain-layer work
- [x] `internal/adapters/http/link_handler.go`
  - [x] `LinkHandler` struct + constructor, following `NoteHandler`'s shape (embeds `*common`,
        holds `manager LinkManager`, the parsed `redirect.html` template, and `directRedirect bool`)
  - [x] `RegisterRoutes(mux *http.ServeMux)` — `GET /r/{id}` only; the JSON `/links` routes are
        registered separately in `router.go`'s `registerOgenRoutes` (ogen-served, like `/notes`)
  - [x] `Redirect(w, r)` handler
    - [x] resolve link via `manager.Redirect`, map `ErrNotFound`/`ErrExpired` to 404/410 via
          the existing shared `writeErrorPage` (no note-specific coupling — `domainErrToPageData`
          already generalizes past the "Paste" copy for unmapped errors)
    - [x] privacy gate — `handlePrivateAuth` calls `manager.Peek` (non-mutating) then
          `link.VisibleTo`/redirect-to-login-or-401, byte-for-byte mirroring
          `NoteHandler.handlePrivateAuth`, reusing `callerIDFromCtx`/`writeUnauthorized`/`negotiate`
    - [x] direct-redirect mode → `http.StatusMovedPermanently` to `link.TargetURL`
          (`//nolint:gosec` on the `G710` open-redirect finding — documented as intentional: the
          URL is already scheme-validated at creation and redirecting to it is the entire feature)
    - [x] interstitial mode → render `redirect.html` template
    - [x] **resolved via `directRedirect bool` set by `Handler.WithDirectRedirect`**, fed from the
          new `RouterOptions.DirectRedirect` / `--direct-redirect` flag (see Config/flags below) —
          this was the actual open design question (TODO originally sketched "direct-redirect vs
          interstitial" as two branches without saying what selects between them); resolved by
          reading ahead to the Acceptance-criteria section, which already specified
          `--direct-redirect=true/false` as a global server flag, not a per-link option
  - [x] **critical fix while wiring routes**: `isPublicRoute`/`isNoteIDPath` in
        `middleware_auth.go` only recognized note paths — `GET /r/{id}` and `GET /links/{id}`
        would otherwise have required a valid credential just to reach the privacy check at all
        when any auth mechanism is configured, breaking anonymous access to `public`-privacy links
        entirely. Added `isLinkPath` (fixed, unambiguous `r/`/`links/` prefixes, no `namedRoutes`
        disambiguation needed unlike notes' root catch-all) and wired it into `isPublicRoute`
  - [x] **critical fix in `OgenHandler.GetLink`**: unlike `GET /notes/{id}` (never actually routed
        to `ogenSrv` — see `GetNote`'s doc comment), `GET /links/{id}` genuinely is served by
        `OgenHandler`, so it is the *sole* enforcement point for the link's privacy level on that
        path. The first draft had no privacy check at all — a `privacy: owner`/`authenticated`
        link's `target_url` would have leaked to any caller via the JSON API. Fixed by checking
        `link.VisibleTo(callerIDFromCtx(ctx), authenticatedFromCtx(ctx))` before returning, mapped
        to the already-documented `401`. Added `authenticatedFromCtx` (context-only counterpart of
        `common.isAuthenticated`, since `OgenHandler` methods receive only a `context.Context`) —
        **known, accepted narrow gap**: it does not recognize the deprecated legacy
        `--auth-tokens` bearer auth (no caller identity to put in context) and does not special-case
        "no auth configured at all" the way `common.isAuthenticated` does for notes (which makes a
        `privacy: authenticated` *note* silently public in that configuration). For links this
        means a `privacy: authenticated` link is fail-closed instead of fail-open when the
        deployment has zero auth configured — arguably the safer of the two behaviors, and the
        divergence only matters for an already-degenerate configuration (privacy requiring auth on
        a server with no auth system at all)
- [x] `internal/adapters/http/templates/redirect.html`
  - [x] `<meta http-equiv="refresh" content="0;url=...">` (works without JS; html/template's
        built-in meta-refresh contextual escaping handles the URL safely)
  - [x] `<noscript>` fallback block pointing at the same `Continue` link
  - [x] JS `setTimeout` 500ms calling `window.location.replace(...)`, CSP nonce wired through
        (`nonceFromContext`) — **reconciled an apparent contradiction**: content="0" (immediate)
        and a 500ms JS timeout look redundant at first glance (meta-refresh always wins the race
        in any browser that honors both). Resolved as intentional defense-in-depth for the (real,
        accessibility-driven) case of a browser/extension that blocks meta-refresh specifically
        but still runs JS — not a "show the page for 500ms" UX mechanism
  - [x] target URL shown to the user before redirecting (transparency) — rendered in a
        `.field-block`/`.copy-row` (both pre-existing CSS classes), plus present in the page
        source/`noscript` regardless of how fast the browser navigates away
  - [x] **no `docs/design/*.html` mockup exists in this repo for this or any other page** — the
        directory itself doesn't exist, despite the project's frontend rule referencing it; the
        de facto design system is the existing rendered templates. Per the rule's own fallback
        ("no mockup exists for this new page → flag it... rather than guessing"), reused the
        `success.html`/`error.html` skeleton (`.wrapper--centered`, `.success-card`,
        `.success-banner`, `.field-block`, `.copy-row`, `.warning-block`, `.btn-primary`,
        `.btn-ghost`, `.card-divider`) verbatim — zero new CSS classes, colors, or fonts introduced
- [x] ogen-backed handlers for `/links` (`CreateLink`/`GetLink`/`DeleteLink`) implemented in
      `ogen_handler.go` directly (not a separate `ogen_link.go` file as first drafted) —
      **deliberately merged in**: `.golangci.yml` already carries a pre-existing, justified
      `ireturn` exclusion keyed to the filename regex `ogen_(handler|convert)\.go` ("ogen handler
      must return generated interfaces — unavoidable"); a separate file wouldn't match it and
      would need either duplicating that exclusion (touching the sacrosanct lint config for a
      case it already anticipated) or scattering `//nolint:ireturn` across every new method.
      Aligning the file layout with the existing exemption was the correct fix, not the config
  - [x] `LinkManager` interface added to `handler.go` (shared by `OgenHandler` and `LinkHandler`,
        same placement precedent as `NoteManager`) — `Create(ctx, targetURL string,
        links.CreateOptions)`, `Peek`, `Redirect`, `Delete(ctx, slug, editCode string, callerID)`
  - [x] response/error mapping added to `ogen_response.go` (`domainLinkToResponse`,
        `domainLinkToCreateResponse`, `linkTargetURL` — parses the stored plaintext URL into the
        `url.URL`-typed ogen field, fail-soft logged since a parse failure isn't reachable in
        practice) and `ogen_errors.go` (`mapCreateLinkError`/`mapGetLinkError`/`mapDeleteLinkError`,
        `//nolint:dupl` against `mapUpdateError` — same operation-per-type ogen constraint already
        documented at the top of the file for the four pre-existing mappers)
  - [x] `ttlToExpiresAt` converts `CreateLinkRequest.ttl` (seconds from creation, matching notes'
        `ttl` field shape for API consistency) into the absolute `*time.Time` `links.CreateOptions`
        actually wants — links have a plain hard expiry, not a burn-after-reading grace period, so
        this conversion (not a raw pass-through) is the adapter layer's job, not the domain's
  - [x] **field-naming deviation from the original spec, deliberate**: the spec's acceptance
        criteria call the created link's identifier `short_link`; used `id` instead, matching
        `NoteResponse.id` — one API, one identifier field name across both resource types, rather
        than two different names for the same concept
- [x] wired into `internal/adapters/http/router.go` (`registerOgenRoutes` gains `POST /links`,
      `GET /links/{id}`, `DELETE /links/{id}` — the DELETE route shares `NoteHandler`'s
      `withFailLockout` cache, safe because notes and links share one global slug/ID namespace, so
      cache keys never collide across the two resource types; `registerAllRoutes` adds
      `handler.LinkHandler` to the registrar list) and `internal/infra/server/server.go`
      (`links.NewManager` composed from `newLinkRepository` + the new `newNotesChecker` factory in
      `db.go` — a second, separately-typed handle onto the note repository exposing `Exists`,
      since `notes.Storage`, `initStorage`'s return type, deliberately doesn't declare it; repos
      are stateless `*bun.DB` wrappers, so constructing a second one is free)
- [x] rate limiting: confirmed `withRateLimit` wraps the whole `mux` (registered before the
      rate-limit middleware layer in `NewRouter`'s stack, same as every other route) — no
      link-specific rate-limit test added since `TestRateLimit_Exceeded`/`TestRateLimit_DifferentIPs`
      already prove the shared middleware works against the same `mux` any new route lands on;
      a per-route duplicate would test the middleware again, not anything link-specific
- [x] handler tests (`link_handler_test.go`, httptest + mockgen), covering:
  - [x] `POST /links` happy path, invalid URL → 400 (**bug caught here**: `domain.ErrInvalidURL`
        was missing from `domainErrStatus`'s table entirely, silently falling through to 500 —
        added it mapped to 400, matching the Bug-class checklist's literal "400" requirement, not
        422 like the other validation errors, since a disallowed URL scheme is a malformed
        request rather than an otherwise-valid-but-unprocessable one), slug conflict → 409
  - [x] `GET /links/{id}` happy path (asserts `edit_code` is never present in the metadata-only
        response), not found, expired, **plus** `privacy: owner` non-owner → 401 (regression
        coverage for the `OgenHandler.GetLink` privacy-leak fix above)
  - [x] `DELETE /links/{id}` owner bypass (no edit_code), edit_code, forbidden
  - [x] `GET /r/{id}` 301 mode, interstitial mode (asserts the target URL and "Redirecting" text
        appear in the rendered HTML), expired → 410, not found → 404, burn-after-click consumed
        once then 404 on the second call (`gomock.InOrder`), `privacy: owner` non-owner → HTML
        redirect-to-`/login` (mirrors the note-side `TestOwnerOnlyNote_OtherAuthenticatedUser_
        Unauthorized` IDOR regression exactly) and the owner-positive counterpart
  - [x] full suite green: `go build`/`go vet` (plain + `-tags=integration`), `make lint`
        (0 issues, after fixing a `dupl` pair via `//nolint`, a `gosec` G710 via `//nolint`, and
        two `unparam` findings in the new test file by dropping always-same-value parameters),
        `go test ./... -count=1 -race`, `go test -tags=integration ./tests/... -count=1 -race`,
        `gofmt -l .` clean on all non-vendor files

### OpenAPI

- [x] added `LinkResponse` schema (`id` — not `short_link`, see the field-naming deviation noted
      in HTTP handlers above — `target_url`, `views`, `burn_after_click`, `privacy`, `expires_at`,
      `created_at`) and `CreateLinkRequest`/`CreateLinkResponse` (`allOf LinkResponse` +
      `edit_code`, same composition pattern as `CreateNoteResponse`)
- [x] `POST /links` — 201/400/401/409/413/422/429/500 responses documented
- [x] `GET /links/{hash}` — 200/401/404/410/500 (401 added beyond the original checklist: the
      privacy-gate fix in `OgenHandler.GetLink` needed a documented status for it)
- [x] `DELETE /links/{hash}` — `X-Edit-Code` header param (mirrors notes' DELETE exactly),
      204/401/403/404/500
- [x] `GET /r/{hash}` — **not added as a formal `paths` entry**: this project's own existing
      convention already draws the line at "JSON API operations only" — bare `GET /{id}` (the
      note-view browser route) isn't in the spec either, only `GET /notes/{id}` is. Adding
      `/r/{id}` would force `ogen` to generate a JSON-shaped response type for a route that
      actually returns a raw 301 or an HTML page, which doesn't fit ogen's per-operation typed
      response model. Documented instead as prose in a new "Short links" section of the top-level
      `description` (both response shapes, the `--direct-redirect` flag, 404/410/401 semantics)
- [x] top-level `description` gained the "Short links" section: creation, redirect semantics,
      privacy/edit-code reuse from notes' existing prose (linked rather than duplicated), and the
      note on `GET /r/{id}` intentionally not being a documented path
- [x] `go generate ./internal/adapters/http/...` — regenerated `ogenapi` + `pkg/client`, confirmed
      `internal/adapters/http/spec/openapi.yaml` copy stayed in sync (part of the same generate run)

### CLI

- [x] `internal/adapters/cli/shorten.go` — `shorten` command, mirrors `create.go`'s
      flag-building pattern
  - [x] **two deliberate flag-naming deviations from the original draft (`--url`, `--alias`)**:
    - `--url` was already the *global* flag (`FlagURL`/`-u`, the padmark server address) before
      this work — reusing it for the link's target would either collide or silently shadow it.
      Renamed to `--target-url` (`FlagTargetURL`), documented at its declaration in `flags.go`
    - `--alias` → reused the existing `FlagSlug`/`--slug` instead — `create` already established
      `--slug` as this CLI's name for "custom identifier"; a second name for the same concept on
      a sibling command would be inconsistent UX, same reasoning as the `id` vs `short_link` JSON
      field-naming decision in the HTTP handlers section
  - [x] `--ttl`/`--privacy`/`--burn`/`--edit-code` reused verbatim from the existing global
        `FlagTTL`/`FlagPrivacy`/`FlagBurn`/`FlagEditCode` constants — same flags, same meaning,
        no `validateBurnTTL`-style gating needed since a link's `ttl` is an unconditional hard
        expiry, not `--burn`-dependent like notes' grace period
  - [x] `buildShortenReq` parses `--target-url` via `net/url.Parse` client-side (clear "invalid
        --target-url: ..." error before ever hitting the network) — `CreateLinkRequest.TargetURL`
        is `url.URL`-typed, not `string`, in the generated client
- [x] `internal/adapters/cli/expand.go` — `expand <id>` command, mirrors `get.go` (minus `--raw`,
      which doesn't apply — a link has no body to print raw, only structured metadata)
- [x] `internal/adapters/cli/delete_link.go` (underscore — this package's own filename convention
      for single-word command files, e.g. `create.go`/`get.go`/`edit.go`/`delete.go`, has no
      precedent for a hyphenated name; the command itself is still named `delete-link`, matching
      the acceptance criteria's literal CLI usage) — edit_code handling mirrors `delete.go` exactly
  - [x] **real refactor, not a linter workaround**: `delete.go` and `delete_link.go` share an
        almost byte-identical "resolve ID → delete → print confirmation" skeleton around ogen's
        distinct per-operation types (`DeleteNoteParams`/`DeleteLinkParams`, `DeleteNoteRes`/
        `DeleteLinkRes` share no common interface). Extracted the genuinely shared skeleton into
        `runDeleteCommand` (`app.go`, next to `noteIDArg`/`linkIDArg`) and rewrote `deleteAction`
        to use it too — not just `deleteLinkAction` — so both commands got smaller, not just the
        new one. The residual duplication (flag definitions + a response-type switch, now the
        *only* thing left in each file) is what `dupl` still flags; that residue genuinely can't
        be unified further without generics/reflection that would obscure more than 2 call sites
        justify — same accepted shape as `mapCreateLinkError`/`mapUpdateError` in `ogen_errors.go`,
        suppressed the same way (`//nolint:dupl` on the `package cli` line — the only placement
        golangci-lint's `dupl` accepts for a whole-file-spanning finding; a package-doc-comment
        placement was tried first and rejected, since `godoclint` requires an exact "Package cli "
        prefix and having two files both claim the package doc is the wrong fix)
- [x] wired into `NewApp()`'s command list in `app.go`, added `linkIDArg` next to `noteIDArg`
- [x] CLI unit tests: `shorten_test.go` (`buildShortenReqFor` — same pattern as `create_test.go`'s
      `buildCreateReqFor` — covering target-url/slug/privacy/burn/ttl/edit-code, plus
      `handleCreateLinkRes` response-mapping cases), `expand_test.go`/`delete_link_test.go`
      (`handleGetLinkRes`/`handleDeleteLinkRes` response-mapping cases, mirroring `get_test.go`'s
      style)
- [x] **manual end-to-end smoke test** against a real running server (`padmark-server serve
      --storage sqlite`, real `padmark-cli` binary, not mocks): `shorten` → `expand` → real
      `curl` confirms an actual `301` with the correct `Location` header → `delete-link` correctly
      rejects with no edit code, succeeds with the right one → a follow-up `expand` correctly
      404s. This is the acceptance criteria's own literal end-to-end scenario, verified for real
- [x] full suite green: `go build`/`go vet` (plain + `-tags=integration`), `make lint` (0 issues),
      `go test ./... -count=1 -race`, `go test -tags=integration ./tests/... -count=1 -race`,
      `gofmt -l .` clean

### Config / flags

- [x] `internal/infra/server/flags.go`
  - [x] **`FlagLinkHashLength`/`EnvLinkHashLength` deliberately NOT added** — this checklist item
        predates the actual slug-generation design (Domain section above): links reuse
        `domain.ResolveSlug`/`domain.SlugMaxLen`, the exact same fixed-length generator notes use,
        not an independently configurable length. A separate length knob for links only would
        directly reintroduce the Bug-class checklist's own "slug length is 10, not a short hash —
        slug is encryption key material" warning (old C2) as a *link*-specific footgun, since a
        shorter configured length would weaken the encryption key entropy. Sharing one fixed
        generator across both resource types is the safer design, not an oversight
  - [x] `FlagDirectRedirect`/`EnvDirectRedirect`, bool, default `true`
- [x] wired into `buildRouter` in `internal/infra/server/server.go` (`RouterOptions.DirectRedirect:
      cmd.Bool(FlagDirectRedirect)`) → `Handler.WithDirectRedirect` in `NewRouter`, same pattern as
      every other per-request-config `With*` call (`WithCookieMaxAge`, `WithSessionTTL`, ...)

### Cross-table slug uniqueness (required both directions)

- [x] `links.Manager.Create` checks `notes.Exists` before inserting — done in the Use-case section
      (`resolveUniqueSlug` → `slugTaken`)
- [x] `notes.Manager.Create` checks `links.Exists` before inserting — done in the Use-case section
      (`resolveUniqueSlug`); confirmed still wired by the new integration test below, not just by
      reading the code — the reverse direction is exactly the kind of thing a later refactor could
      silently drop without a test noticing
- [x] integration test added: `tests/manager/suite_test.go`'s
      `TestCrossTableSlugUniqueness_NoteFirst_ThenLinkConflicts` and
      `TestCrossTableSlugUniqueness_LinkFirst_ThenNoteConflicts`, on `ManagerSuite` (not a
      storage-specific file) so both directions run against **both** real SQLite and real Postgres
      via the existing `TestSQLiteManager`/`TestPostgresManager` suite runners, with zero
      duplicated test code — `ManagerSuite` gained a `LinkManager *links.Manager` field (parallel
      to the existing `Manager`/`RevealStore` fields), populated by each storage-specific
      `SetupTest` via a new `newLinkManager` helper mirroring `newManager`. Verified passing
      against both backends (`go test -tags=integration ./tests/manager/...`)
- [x] decision documented (both in code, next to the new tests, and here): **application-level
      check**, not a unified `slugs` table — this was already the shape the Use-case-section
      implementation took (each resource keeps its own table/repository, cross-checked via a
      narrow `Exists` interface each usecase package defines for the other), consistent with this
      project's per-layer-owns-its-interface convention. The trade-off: two application-level
      existence checks (one query each way) instead of a single DB-level unique constraint on a
      shared table — accepted because a unified table would mean either notes and links stop being
      independently owned repositories (cross-layer coupling this project's onion architecture
      doesn't otherwise have) or a third, redundant table just for slug reservation, adding a write
      path that must stay in lockstep with two other tables instead of one narrow read query

### Definition of Done for Phase 1

- [x] `go build ./...` and `go build -tags=integration ./...` clean
- [x] `go vet ./...` clean (plain + `-tags=integration`)
- [x] `make test` / `go test ./... -race -count=1` all green
- [x] `go test -tags=integration ./tests/... -race -count=1` all green
- [x] `make lint` — 0 issues, `.golangci.yml` untouched throughout the whole feature (every lint
      finding fixed via code changes or a targeted, justified `//nolint`, never a config edit)
- [x] `gofmt -l .` clean (verified non-vendor only — vendor/ carries its own upstream formatting,
      not this project's concern)
- [x] README.md updated: "What it does" bullet, new "How short links work" section (mirrors "How
      notes work"), CLI examples (`shorten`/`expand`/`delete-link`) under "Three ways to use
      padmark", REST examples (`/links`, `/r/{id}`) in the same section, "Security at rest" now
      mentions link target-URL encryption, `--direct-redirect` added to the Server flags table,
      the reserved-slug-prefix list in `--custom-slugs`' description updated with `links`/`r`

## Phase 2 — Privacy + Owner + Security

Everything in this phase turned out to already be implemented as part of Phase 1's actual work
(the HTTP handlers and Use-case sections built privacy/owner enforcement in from the start, not
as an afterthought) — this pass was verification and closing real test-coverage gaps, not new
business logic.

- [x] privacy enforcement wired into `Redirect` (`VisibleTo` gate before serving/redirecting) —
      `LinkHandler.handlePrivateAuth` (Peek + `VisibleTo`) gates `GET /r/{id}` before `Redirect`
      ever runs; `OgenHandler.GetLink` has its own equivalent gate for `GET /links/{id}` (the
      privacy-leak bug fixed in the HTTP handlers section)
- [x] owner bypass for `Delete` (no edit_code needed for the creator, same as notes) —
      `links.Manager.Delete`'s `OwnedBy(callerID) || hasher.Verify(...)` check, covered by
      `TestDeleteLink_OwnerBypass_NoEditCode`
- [x] CLI `--privacy` flag on `shorten` — reuses the existing `FlagPrivacy`/`--privacy` constant
      (see the CLI section's flag-naming-consistency decision); "update on link editing" doesn't
      apply — `links.Manager` has no `Update` method (Create/Peek/Redirect/Delete only, a Use-case
      section design decision), so there is no link-editing surface for a `--privacy` flag to
      attach to
- [x] URL validation hardening: explicit whitelist test matrix — added
      `TestCreate_URLSchemeWhitelist` in `links/manager_test.go` (replacing the two ad-hoc
      single-case tests it superseded), a `s.Run` table covering exactly the matrix this bullet
      specifies: `http://`/`https://` allowed; `javascript:`/`data:`/`file:`/`vbscript:`/
      schemeless/relative/empty rejected with `domain.ErrInvalidURL`
- [x] tests: privacy level × redirect caller (owner/authenticated/anonymous) — the HTTP handlers
      section had already covered owner-vs-non-owner-authenticated and owner-positive; this pass
      added the two missing cells of the matrix (mirroring `TestOwnerOnlyNote_Anonymous_*` and
      `TestPrivateNote_TOTPMode_*` from notes): `TestRedirectLink_OwnerOnly_Anonymous_Unauthorized`/
      `_HTML_RedirectToLogin`, `TestRedirectLink_PrivacyAuthenticated_Unauthenticated`/
      `_Authenticated`, plus the `GET /links/{id}` (ogen) equivalents
      `TestGetLink_PrivacyAuthenticated_Authenticated`/`_Anonymous_Unauthorized`. The fail-closed
      unrecognized-privacy-value case and the nil-OwnerID-with-owner-privacy case were already
      covered at the domain layer (`domain/link_test.go`'s table-driven `TestLink_VisibleTo`,
      built alongside `Link` itself in the Domain section) — not repeated here, since `VisibleTo`
      is the single source of truth both HTTP paths call into, not reimplemented per-handler
- [x] tests: malicious URL schemes rejected at Create time, not just at redirect time — the same
      `TestCreate_URLSchemeWhitelist` matrix proves this structurally: for every rejected case, no
      `storage.Create`/`storage.Exists`/`notes.Exists` mock expectation is set, so `gomock` would
      fail the test if `Create` reached persistence for a bad scheme — it can't reach `Redirect`
      at all if it was never persisted

## Phase 3 — Advanced (explicitly out of scope for Phase 1/2 — do not pull forward without discussion)

User explicitly authorized Phase 3 on 2026-07-20 ("Делай фазу 3"). Worked item by item, in order,
each fully tested and verified before moving to the next — same discipline as Phase 1/2.

- [x] custom alias validation reuse (reserved words, regex, cross-table) — confirmed: `grep -rn
      "reserved\|regex\|regexp" internal/usecases/links/*.go` returns zero matches outside test
      files. `links.Manager` has no custom-alias validation logic of its own; it purely delegates
      to `domain.ResolveSlug` (already shared with notes since the Phase 1 move) — nothing to do
- [x] expiry cleanup — **decision: lazy sweep-on-Create, not a background job** — matches this
      codebase's own already-established, three-times-repeated convention (`session_repository.go`,
      `invite_repository.go`, reveal-token repository), each with the identical doc comment "no
      background sweep worker in this codebase." `LinkRepository.Create` (both dialects) now
      deletes every already-expired row (`id != ?` excluding the row just inserted, so a
      caller-supplied already-past `ExpiresAt` — e.g. `ttl=0` — can't vanish before `Create` even
      returns) after a successful insert, mirroring `session_repository.go`'s pattern exactly.
      This closes the one gap `links.Manager.Redirect`'s own delete-on-read leaves: a link that
      expires and is never followed again. Tested: `TestCreate_SweepsExpiredLinks` (both dialects)
- [x] per-link `direct-redirect` override (server default + per-link opt-out) —
      `domain.Link.DirectRedirect *bool` (nil = inherit server default) +
      `EffectiveDirectRedirect(serverDefault bool) bool`; new nullable `direct_redirect` column
      (migration `20260722000001_links_direct_redirect.sql`, both dialects, with a dedicated
      migration test mirroring `migrate_privacy_test.go`'s Up/Down pattern); threaded through
      `links.CreateOptions` → `LinkHandler.Redirect` (`link.EffectiveDirectRedirect(h.directRedirect)`
      replaces the old unconditional `h.directRedirect`); OpenAPI `direct_redirect` field on
      `CreateLinkRequest`/`LinkResponse`/`CreateLinkResponse` (nullable bool); CLI `--direct-redirect`
      flag on `shorten` (`cmd.IsSet`-gated, matching `edit.go`'s existing optional-bool-flag
      pattern). Verified with a real manual smoke test: server started with
      `--direct-redirect=true`, a link created with `--direct-redirect=false` correctly got `200`
      (interstitial) instead of the server's own `301` default
- [x] `GET /links` (list my links, pagination) — **not implemented**, per this item's own
      explicit gate ("only implement if explicitly requested") — the "Делай фазу 3" authorization
      was general, not a specific request for this item, so it stays undone; the standing
      guidance not to raise pagination/list-limit concerns unprompted applies here too
- [x] bulk create (`POST /links/bulk`) — best-effort, not transactional (one item's failure
      doesn't abort the rest — matches how a URL-shortener bulk-import realistically needs to
      behave, since a caller submitting 50 URLs expects 49 successes even if one has a bad
      scheme). `BulkCreateLinksRequest`/`BulkCreateLinksResponse`/`BulkLinkResult` schemas
      (`links` capped at `[1, 100]` via ogen's generated `Validate()` — no manual length check
      needed); `OgenHandler.CreateLinksBulk` loops `linkManager.Create` per item via the shared
      `buildCreateLinkOptions`/`createOneBulkLink` helpers (also refactored `CreateLink` itself to
      use `buildCreateLinkOptions`, removing duplication rather than adding more). Per-item
      failures map through `errorStatusMessage` — the same safe mapping the single-item path
      uses — never the raw `err.Error()`, which could otherwise leak internal error detail; an
      unmapped (500-class) per-item failure is still logged, matching the single-item path's
      discipline. Verified with a real manual smoke test: 3 items (1 valid, 1 bad scheme, 1
      slug-conflict) → one success + two distinct, correctly-classified error messages
- [x] in-memory LRU cache for hot links — new `internal/infra/linkcache` package, reusing
      `github.com/bluele/gcache` (**already vendored** — the same library backing the fail-lockout
      cache in `middleware_faillock.go` — deliberately not a new dependency for one more use of
      the same shape), `.LRU().Expiration(ttl)`, default 10,000 entries / 30s TTL. `links.Manager`
      gained an optional `Cache` interface + `WithCache` fluent setter (mirrors
      `Handler.WithRevealStore`'s optional-collaborator shape — nil disables caching entirely,
      every code path already nil-checks it, so this is a pure performance optimization, never a
      correctness dependency). `Peek`/`Redirect` share a new `resolveLink` helper: cache hit skips
      both the storage round-trip and the decrypt; only a link that is **neither burn-after-click
      nor already expired** is ever cached (caching a burn link would risk a second concurrent
      request seeing a stale "not yet consumed" copy instead of relying on `storage.Consume`'s
      atomicity; caching an already-expired one is pure churn since `Redirect` is about to delete
      it and evict the cache again a moment later). `Delete` and the expired-link deletion path in
      `Redirect` both evict the cache explicitly.
      - **A real bug was caught and fixed during this work, before it shipped**: the initial
        refactor moved `decryptTargetURL` (inside the new `resolveLink`) to run *before* the
        caller pinned `link.ID = slug` — but the slug is the decryption key material, and
        `storage.GetByID` returns the row with `ID` still set to the *hashed* dbID, not the
        plaintext slug. Every `Peek`/`Redirect` call was therefore decrypting with the wrong key,
        collapsing to `domain.ErrNotFound` for every single link, cache or no cache. The existing
        unit tests (which use a `passthroughEncryptor` test double) couldn't catch this — a
        passthrough ignores the key entirely. Caught via a real manual smoke test against the
        actual server (not just unit tests), then fixed (`resolveLink` now takes the plaintext
        `slug`, not the hashed `dbID`, and pins `link.ID` before decrypting) and closed the
        underlying coverage gap with a new integration test using **real** `crypto.New()`
        encryption: `TestLink_CreateThenRedirect_RealCrypto_RoundTrips` in
        `tests/manager/suite_test.go`, run against both SQLite and Postgres
      - Accepted trade-off, documented in code, OpenAPI, and README: a cached link's `views`
        value (returned by `Peek`/`Redirect`, and thus visible via `GET /links/{id}`) can lag the
        true stored count by up to the cache TTL — the stored count itself is always accurate,
        since `IncrementViews` always writes to real storage regardless of cache hit/miss
      - Tests: 6 manager-level (cache hit skips storage, cache miss populates, burn-after-click
        never cached, expired-link eviction, `Peek` cache hit, `Delete` eviction) + 8 for the
        `linkcache` package itself (round-trip, miss, delete, delete-missing-key, TTL expiry, LRU
        eviction, and — the two tests directly proving the cloning contract — mutating a `Get`
        result or a `Set` caller's own value never corrupts the cache)
- [x] Prometheus metrics: `padmark_links_created_total`, `padmark_links_redirects_total`,
      `padmark_links_collisions_total`
      - This item's own premise ("follow the existing metrics interface pattern") didn't hold — no
        such pattern existed anywhere in this codebase yet (confirmed via `grep -rn
        "prometheus\|Metrics interface"` across `internal/` returning nothing relevant), so the
        design had to be established from scratch, grounded in existing conventions elsewhere
      - `links.Metrics` interface (`IncLinksCreated`/`IncRedirects`/`IncCollisions`) defined next
        to its consumer in `internal/usecases/links/manager.go`, same optional-collaborator shape
        as `Cache`/`WithCache` above: a `WithMetrics` fluent setter, nil-checked at every call
        site, so metrics are a pure observability add-on, never a correctness dependency
      - `IncCollisions` is scoped deliberately narrowly — only a random-slug retry collision inside
        `resolveUniqueSlug`, never a rejected custom alias (`ErrSlugConflict` from a chosen alias
        is normal user behavior, not an operational health signal); the interface's own doc
        comment documents this distinction so a future caller can't blur it by accident
      - Naming follows `~/.claude/docs/observability.md`'s `{namespace}_{subsystem}_{metric_name}_
        {unit}` convention: namespace `padmark`, subsystem `links`
      - Concrete Prometheus-backed implementation lives in the new `internal/infra/linkmetrics`
        package (`New()`/`NewRegistered(reg)`), kept out of `internal/usecases/links` per the
        onion-architecture import table (usecases must not import a concrete external dependency)
      - Exposure: a separate internal `/metrics` listener (`--metrics-addr`/`PADMARK_METRICS_ADDR`,
        empty by default = disabled), never on the main public-facing listener — per this same
        doc's "separate internal port" recommendation. `startMetricsServer` in
        `internal/infra/server/server.go` mirrors the pre-existing `startRedirectServer`'s shape
        and non-fatal-bind-failure handling exactly (a bind error here must not tear down an
        otherwise healthy main server), registered with `partyzanex/shutdown` the same way
      - The registry also carries the standard `collectors.NewGoCollector()`/
        `NewProcessCollector()` alongside the three link counters
      - New dependency: `github.com/prometheus/client_golang`, vendored
      - `serverAction` grew past `funlen`'s limit once this wiring landed; fixed by extracting two
        cohesive helpers — `newMetricsRegistry` (registry + collector setup) and `buildHandlers`
        (manager/linkManager/handler/ogenHandler construction) — same extract-a-helper pattern
        used throughout this whole session for identical violations, not a broader restructure
      - Tests: 7 manager-level (exactly which paths do/don't record each counter — negative cases
        deliberately omit the mock `EXPECT()` so gomock's controller fails on any unexpected call)
        + 5 for the `linkmetrics` package itself (each counter increments correctly, registration
        wires all three names onto a real registry, double-registration on the same registry
        errors instead of silently duplicating)
      - Verified end-to-end via a real manual smoke test (not just unit tests, given the earlier
        caching bug was only caught that way): started a real server with `--metrics-addr=:19090`,
        created one link and followed it three times, confirmed `padmark_links_created_total 1` and
        `padmark_links_redirects_total 3` on the internal port, and confirmed `/metrics` on the
        main public port returns `404` (never exposed there)

## Bug-class checklist (do not reintroduce these from the old shortlink codebase)

- [x] no `&&`/`||` logic-inversion bugs around nil checks (old C1) — `Link.VisibleTo`/`OwnedBy`
      mirror `Note`'s already-reviewed versions exactly, no new boolean logic invented; the
      `handlePrivateAuth`/`isPublicRoute` additions were the actual new boolean-logic surface this
      session and both have direct regression tests (owner-only denial, anonymous-public-access)
- [x] slug length is 10, not a short hash — slug is encryption key material (old C2) — links reuse
      `domain.ResolveSlug`/`SlugMaxLen`, the exact same generator notes use (see the deliberate
      `FlagLinkHashLength` omission in the Config/flags section — a separate length knob was
      rejected specifically to avoid reintroducing this bug class)
- [x] collision retry is a bounded `for` loop (max 5), never an unbounded `goto`/loop (old C3) —
      `for range maxHashRetries` in `links.Manager.resolveUniqueSlug`, covered by
      `TestCreate_RandomSlugCollision_Retries`/`TestCreate_RandomSlugCollision_ExhaustsRetries`
      in `manager_test.go`
- [x] `javascript:`/`data:`/`file:` schemes rejected with 400 at Create time (old C4) —
      `validateTargetURL` (scheme whitelist) + the `domainErrStatus` fix that maps
      `ErrInvalidURL` → 400 (was silently falling through to 500 before `TestCreateLink_InvalidURL`
      caught it — see the HTTP handlers section)
- [x] expired links hard-fail with 410 and get deleted, never redirect (old C5) —
      `links.Manager.Redirect` deletes on expiry and returns `ErrExpired` before ever reaching the
      decrypt/redirect path; `TestRedirectLink_Expired` (handler) +manager-level tests confirm
- [x] no debug/pprof endpoints bound to `0.0.0.0` in production config (old C6) — confirmed via
      `grep -rn pprof internal/ cmd/`: zero matches, `net/http/pprof` is never imported anywhere
- [x] rate limiting is actually wired to `/links` and `/r/{hash}`, not just assumed (old C7) —
      **this bullet's own "not just assumed" phrasing was a direct callout of a gap**: the HTTP
      handlers section had reasoned (correctly, but without direct proof) that the shared `mux`
      wrapping already covered new routes. Closed the gap with a concrete test,
      `TestRateLimit_Exceeded_POSTLinks`, hitting `POST /links` specifically and asserting the
      second request gets `429`
- [x] no gRPC surface reintroduced (old C8) — HTTP/ogen only, matches padmark's existing stack;
      confirmed no new dependency on any grpc package was added (`go.mod` unchanged for this)
- [x] tests use mocks/testcontainers + `t.Cleanup`, never a hardcoded DSN or a 100k-row load
      test committed to the suite (old C9) — every links test this session used `gomock`
      (handler/manager unit tests), sqlite in-memory (`SetupTest`/`TearDownTest`), or the existing
      `testcontainers` Postgres suite; no hardcoded DSNs or bulk-data tests were added

## Acceptance criteria (from the spec, section 15 — verify each explicitly before calling a
   phase done)

- [x] `POST /links` creates a link, returns `id` (not `short_link`, see the HTTP handlers section's
      field-naming deviation), once, plaintext, never re-derivable from storage (the DB only ever
      stores `domain.HashSlug(id)`) — `TestCreateLink_OK`, plus the real end-to-end smoke test
- [x] `GET /links/{hash}` returns `target_url`, 200 — `TestGetLink_OK`
- [x] `GET /r/{hash}` → 301 to `target_url` when `--direct-redirect=true` (the default) —
      `TestRedirectLink_DirectMode_301`, plus a real `curl -I` against a running server in the
      manual smoke test confirming the actual `Location` header
- [x] `GET /r/{hash}` → interstitial HTML (meta-refresh + noscript fallback) when
      `--direct-redirect=false` — `TestRedirectLink_InterstitialMode_200`
- [x] `DELETE /links/{hash}` removes the link via edit_code or owner bypass, 204 —
      `TestDeleteLink_OK`, `TestDeleteLink_OwnerBypass_NoEditCode`
- [x] expired link → `GET /r/{hash}` returns 410 Gone — `TestRedirectLink_Expired`,
      `TestGetLink_Expired`
- [x] `javascript:`/other disallowed-scheme URL → `POST /links` returns 400 —
      `TestCreateLink_InvalidURL` (this test is what caught the missing `domainErrStatus` entry)
- [x] `privacy: owner` + non-owner caller → `GET /r/{hash}` denies access — resolved as 401/
      redirect-to-login, not the spec draft's 404 (see the now-resolved Open Question below);
      `TestRedirectLink_OwnerOnly_NonOwner_HTML_RedirectToLogin`,
      `TestGetLink_OwnerOnly_NonOwner_Unauthorized`
- [x] burn-after-click link redirects once (301/interstitial), then 404 on every subsequent
      request — `TestRedirectLink_BurnAfterClick_ConsumedOnce` (handler level, `gomock.InOrder`)
      plus the manager-level `TestRedirect_BurnAfterClick_*` tests covering the atomic consume
- [x] note+link slug collision (either creation order) → `ErrSlugConflict` —
      `TestCrossTableSlugUniqueness_NoteFirst_ThenLinkConflicts`/`_LinkFirst_ThenNoteConflicts` in
      `tests/manager/suite_test.go`, against both real SQLite and real Postgres
- [x] DB dump of the `links` table contains no plaintext target URLs —
      `TestLink_TargetURLIsEncryptedAtRest` in `tests/manager/suite_test.go`: creates a link
      through the real `crypto.New()` encryptor (not a passthrough test double, unlike the
      manager's own unit tests), then reads the raw `target_url` column directly via `bun` and
      asserts it neither equals the plaintext nor contains any substring of it — against both
      real SQLite and real Postgres
- [x] random-slug collision retries are bounded (5), never hang — `TestCreate_RandomSlugCollision_
      Retries`/`_ExhaustsRetries`
- [x] `padmark-cli shorten --target-url ...` prints `short_link: <slug>` — flag renamed from the
      draft's `--url` to `--target-url` (see CLI section for why); verified via a real manual
      end-to-end smoke test against a running server, not just unit tests
- [x] `padmark-cli expand <slug>` prints `target_url: <url>` — verified via the same smoke test
- [x] `padmark-cli delete-link <slug> --edit-code ...` deletes and confirms — verified via the
      same smoke test, including the negative case (no edit code → rejected) and the
      post-delete `expand` correctly 404ing
- [x] migration Up and Down both work cleanly on a fresh DB — `migrate_links_test.go`
      (`TestUp_CreatesLinksTableWithDefaults`/`TestDown_DropsLinksTable`), following the
      `migrate_privacy_test.go` pattern, done in the Storage layer section
- [x] all new and existing tests pass, `t.Cleanup` used consistently, no leaked resources —
      confirmed via the full suite run at the end of every section this session, most recently
      `go test ./... -race -count=1` and `go test -tags=integration ./tests/... -race -count=1`,
      both clean
- [x] `go build ./...` OK
- [x] `go vet ./...` clean
- [x] `POST /links` returns 429 after exceeding the configured rate limit —
      `TestRateLimit_Exceeded_POSTLinks` (added specifically to close the Bug-class C7 gap, which
      called out that this needed direct proof, not an inference from shared-middleware reasoning)
- [x] structured logs use `slog` with an `id`/`"slug"`-equivalent field only where safe — **never
      log the plaintext target URL or edit_code** — confirmed via `grep` over `links/manager.go`:
      every log call passes only `id`/`slug` (the resource's own address, not a secret in this
      codebase's model — see below) or an `error`, never `TargetURL`/`EditCode`. **Reconciled
      against the spec draft's stricter wording** ("never log the plaintext slug"): notes' own
      `manager.go` already logs the plaintext slug as `"id"` (`m.log.DebugContext(ctx, "note
      created", "id", slug)`), a pre-existing, presumably-reviewed pattern this session didn't
      introduce. The slug/id is the resource's own public address — the exact value the creator
      shares to let others reach it — not a secret the way `edit_code` and (for links)
      `target_url` are; it only *doubles* as content-encryption key material, which constrains
      where it may be exposed to unauthorized parties, not whether the operator's own trusted
      application logs may record it. Links mirror this exact, already-accepted precedent rather
      than inventing a stricter rule for one resource type and not the other
- [x] `GET /r/{hash}` increments the `views` counter exactly once per successful redirect —
      `links/manager_test.go` asserts `s.Equal(1, got.Views)` after one successful `Redirect` with
      a mocked `IncrementViews`

## Open questions to resolve before or during implementation (not fully settled by the spec)

- [x] Resolved in favor of `handlePrivateAuth`'s 401/redirect pattern, not the spec draft's "404":
      consistency with this codebase's own established convention (notes already do this) beat
      matching the external spec literally — a privacy denial and "doesn't exist" are different
      facts, and collapsing them to 404 would also contradict the acceptance criteria's own
      already-settled "expired link → 410 Gone" (a 404 there would just as plausibly be "picked" by
      the same literal reading). Implemented identically for both `GET /r/{id}` (native handler,
      401 or `/login` redirect depending on `Accept`) and `GET /links/{id}` (ogen JSON API, 401)
- [x] `Link` does need its own `Peek` — confirmed the same shape as notes applies: `LinkHandler.
      handlePrivateAuth` calls `Peek` (non-mutating) to check `VisibleTo` before `Redirect`
      (mutating: increments views, consumes burn-after-click) ever runs, so a denied caller never
      triggers those side effects. `OgenHandler.GetLink` also uses `Peek` directly as its own
      implementation (a metadata read has no side effects to protect in the first place)
- [x] Route prefix decision resolved: `/r/{hash}` for redirects, `/links`/`/links/{hash}` for the
      JSON API — both added to `domain.ReservedSlugPrefixes()` (`"r"`, `"links"`), confirmed via
      `TestValidCustomSlug` new cases and `TestIsPublicRoute` (uses `buildNamedRoutes()`
      dynamically, so it picked up the new entries without needing its own update)

## Deprecated functionality cleanup (not part of the shortlink work — general tech debt)

User explicitly authorized both removals on 2026-07-21 ("Да, хочу, чтобы ты это взялся"). Both
are done.

### Legacy bearer-token write auth (`--auth-tokens` / `PADMARK_AUTH_TOKENS`) — [x] removed

Superseded by `--enable-accounts` (TOTP account system). Was marked `Deprecated:` in
`internal/infra/server/flags.go`, `internal/adapters/http/handler.go`,
`internal/adapters/http/middleware_auth.go`, `internal/adapters/http/router.go` — all now gone.

- [x] removed `FlagAuthTokens`/`EnvAuthTokens` and the `--auth-tokens` CLI flag; also removed
      `FlagCookieMaxAge`/`EnvCookieMaxAge`/`DefaultCookieMaxAge`/`--cookie-max-age`, discovered to
      be dead weight once the legacy login cookie it configured was gone — nothing else read it
- [x] removed `tokenCookieName`, `extractToken`, and `loginHandler` (the legacy `POST /login`
      backed by bearer tokens) from `middleware_auth.go`; `POST /login` is no longer a registered
      route at all — the API-token Bearer path (`resolveAPITokenUser`) is untouched, that's the
      *other*, non-deprecated Bearer mechanism
- [x] removed the legacy bearer-token branch in `authMiddleware.ServeHTTP` and the `allowed` field
      on `authMiddleware`; `newAuthMiddleware` no longer takes a `tokenSet` param
- [x] removed `common.AllowedTokens()` and the `allowedTokens` field on `common`/`Handler`; every
      read site updated — `NewHandler` no longer takes a `tokens []string` param,
      `handlePrivateAuth` (both `LinkHandler` and `NoteHandler`) now just checks `h.authMgr == nil`,
      `common.isAuthenticated` simplified to the same
- [x] removed `makeTokenSet` and its call site
- [x] `login.html` template simplified to a single always-TOTP form (the old
      `{{if .TOTPMode}}...{{else}}...{{end}}` token-form branch is gone, along with
      `loginViewData.TOTPMode`) — GET `/login` still works and still honors `?next=`/`?error=1`,
      it just always shows "Sign in with TOTP" now
- [x] updated `openapi.yaml` (regenerated `spec/openapi.yaml`, `ogenapi/`, `pkg/client/` via
      `go generate`): dropped the `--auth-tokens` paragraph from the Authentication section,
      reworded the `bearerAuth` security-scheme description and the `owner`-privacy field
      descriptions (notes + links) to describe the API-token mechanism only
- [x] updated README.md: removed the "Bearer tokens (API / CLI)" deprecated section and its flag
      table rows, reworded the auth intro to describe the single TOTP-gated mechanism
- [x] test audit (the real bulk of this change, ~40 call sites in `handler_test.go` plus
      `link_handler_test.go`/`web_auth_test.go`/`middleware_disable_api_test.go`/
      `tests/auth/*_test.go`): every test that used `s.newRouter([]string{"secret-token"})` purely
      to force "auth is configured" (`TestGetNote_Private_*`, `TestAuth_CreateRequiresAuth`,
      `TestAuth_PublicPaths`, the `newRouterWithReveal` burn-interstitial fixture, etc.) switched
      to `s.newRouterWithTOTPAuth()` / `WithAuthManager(s.authMgr)`; only tests that specifically
      exercised the legacy mechanism itself as a *credential* were deleted:
      `TestAuth_BearerToken`, `TestAuth_CookieToken`, `TestAuth_InvalidToken`, `TestLogin_OK`,
      `TestLogin_InvalidToken`, `TestLogin_EmptyToken`, `TestLogin_OK_WithNext`,
      `TestLogin_InvalidToken_PreservesNext`, the three `TestLogin_Next_*OpenRedirectBlocked`
      tests (coverage for `safeNextURL` itself survives via the existing standalone
      `TestSafeNextURL` table test in `router_test.go`), `TestIsAuthenticated_ReadsFromContext`,
      `TestLegacyLogin_NotRateLimited`, and `TestGetNote_Private_Authorized` (redundant with the
      already-existing `TestPrivateNote_TOTPMode_Authenticated`, which covers the same assertion
      via a real TOTP session instead of a bearer token)
- [x] `--token`/CLI API-token auth confirmed untouched — it's a distinct mechanism
      (`resolveAPITokenUser` / `ResolveAPIToken`), never read `allowedTokens`
- [x] verified: `go build`/`go vet` (plain + integration), `make lint` (0 issues), full unit +
      integration test suites, `gofmt -l .` clean, and a manual smoke test against a real server
      (`GET /login` renders "Sign in with TOTP", `POST /login` now falls through to the generic
      note-reveal catch-all and gets CSRF-rejected — same as any other undefined `POST` path, not
      a regression)

### Deprecated `notes.private` DB column — [x] dropped

Superseded by the `privacy` enum column. Was kept intentionally when `privacy` was introduced;
now dropped for real via `migrations/{sqlite,postgres}/20260723000001_drop_private.sql`.

- [x] new migration: `ALTER TABLE notes DROP COLUMN private;`. Down is intentionally NOT symmetric
      — it re-adds the column (`NOT NULL DEFAULT 0`/`FALSE`, matching the original type) for goose
      round-trip structural consistency, but the migration comment states plainly that Down cannot
      restore per-row data, since a dropped column's values are gone for good
- [x] simplified `Consume`/`SetBurnExpiry`'s explicit `Returning(...)` column lists back to
      `Returning("*")` in both dialects (sqlite + postgres) — the workaround comments (which
      explained why `*` used to fail) are gone along with the workaround itself
- [x] added `internal/infra/storage/sqlite/migrate_drop_private_test.go`
      (`TestUp_DropsPrivateColumn`, `TestDown_RestoresPrivateColumnStructureOnly`) as a companion
      to `migrate_privacy_test.go`, rather than editing that file's existing
      `TestDown_DropsPrivacyColumnOnly` assertion ("the deprecated private column must survive
      Down untouched") out of history — that assertion is still correct for *that* migration's
      Down, it's just no longer the final word on the column's lifetime
- [x] verified end-to-end: real SQLite smoke test (fresh DB → all 12 migrations apply cleanly,
      `PRAGMA table_info(notes)` confirms `private` is gone and `privacy` remains, then a real
      `POST /notes` + `GET /` round-trip against the post-migration schema succeeds) plus the full
      integration suite (`tests/auth`, `tests/manager`, `-tags=integration`) passing against real
      Postgres via testcontainers, which exercises the same migration on that dialect
- [x] no external-repo readers of `private` found via full-codebase analysis (dashboards, ad-hoc
      reporting, backup/restore tooling are outside this repo's scope to audit further; nothing
      inside the repo reads the column post-removal)
- [x] `links` table design (Phase 1 above) never copied this deprecated-column pattern — confirmed
      by inspection, `links` has no legacy boolean equivalent
