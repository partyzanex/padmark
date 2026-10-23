# Changelog

All notable changes to this project. The format is inspired by
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## v0.2.0 — unreleased (branch feature/owned-notes)

Range: v0.1.6 (d2e5e8f) → feature/owned-notes, 107 commits. 22 of them already sit on `main` without a tag.

### Compatibility with v0.1.6 — TL;DR

A v0.1.6 installation upgrades in place: existing notes are carried over, and every removed
option either keeps working with a warning or fails startup with a message that says what to do
instead. Three commands with the servers stopped: `migrate`, `encryption migrate`, `serve`.

| What | Effect when upgrading from v0.1.6 |
|---|---|
| **Existing notes are converted, not lost.** v0.1.6 stored plaintext content under plain slugs; the new schema stores `sha256(slug)` as the id and encrypted content. Migration `20261023000001` tags every v0.1.6 row (`legacy_slug`), and `encryption migrate` converts them: same URL, same content, same edit code, `private` becomes `privacy: authenticated`, burn and expiry are preserved. | `serve` refuses to start with `content migration required` while any tagged row remains, so a half-done upgrade cannot leak or corrupt anything. `encryption status` reports the count as `plaintext`; `encryption migrate --dry-run` previews. Known gap: a v0.1.6 custom slug that is itself 64 lowercase hex characters is not recognised as legacy. |
| **Schema migrations: 17 new, several irreversible** (drop `private`, token hashing). They run on `serve` and on `padmark-server migrate`. | SQLite: when the database already has applied migrations and new ones are pending, a copy `<db>.bak-<YYYYMMDD-HHMMSS>` (mode `0600`) is written next to it first; startup fails if the copy cannot be made. `--skip-migration-backup` / `PADMARK_SKIP_MIGRATION_BACKUP` opts out. PostgreSQL: a warning lists the pending versions; take `pg_dump` yourself. |
| **A keyring is required for content encryption.** | SQLite with a file database: defaults to `padmark-keys.json` next to the database file; `serve` generates it on first start (`0600`) and logs a warning to back it up. PostgreSQL and `:memory:`: `PADMARK_CONTENT_KEYRING_FILE` / `--content-keyring-file` with a file from `padmark-server encryption keygen` is mandatory. The `encryption` commands never create a keyring. Back it up separately from the database: losing the key means losing the content. |
| **`--auth-tokens` / `PADMARK_AUTH_TOKENS` is removed**; static bearer tokens are replaced by the account system (`--enable-accounts`, TOTP, API tokens). | Startup **fails closed** whenever the flag or the variable is set: the error names `--enable-accounts`, `PADMARK_BOOTSTRAP_TOKEN` and `/api/tokens`; the token value is never echoed. Without accounts the server is fully public, as v0.1.6 was without `--auth-tokens`, so unset the variable only after deciding whether you need accounts. |
| **`--cookie-max-age` / `PADMARK_COOKIE_MAX_AGE` is deprecated** in favour of `--session-ttl` (seconds, default 30 days). | Still accepted: its value becomes the session TTL with a warning. If both are set, `--session-ttl` wins and the old flag is ignored with a warning. |
| **API: `private: bool` is deprecated** in favour of `privacy: public\|authenticated\|owner`. | Requests may still send `private`: `true` maps to `authenticated`, `false` to `public`; sending both with contradicting values is a `400`. Responses carry both `privacy` and the read-only `private` (`true` for anything other than `public`). The `private` column is dropped irreversibly by migration `20260723000001`; the field lives on in the API only. |
| **`edit_code` on creation: 8–128 characters.** | `POST /notes` with a shorter code → `400/422`. Converted v0.1.6 notes keep their old codes whatever their length; verification is unchanged. |
| **The SQLite file is tightened to `0600`** on open, including `-wal`/`-shm`. | A pre-existing file with group/other bits is chmod-ed with an INFO log entry; if chmod fails, startup continues with a WARN instead of refusing. Other processes that read the file through group permissions lose access. |
| **PostgreSQL DSN must be a URL** (`postgres://`, `postgresql://`, `unix://`). | A key=value DSN is rejected at startup with an error that never contains the password. Previously such a DSN crashed the process with the password in the panic output. |
| **Go 1.27.3, image `golang:1.27.3-alpine` → `scratch`.** | Rebuild the image. `make lint` needs golangci-lint v2.15+. |
| Security headers and CSP are tightened; `/api` no longer loads Redoc from a CDN. | Check proxies that rewrite CSP. Redoc search needs `worker-src blob:`, which is not granted, so search in the docs page does not work (it did not before either). |

What did **not** change: addresses, default limits (`--rate-limit 10`, `--rate-burst 20`, timeouts,
4 MB body), an empty `--allowed-hosts` keeps the old behaviour, `--enable-accounts` is off by
default, so without opting in the server stays fully public, exactly like v0.1.6 without
`--auth-tokens`.

### Added

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
- `--disable-api`: kill switch for the REST/JSON API (`503`); the web UI keeps working.

**Short links**
- `/links`, `/links/bulk`, `GET/PUT/DELETE /links/{id}`, redirect at `/r/{id}`. The same
  edit_code / ownership / privacy / burn-after-click model as notes.
- An interstitial page instead of an instant redirect for anonymously created links by default
  (`--direct-redirect false`), so the instance cannot serve as an open redirector. Prometheus
  metrics on a separate `--metrics-addr` listener.

**Content encryption at rest**
- Note content and link target URLs are encrypted with AES-256-GCM under a random data key that
  is wrapped by the active installation key from the keyring. Slugs no longer take part in key
  derivation, which enables `--custom-slugs` (human-readable slugs with `/` and `.`).
- `encryption keygen | status | migrate | register | activate | rewrap | convert | retire`
  commands for key rotation. `serve` verifies the key registry and the envelopes before opening
  listeners. `encryption migrate` also converts v0.1.6 plaintext notes in place (hashed slug,
  encrypted content, hashed edit code); `tests/upgrade` runs that path against database dumps
  produced by the real v0.1.6 binary on SQLite and PostgreSQL.
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
- `--argon2-memory/time/threads` and the new `--argon2-memory-budget` (128 MiB): a process-wide
  cap on the memory held by concurrent argon2 derivations. README gained a memory-sizing section;
  a 256 MB container needs `GOMEMLIMIT≈200MiB`.
- Fonts and Redoc are self-hosted, no external CDNs. docker-compose with the keyring as a secret;
  `.env.example` documents every variable.

### Security fixes (from the audit)

No critical findings. High:
- 13 Go 1.26.x standard-library vulnerabilities closed by moving to 1.27.3; dependencies
  refreshed, `govulncheck` is clean.
- Burn-after-reading: HTML-only interstitial and reveal tokens, so a note no longer burns from an
  `<img src>` or an `Accept: */*` request.

Medium:
- Per-username lockout on `/totp-login` and login: 10 failures, 5 minutes, across all IPs. A lock
  can no longer be evicted from the cache by spraying 10,000 keys.
- Caller-chosen `edit_code`s are hashed with argon2id at the configured cost and must be at least
  8 characters. The wrong-code lockout is keyed by `{resource, caller, IP}` instead of globally
  per resource.
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
- `edit_code` is no longer accepted in the `DELETE` query string. Usernames are unique
  case-insensitively (the migration fails and changes nothing if collisions already exist).
  Stored argon2 costs above the accepted ceiling are refused before any allocation.
- Note content is capped at 1 MiB; rendered HTML is cached (128 entries, 2 minutes). The goldmark
  parser is guarded against quadratic inputs (nested `>`, long `*`/`_`/`~` runs, `](` tails); a
  single 255 KiB document no longer pins a core for minutes.
- ogen decode errors no longer leak internals; `415` is documented. An authenticated intruder on
  a private note gets `403` instead of a redirect to `/login`. The slug TOCTOU race between notes
  and links is closed. SQLite transactions use `BEGIN IMMEDIATE`.

### Upgrade checklist from v0.1.6

1. Stop every server instance; the content conversion must not race with writers.
2. Back up the database yourself as well (`migrate` makes a SQLite copy, but not a PostgreSQL
   one).
3. Keyring. SQLite with a file database: nothing to do, `padmark-keys.json` is created next to
   the database on the first `serve`, or create it now with
   `padmark-server encryption keygen --key-id k1 --output <db dir>/padmark-keys.json`.
   PostgreSQL: `encryption keygen`, `chmod 600`, and pass `PADMARK_CONTENT_KEYRING_FILE`.
4. `padmark-server migrate` (schema), then `padmark-server encryption migrate` (content;
   `--dry-run` first if you like). `encryption status` must show `plaintext: 0`.
5. Remove `--auth-tokens` / `PADMARK_AUTH_TOKENS`; startup fails while it is set. If you need
   authentication: `--enable-accounts`, `PADMARK_BOOTSTRAP_TOKEN`, then `/setup` or
   `padmark-cli setup`, then unset the bootstrap token. Replace `--cookie-max-age` with
   `--session-ttl` at your convenience.
6. Update API clients when convenient: `private` → `privacy`; new `edit_code`s need ≥ 8
   characters.
7. In a memory-limited container set `GOMEMLIMIT` to about 75–80 % of the limit and, if needed,
   `PADMARK_ARGON2_MEMORY_BUDGET`.
8. Behind a reverse proxy: `--trusted-proxies`, `--public-scheme https`, `--allowed-hosts`.
9. Start `serve`. It refuses with `content migration required` if step 4 was skipped, and with a
   keyring error if the keyring is missing on PostgreSQL. Check the log for the keyring backup
   warning and key-registry errors.

## v0.1.6

The last release before this changelog: notes, Markdown rendering with content negotiation,
`--auth-tokens`, the `private` flag.
