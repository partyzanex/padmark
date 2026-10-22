# Changelog

All notable changes to this project. The format is inspired by
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## v0.2.0 — unreleased (branch feature/owned-notes)

Range: v0.1.6 (d2e5e8f) → b4490e4, 83 commits. 22 of them already sit on `main` without a tag.

### Compatibility with v0.1.6 — TL;DR

This is a **breaking** release. Upgrading a v0.1.6 installation in place without preparation
will break it.

| What | Effect when upgrading from v0.1.6 |
|---|---|
| **All existing notes are lost.** Migrations `20260519000001` and `20260604000001` set `expires_at = now()` on every note: v0.1.6 stored plaintext under plain slugs, the new schema stores hashed slugs and encrypted content, so the old rows can neither be found nor decrypted. They are deleted on next access. | There is no way back (the migration's Down is a no-op). Export the notes you need via `GET /notes/{id}` before upgrading and recreate them. `encryption migrate` does not help here: it is for intermediate builds with unversioned ciphertext, not for v0.1.6 plaintext. |
| **A keyring is mandatory.** `PADMARK_CONTENT_KEYRING_FILE` / `--content-keyring-file`, created with `padmark-server encryption keygen`. | Without it `serve` refuses to start. The file must be `0600`/`0400`, otherwise startup is refused (except under `/run/secrets/` or with `--allow-shared-keyring`). Back it up separately from the database: losing the key means losing the content. |
| **`--auth-tokens` and `--cookie-max-age` are removed.** Replaced by the account system (`--enable-accounts`, TOTP, API tokens). | The flag on the command line fails startup with `flag provided but not defined`. **The `PADMARK_AUTH_TOKENS` environment variable is silently ignored**: the server comes up fully public with anonymous writes allowed. Check the environment before deploying. |
| **API: `private: bool` is replaced by `privacy: public\|authenticated\|owner`.** The `private` field is gone from responses. | A client sending `private: true` gets no error but a **public note** (ogen skips unknown fields). Clients reading `private` from responses see the field missing. The `private` column is dropped irreversibly by migration `20260723000001`. |
| **Schema migrations run automatically on `serve`**: 16 new ones, several irreversible (drop `private`, token hashing). | Back up the database before the first start of the new version. |
| **`edit_code` on creation: 8–128 characters.** | `POST /notes` with a shorter code → `400/422`. Verification of stored codes is unchanged. Irrelevant for v0.1.6 data, since those notes expire anyway. |
| **The SQLite file is forced to `0600`**, including `-wal`/`-shm`. | If chmod is impossible, startup is refused. Other processes that read the file through group permissions lose access. |
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
  listeners.
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
- A keyring with permissions wider than `0600` is refused. The SQLite file is `0600`.
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

1. Export the notes you need; they will not survive the migration.
2. Back up the database.
3. `padmark-server encryption keygen --key-id k1 --output <path>`, `chmod 600`, and pass
   `PADMARK_CONTENT_KEYRING_FILE`.
4. Remove `--auth-tokens` / `PADMARK_AUTH_TOKENS` and `--cookie-max-age`. If you need
   authentication: `--enable-accounts`, `PADMARK_BOOTSTRAP_TOKEN`, then `/setup` or
   `padmark-cli setup`, then unset the bootstrap token.
5. Update API clients: `private` → `privacy`, `edit_code` ≥ 8 characters.
6. In a memory-limited container set `GOMEMLIMIT` to about 75–80 % of the limit and, if needed,
   `PADMARK_ARGON2_MEMORY_BUDGET`.
7. Behind a reverse proxy: `--trusted-proxies`, `--public-scheme https`, `--allowed-hosts`.
8. Start `serve`; migrations apply automatically. Check the log for key-registry errors.

## v0.1.6

The last release before this changelog: notes, Markdown rendering with content negotiation,
`--auth-tokens`, the `private` flag.
