# Changelog

All notable changes to this project. The format is inspired by
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## v0.4.0 — unreleased

The current release draft is v0.4.0. Existing Git tags already reach **v0.3.3** (`b32ab62`);
summaries of the previously tagged v0.2.x/v0.3.x releases are recorded below.

The upgrade comparison and cumulative feature overview use historical **v0.1.6** (`d2e5e8f`)
as their baseline because its real database dumps exercise the plaintext-to-encrypted upgrade.
That baseline is not the latest released version.

### Pending changes beyond v0.3.3

- Account ownership and the `public`/`authenticated`/`owner` privacy model; short links and
  configurable API disabling.
- Installation-key content envelopes, key-management commands and resumable schema/content
  upgrade support, including conversion of tagged v0.1.6 plaintext notes.
- Headless account provisioning, the MCP proxy and `/llms.txt`.
- Bootstrap/step-up protection, session/key revocation, CSRF/lockout hardening, argon2 memory
  budgeting, content/rendering limits and operational configuration checks.
- The separate Go browser-test module, desktop/mobile PNG comparisons and CI integration.
- Database-enforced slug uniqueness across notes and links, including concurrent creates
  from different server replicas. Reservations are released atomically on deletion or burn.
  If existing native records already collide, the schema migration stops and preserves both
  records; resolve the collision explicitly before retrying.

### Compatibility with v0.1.6 — TL;DR

A v0.1.6 installation upgrades in place: existing notes are carried over, and every removed
option either keeps working with a warning or fails startup with a message that says what to do
instead. With the servers stopped: create the keyring (`encryption keygen`), then `migrate`,
`encryption migrate`, `serve`.

| What | Effect when upgrading from v0.1.6 |
|---|---|
| **Existing notes are converted, not lost.** v0.1.6 stored plaintext content under plain slugs; the new schema stores `sha256(slug)` as the id and encrypted content. Migration `20261023000001` tags every v0.1.6 row (`legacy_slug`), and `encryption migrate` converts them: same URL, same content, same edit code, `private` becomes `privacy: authenticated`, burn and expiry are preserved. | `serve` refuses to start with `content migration required` while any tagged row remains, so a half-done upgrade cannot leak or corrupt anything. `encryption status` reports the count as `plaintext`; `encryption migrate --dry-run` previews. A v0.1.6 custom slug that is itself 64 lowercase hex characters looks like a hashed id; migration `20261024000001` tags it because its plaintext edit code does not have the full shape of a stored hash (a code that only starts with `v1$` or `s1$` is still recognised). Such a note is never wrapped as ciphertext or pruned; `encryption status` reports an untagged one as `untagged_legacy`. Remaining gap: a caller-chosen code of the exact hash shape shows as `corrupt_raw`; `encryption prune --corrupt --dry-run` lists its ID and `encryption mark-legacy --id <id>` tags it for conversion. |
| **Schema migrations: 19 new, several irreversible** (drop `private`, token hashing). They run on `serve` and on `padmark-server migrate`. | SQLite: when the database already has applied migrations and new ones are pending, a copy `<db>.bak-<YYYYMMDD-HHMMSS>` (mode `0600`) is written next to it first; startup fails if the copy cannot be made. `--skip-migration-backup` / `PADMARK_SKIP_MIGRATION_BACKUP` opts out. PostgreSQL: a warning lists the pending versions; take `pg_dump` yourself. |
| **A keyring is required for content encryption.** | SQLite with a file database: defaults to `padmark-keys.json` next to the database file; `serve` generates it on first start (`0600`) and logs a warning to back it up. PostgreSQL and `:memory:`: `PADMARK_CONTENT_KEYRING_FILE` / `--content-keyring-file` with a file from `padmark-server encryption keygen` is mandatory. The `encryption` commands never create a keyring, and an upgrade runs `encryption migrate` before the first `serve`, so create the keyring with `encryption keygen` first on SQLite too; a missing file gives an error with the exact keygen command. Back it up separately from the database: losing the key means losing the content. |
| **`--auth-tokens` / `PADMARK_AUTH_TOKENS` is removed**; static bearer tokens are replaced by the account system (`--enable-accounts`, TOTP, API tokens). | Startup **fails closed** whenever the flag or the variable is set: the error names `--enable-accounts`, `PADMARK_BOOTSTRAP_TOKEN` and `/api/tokens`; the token value is never echoed. Without accounts the server is fully public, as v0.1.6 was without `--auth-tokens`, so unset the variable only after deciding whether you need accounts. |
| **`--cookie-max-age` / `PADMARK_COOKIE_MAX_AGE` is deprecated** in favour of `--session-ttl` (seconds, default 30 days). | Still accepted: its value becomes the session TTL with a warning. If both are set, `--session-ttl` wins and the old flag is ignored with a warning. |
| **API: `private: bool` is deprecated** in favour of `privacy: public\|authenticated\|owner`. | Requests may still send `private`: `true` maps to `authenticated`, `false` to `public`; sending both with contradicting values is a `400`. Responses carry both `privacy` and the read-only `private` (`true` for anything other than `public`). The `private` column is dropped irreversibly by migration `20260723000001`; the field lives on in the API only. |
| **Privacy levels need the account system.** Without `--enable-accounts` nothing enforces `authenticated` or `owner`. | `privacy: authenticated` or `owner` → `422`; the editor offers only Public (and Owner only only to a signed-in user). `private: true` (v0.1.6) is still accepted and, as in v0.1.6 without `--auth-tokens`, not enforced until accounts are enabled. Existing v0.1.6 private notes stay readable, editable and deletable with their edit codes. |
| **`edit_code` on creation: 8–128 characters.** | `POST /notes` with a shorter code → `400/422`. Converted v0.1.6 notes keep their old codes whatever their length; verification is unchanged. `DELETE /notes/{id}?edit_code=` still works but is deprecated: it is used only without `X-Edit-Code` and logs a warning; send the header, as the v0.1.6 `padmark-cli` already does. |
| **Note content: at most 1 MiB** (1,048,576 characters) on create and update. v0.1.6 accepted up to the 4 MiB request body. | Larger content → `400`. Existing larger notes are converted and stay readable unchanged; to edit one, shorten its content to 1 MiB or less. Rendered HTML of notes up to 256 KiB is cached in memory for 2 minutes. |
| **Burn-after-reading reads need intent.** | `GET /notes/{id}` with no `Accept` header at all (the v0.1.6 `padmark-cli` and `pkg/client`) still reads and burns the note. An `Accept` of only `*/*` (`curl`), `HEAD`, a browser subresource, and the short `/{id}` URL without an exact `Accept` → `409 burn_read_not_explicit`; the note stays intact. Browsers get a reveal page first. |
| **Custom slugs stay on by default** (`--custom-slugs`, `PADMARK_CUSTOM_SLUGS`), as in v0.1.6. | `POST /notes` with `slug` (`padmark-cli create --slug`) works as before. New: a slug may contain `/` and `.` (up to 10 segments), and a first segment that is a reserved route name (`admin`, `setup`, `change-password`, …) → `422`. Set `PADMARK_CUSTOM_SLUGS=false` to issue only random slugs (`422 custom slugs are disabled on this server`). Existing notes with custom slugs stay readable and editable either way. |
| **`docker-compose.yml` changed.** `POSTGRES_PASSWORD` and `PADMARK_CONTENT_KEYRING_FILE` are required; the image is `postgres:17-alpine`; the database port is published on `127.0.0.1:5432`. | An existing volume keeps its old password (`padmark` in the v0.1.6 file), so set `POSTGRES_PASSWORD=padmark` or change the password in the database first. The v0.1.6 file (`postgres:18-alpine` with the volume on `/var/lib/postgresql/data`) does not start with current PostgreSQL 18 images. If your data directory is PostgreSQL 18, keep a PostgreSQL 18 image: PostgreSQL 17 cannot open it. Remove the port mapping if port 5432 is already in use on the host. |
| **The SQLite file is tightened to `0600`** on open, including `-wal`/`-shm`. | A pre-existing file with group/other bits is chmod-ed with an INFO log entry; if chmod fails, startup continues with a WARN instead of refusing. Other processes that read the file through group permissions lose access. |
| **PostgreSQL DSN must be a URL** (`postgres://`, `postgresql://`, `unix://`). | A key=value DSN is rejected at startup with an error that never contains the password. Previously such a DSN crashed the process with the password in the panic output. |
| **Go 1.27.3, image `golang:1.27.3-alpine` → `scratch`.** | Rebuild the image. `make lint` needs golangci-lint v2.15+. |
| Security headers and CSP are tightened; `/api` no longer loads Redoc from a CDN. | Check proxies that rewrite CSP. Redoc search needs `worker-src blob:`, which is not granted, so search in the docs page does not work (it did not before either). |

What did **not** change: addresses, default limits (`--rate-limit 10`, `--rate-burst 20`, timeouts,
4 MB body), an empty `--allowed-hosts` keeps the old behaviour, `--enable-accounts` is off by
default, so without opting in the server stays fully public, exactly like v0.1.6 without
`--auth-tokens`.

### Cumulative feature overview since v0.1.6

**Accounts and access control** (`--enable-accounts`)
- Users with a password (argon2id) and TOTP; the first admin is created through `/setup` with
  `PADMARK_BOOTSTRAP_TOKEN` (≥32 characters, environment only). Invites are single-use and live
  24 hours. Cookie sessions, `--session-ttl` defaults to 30 days.
- `/admin`: invites, API keys (Bearer tokens for the CLI/MCP, `/api/tokens`), user revocation with
  cascade. `/change-password` requires TOTP and signs out every other session and revokes the
  account's keys. Signing in again revokes the previous session; `/admin` and `/change-password`
  have a sign-out control.
- Headless provisioning: `padmark-cli setup`, `create-token`, `create-invite`, JSON `/setup`,
  `/api/tokens`, `/api/invites`.
- Privacy levels `privacy: public | authenticated | owner` for notes and links. The owner edits
  and deletes their notes without an `edit_code`. Anonymous mutations are limited to what an
  anonymous caller could read.
- `--disable-api`: kill switch for the REST/JSON API (`503`). The editor becomes read-only:
  publishing/saving use those API endpoints. Note views, link redirects, account/admin forms
  and health checks remain available.

**Short links**
- Redirects check privacy on the snapshot supplying the URL. Burn/expiry deletion and click
  increments also check the reader's current access in SQL, including across replicas.
- `/links`, `/links/bulk`, `GET/PUT/DELETE /links/{id}`, redirect at `/r/{id}`. The same
  edit_code / ownership / privacy / burn-after-click model as notes.
- An interstitial page instead of an instant redirect for anonymously created links by default
  (`--direct-redirect false`), so the instance cannot serve as an open redirector. Prometheus
  metrics on a separate `--metrics-addr` listener.

**Content encryption at rest**
- Note content and link target URLs are encrypted with AES-256-GCM under a random data key that
  is wrapped by the active installation key from the keyring. Slugs no longer take part in key
  derivation, which enables `--custom-slugs` (human-readable slugs with `/` and `.`).
- Key-management subcommands under `encryption`: `keygen`, `key-add`, `key-activate`,
  `key-remove`, `register`, `status`, `migrate`, `rewrap`, `convert` and `prune`.
  `serve` verifies the key registry and the envelopes before opening
  listeners. `encryption migrate` also converts v0.1.6 plaintext notes in place (hashed slug,
  encrypted content, hashed edit code); `tests/upgrade` runs that path against database dumps
  produced by the real v0.1.6 binary on SQLite and PostgreSQL.
  Every nonempty legacy edit code uses argon2id: its length and alphabet cannot establish
  that it was server-generated. The original code remains valid after migration.
- Session IDs, invite tokens and burn-after-reading reveal tokens are stored as SHA-256 digests
  only.

**Agent surface**
- `padmark-mcp`: a stdio MCP proxy to the server. `/llms.txt` as a machine-readable description.
  `padmark-cli` gained `--allow-insecure-http`; by default a token is never sent over plain HTTP
  to a non-loopback host.

**Operations**
- `--public-scheme https` marks cookies `Secure` and enables HSTS. `--allowed-hosts` now applies
  to the main listener too (`421`), with `/healthz` and `/readyz` exempt. `--trusted-proxies`
  warns about misconfiguration. `--csrf-secret` is configurable.
- `padmark-server migrate` applies pending schema migrations and exits. On SQLite a `0600` copy of
  the database is written next to it before the first pending migration (`--skip-migration-backup`
  to opt out); on PostgreSQL the pending versions are logged with a reminder to `pg_dump`.
  Existing backups are preserved on filename collisions: a new copy gets a unique suffix,
  and a failed write removes only the file reserved for that attempt.
- `--argon2-memory/time/threads` and the new `--argon2-memory-budget` (128 MiB): a process-wide
  cap on the memory held by concurrent argon2 derivations. README gained a memory-sizing section;
  a 256 MB container needs `GOMEMLIMIT≈200MiB`.
- Fonts and Redoc are self-hosted, no external CDNs. docker-compose with the keyring as a secret;
  `.env.example` documents server/Compose configuration, keyring behavior and memory settings.

**Development and browser tests**
- Go browser tests in the separate `tests/ui` module, using playwright-go and the standard
  test runner: 19 scenarios at desktop/mobile sizes (38 checks). Forms, validation, keyboard
  actions, clipboard, themes, browser diagnostics and visual comparisons are covered.
- Browser dependencies, driver/cache, PNG references and reports live under `tests/ui`.
  Root npm/TypeScript manifests are removed; the Go binding installs its internal driver.
- `make test-ui-setup`, `test-ui`, `test-ui-headed`, `test-ui-debug`, `test-ui-update` and
  `test-ui-report` provide setup, execution, Inspector debugging, reference updates and reports.
  Root `make test` remains the application unit/integration command; it excludes the nested
  browser module. CI runs the browser suite separately before container publishing.
- README, development/agent guides and `/llms.txt` are aligned with current keyring defaults,
  migration steps, account-based ownership, header-only delete codes and Go UI-test commands.

### Security fixes (from the audit)

No critical findings. High:
- The toolchain moves from Go 1.26.x to 1.27.3, with refreshed dependencies.
- Burn-after-reading: HTML-only interstitial and reveal tokens, so a note no longer burns from an
  `<img src>` or an `Accept: */*` request.

Medium:
- Per-username lockout on `/totp-login` and login: 10 failures, 5 minutes, across all IPs. A lock
  can no longer be evicted from the cache by spraying 10,000 keys.
- Caller-chosen `edit_code`s are hashed with argon2id at the configured cost and must be at least
  8 characters. Wrong-code lockout is per resource/account, or per resource/client IP for an
  anonymous caller (IPv6 grouped by /64), rather than global per resource.
- A keyring with permissions wider than `0600` is refused. The SQLite file is tightened to `0600`
  on open (a failed chmod on a pre-existing file is a warning, not a refusal).
- Process-wide argon2 memory budget: without it, 4 concurrent `POST /notes` with an `edit_code`
  OOM-killed the server in a 256 MB container.
- CSRF tokens are bound to the session; forms check `Origin`/`Sec-Fetch-Site`. Duplicate
  `padmark_session`/`padmark_csrf` cookies (cookie tossing from a sibling subdomain) are treated
  as ambiguous and rejected.
- The PostgreSQL DSN password never reaches errors, panics or logs on a malformed DSN.

Low and other:
- CSP: `base-uri 'none'`, `form-action 'self'`, `frame-ancestors 'none'`, `object-src 'none'`,
  nonces on scripts. Markdown element IDs get a `user-content-` prefix against DOM clobbering.
- The `DELETE` query-string `edit_code` is deprecated: it is used only without `X-Edit-Code`
  and logs a warning (kept for v0.1.6 clients). Usernames are unique
  case-insensitively (the migration fails and changes nothing if collisions already exist).
  Stored argon2 costs above the accepted ceiling are refused before any allocation.
- Note content is capped at 1,048,576 characters, with the request-byte limit still applying;
  rendered HTML is cached (128 entries, 2 minutes). The goldmark
  parser is guarded against quadratic inputs (nested `>`, long `*`/`_`/`~` runs, `](` tails); a
  single 255 KiB document no longer pins a core for minutes.
- ogen decode errors no longer leak internals; `415` is documented. An authenticated intruder on
  a private note gets `403` instead of a redirect to `/login`. The slug TOCTOU race between notes
  and links is closed. SQLite transactions use `BEGIN IMMEDIATE`.

### Upgrade checklist from v0.1.6

1. Stop every server instance; the content conversion must not race with writers.
2. Back up the database yourself as well (`migrate` makes a SQLite copy, but not a PostgreSQL
   one).
3. Keyring. Create it before step 4: the `encryption` commands never create one.
   SQLite with a file database:
   `padmark-server encryption keygen --key-id k1 --output <db dir>/padmark-keys.json`;
   `serve` and the `encryption` commands find it there without a setting.
   PostgreSQL: `encryption keygen`, `chmod 600`, and pass `PADMARK_CONTENT_KEYRING_FILE`.
4. `padmark-server migrate` (schema), then `padmark-server encryption migrate` (content;
   `--dry-run` first if you like). `encryption status` must show `plaintext: 0`. If it shows
   `untagged_legacy` or `corrupt_raw` above 0, list the IDs with
   `encryption prune --corrupt --dry-run`. Tag each v0.1.6 note among them with
   `encryption mark-legacy --id <id>` and rerun `encryption migrate`. Do not run
   `encryption prune --corrupt` before that: it deletes every remaining `corrupt_raw` record.
5. Remove `--auth-tokens` / `PADMARK_AUTH_TOKENS`; startup fails while it is set. If you need
   authentication: `--enable-accounts`, `PADMARK_BOOTSTRAP_TOKEN`, then `/setup` or
   `padmark-cli setup`, then unset the bootstrap token. Replace `--cookie-max-age` with
   `--session-ttl` at your convenience.
6. Update API clients when convenient: `private` → `privacy`; new `edit_code`s need ≥ 8
   characters; content is limited to 1 MiB; send `X-Edit-Code` instead of `?edit_code=`.
7. In a memory-limited container set `GOMEMLIMIT` to about 75–80 % of the limit and, if needed,
   `PADMARK_ARGON2_MEMORY_BUDGET`.
8. Behind a reverse proxy: `--trusted-proxies`, `--public-scheme https`, `--allowed-hosts`.
9. Start `serve`. It refuses with `content migration required` if step 4 was skipped, and with a
   keyring error if the keyring is missing on PostgreSQL. Check the log for the keyring backup
   warning and key-registry errors.

## v0.3.3 — 2026-07-12

- Self-host Source Serif 4 and JetBrains Mono fonts and remove the Google Fonts CDN dependency.
- Use `MaxBodyBytes` as the note-size bound instead of the earlier fixed 1 MiB limit.
  The current unreleased version adds an explicit content-character limit and rendering guards.

## v0.3.2 — 2026-07-12

- Add `--public-scheme` to force the scheme used in absolute links behind a reverse proxy.

## v0.3.1 — 2026-07-10

- Add the CLI `--private` flag.

## v0.3.0 — 2026-07-10

- Issue account API tokens from the admin page and authenticate CLI requests with Bearer keys.
- Store API-token hashes, use self-contained token envelopes with the server URL, and enforce
  a per-user key cap.
- Update related migrations, security boundaries and token-issuance documentation.

## v0.2.1 — 2026-07-05

- Support immediate burn-after-reading and speed up edit-code hashing while retaining
  verification of existing hashes.

## v0.2.0 — 2026-06-08

- Encrypt note content at rest and hash edit codes.
- Add password/TOTP accounts, the admin interface and associated tests.
- Update burn-after-reading behavior and strengthen security/layer boundaries.

## v0.1.6 — 2026-05-10

Historical plaintext-note release with Markdown/JSON/raw content negotiation, `--auth-tokens`
and the `private` flag. Its real dumps are retained in `tests/fixtures/v0.1.6` for upgrade tests;
it is not the project's latest tagged release.
