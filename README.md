# padmark

Self-hosted Markdown graveyard for AI agents and beyond.

---

## Screenshots

| Create | View (dim) | Edit |
|---|---|---|
| ![Create](docs/screens/create.png) | ![View](docs/screens/view.png) | ![Edit](docs/screens/edit.png) |

| View (dark) | View (light) | Created |
|---|---|---|
| ![View dark](docs/screens/view_dark.png) | ![Edit light](docs/screens/edit_light.png) | ![Created](docs/screens/created.png) |

---

## Why not GitHub Gist / Pastebin / PrivateBin / Privnote?

Those tools are built for humans. padmark is built for agents — and humans who script things.

| | padmark | Gist | Pastebin | PrivateBin | Privnote |
|---|---|---|---|---|---|
| Self-hosted | ✓ | ✗ | ✗ | ✓ | ✗ |
| REST API + OpenAPI spec | ✓ | partial | ✗ | ✗ | ✗ |
| Content negotiation (HTML / JSON / plain from same URL) | ✓ | ✗ | ✗ | ✗ | ✗ |
| Burn-after-reading with grace period TTL | ✓ | ✗ | ✓ | ✓ | partial |
| Per-note private flag | ✓ | ✗ | ✗ | ✗ | ✗ |
| Built-in CLI + stdin | ✓ | partial | ✗ | ✗ | ✗ |
| Single binary, SQLite by default | ✓ | — | — | ✗ | — |

**The key feature:** one URL, many consumers. An AI agent hits `/notes/{id}` with `Accept: application/json` and gets structured data. A browser hits the same URL and gets rendered HTML. A shell script pipes to `padmark-cli` from stdin. No adapters, no glue.

---

## What it does

- Share Markdown (or plain text) notes via a short URL
- Edit or delete notes using a secret `edit_code` — no login required
- If you were signed in (session or API token) when you created a note, edit or delete it later with that same credential — no `edit_code` needed
- Make notes disappear after the first read (burn-after-reading)
- Keep notes private so only authenticated users can see them
- Gate access with user accounts protected by TOTP two-factor auth, or with API Bearer tokens
- Store note content encrypted at rest
- Script everything from the terminal or another service

**What it doesn't do:** team wikis, collaborative editing, document management. Intentionally lightweight.

---

## How notes work

Every note has:

| Field | Description |
|---|---|
| `slug` | Short URL identifier, auto-generated or custom |
| `title` | Note title |
| `content` | Markdown or plain text body |
| `edit_code` | Your one-time secret for editing and deleting |
| `burn_after_reading` | Delete the note on first read |
| `ttl` | Seconds to live *after* the first read (optional) |
| `private` | Require authentication to read |

> **Keep your `edit_code` safe.** It's shown once at creation and cannot be recovered. Exception:
> if you created the note while authenticated (session or API token), that same credential edits
> and deletes it without the `edit_code` — see [Editing without `edit_code`](#editing-without-edit_code-owners).

### Burn-after-reading modes

| Config | Behavior |
|---|---|
| `burn_after_reading: true` | Note is deleted the moment it's first read |
| `burn_after_reading: true` + `ttl: 3600` | First read starts a 1-hour countdown, then it's gone |

The timer starts on first read, not on creation.

---

## Quick start

### SQLite (simplest)

```bash
go run ./cmd/padmark-server --addr :4000
```

Open `http://localhost:4000`.

### PostgreSQL

```bash
go run ./cmd/padmark-server serve \
  --storage postgres \
  --dsn "postgres://user:pass@localhost:5432/padmark?sslmode=disable" \
  --addr :4000
```

### Docker

**SQLite (single container, data stored in a named volume):**

```bash
docker run -d \
  --name padmark \
  -p 8080:8080 \
  -v padmark_data:/data \
  -e PADMARK_DSN=/data/padmark.db \
  partydev/padmark:latest
```

Open `http://localhost:8080`.

**PostgreSQL (Compose):**

Save the file below as `docker-compose.yml` and run `docker compose up -d`.

```yaml
services:
  db:
    image: postgres:18-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: padmark
      POSTGRES_USER: padmark
      POSTGRES_PASSWORD: padmark
    volumes:
      - db_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U padmark -d padmark"]
      interval: 5s
      timeout: 5s
      retries: 10
    networks:
      - padmark

  padmark:
    image: partydev/padmark:latest
    command: serve
    ports:
      - "8080:8080"
    environment:
      PADMARK_STORAGE: postgres
      PADMARK_DSN: "postgres://padmark:padmark@db:5432/padmark?sslmode=disable"
      PADMARK_ADDR: ":8080"
      PADMARK_LOG_FORMAT: json
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped
    networks:
      - padmark

networks:
  padmark:

volumes:
  db_data:
```

This file is also included in the repo as [`docker-compose.yml`](docker-compose.yml).

> **Change the database password** before deploying to a non-local environment — replace `padmark` in both `POSTGRES_PASSWORD` and the DSN string.

---

## Three ways to use padmark

### 1. Web UI

Go to `/`, write your note, optionally set a title and slug, hit **Publish**.
You'll get a URL and your `edit_code`. Done.

---

### 2. CLI

Install:

```bash
go install github.com/partyzanex/padmark/cmd/padmark-cli@latest
```

Common commands:

```bash
# Create a note from a file
padmark-cli create --file note.md

# Create from stdin
echo "# Hello" | padmark-cli create

# Custom slug and title (server must run with --custom-slugs; see Configuration reference)
padmark-cli create --title "Runbook" --content "..." --slug deploy-notes

# Burn-after-reading with a 1-hour window after first read
padmark-cli create --title "Secret" --content "eyes only" --burn --ttl 3600

# Private note: reading it requires a bearer token or an authenticated session
# (any signed-in caller, not just the creator — see "API keys" below)
padmark-cli create --title "Internal" --content "..." --private

# Plain text instead of markdown
padmark-cli create --title "Log dump" --content "..." --plain

# Fetch a note (JSON, raw, or default)
padmark-cli get abc123def4
padmark-cli get --raw abc123def4
padmark-cli get --json abc123def4

# Update a note
padmark-cli edit abc123def4 --edit-code JmNkn0LdjbMw --file updated.md

# Delete a note
padmark-cli delete abc123def4 --edit-code JmNkn0LdjbMw

# Edit/delete WITHOUT --edit-code: works if --token identifies the note's creator
# (see "Editing without edit_code" below) — no --edit-code needed at all
padmark-cli --token <envelope-token> edit abc123def4 --file updated.md
padmark-cli --token <envelope-token> delete abc123def4

# Check server health
padmark-cli ping

# Bound each request (default 30s; 0 disables)
padmark-cli --timeout 5s get abc123def4
```

Set defaults via environment variables: `PADMARK_URL`, `PADMARK_TOKEN`, `PADMARK_TIMEOUT`, `PADMARK_EDIT_CODE`.

> A bearer token over a non-HTTPS `--url` is sent in cleartext; the CLI prints a stderr warning. Use an `https://` server URL in production.

---

### 3. REST API

**Create a note**
```bash
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{"title": "Hello", "content": "# Hello\n\nThis is **padmark**."}'
```

**Read a note** — content negotiation based on `Accept` header:
```bash
curl http://localhost:4000/notes/{id}                        # JSON (default)
curl -H "Accept: text/html"  http://localhost:4000/notes/{id}  # Rendered HTML
curl -H "Accept: text/plain" http://localhost:4000/notes/{id}  # Raw Markdown
curl http://localhost:4000/{id}                              # Short URL for browsers
```

**Update a note**
```bash
curl -X PUT http://localhost:4000/notes/{id} \
  -H "Content-Type: application/json" \
  -d '{"title": "Updated", "content": "# New content", "edit_code": "JmNkn0LdjbMw"}'
```

**Delete a note**
```bash
curl -X DELETE http://localhost:4000/notes/{id} \
  -H "X-Edit-Code: JmNkn0LdjbMw"
```

**Update or delete without `edit_code`** — if the note was created by this same authenticated
caller, `edit_code` can be omitted entirely; a Bearer token or session cookie is enough (see
[Editing without `edit_code`](#editing-without-edit_code-owners)):
```bash
curl -X PUT http://localhost:4000/notes/{id} \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <api-key>" \
  -d '{"title": "Updated", "content": "# New content"}'

curl -X DELETE http://localhost:4000/notes/{id} \
  -H "Authorization: Bearer <api-key>"
```

**Burn-after-reading**
```bash
# Delete on first read
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{"title": "Secret", "content": "eyes only", "burn_after_reading": true}'

# Expire 1 hour after first read
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{"title": "Secret", "content": "eyes only", "burn_after_reading": true, "ttl": 3600}'
```

Interactive API docs live at `/api`. Raw OpenAPI spec at `/api/openapi.yaml`.

---

## Authentication & accounts

padmark has two independent auth mechanisms. Use either, both, or neither.

### Bearer tokens (API / CLI)

> **Deprecated.** `--auth-tokens` / `PADMARK_AUTH_TOKENS` is the legacy bearer-token write auth.
> Use the **TOTP account system** (`--enable-accounts`) and issue an admin **API key** from
> `/admin` instead. The flag is kept for backwards compatibility and will be removed in a future
> release.
>
> When set, **write operations** (create, update, delete) require `Authorization: Bearer <token>`.
> Public notes stay readable without a token.

### API keys (TOTP account system)

With `--enable-accounts=true`, an admin can issue a long-lived API key for their own account from
`/admin` → **API keys** → **Create key**. The key is shown **once** in plaintext on that page; only
its SHA-256 hash is stored (`api_tokens.token_hash`, primary key). The same page lists all keys
(hash prefix, owner, created / last-used) and lets the admin revoke them.

The value shown on that page is an **envelope token**: the raw key packed together with the
server's own base URL (scheme + host). Pass it straight to `--token` / `PADMARK_TOKEN` and the CLI
picks up the server URL from the token itself — no `--url` needed:

```bash
padmark-cli --token <envelope-token> create --title "hello" --content "world"
```

An explicit `--url` (or `PADMARK_URL`) always overrides the URL embedded in the token. A bare
bearer key (no embedded URL) still works exactly as before — just pair it with `--url`.

The CLI resolves the bearer token in this order (first non-empty wins):

1. `--token` flag
2. `PADMARK_TOKEN` env var
3. `~/.config/padmark/token` (honours `XDG_CONFIG_HOME`; contents are trimmed)

Only the raw key (never the envelope wrapper) is sent as `Authorization: Bearer <key>` on every
request. On the server, the auth middleware resolves it via `Manager.ResolveAPIToken` after the
session-cookie check; an unknown, revoked, or expired key falls through to 401 / login redirect.

> Sending a token to a non-HTTPS server logs a cleartext-exposure warning (advisory, not blocking);
> use an `https://` server URL in production.

### User accounts with TOTP 2FA (web)

**Off by default — the site is fully public** (read and create notes, no login). Enable the account system with `--enable-accounts` / `PADMARK_ENABLE_ACCOUNTS=true`:

```bash
PADMARK_ENABLE_ACCOUNTS=true go run ./cmd/padmark-server serve
```

Once enabled, on first run (no users yet) padmark logs:

```
No users found. Open /setup to create the first admin.
```

When **disabled**, none of the routes below are gated and `/setup`, `/admin`, etc. return `404`.

- **`/setup`** — create the first admin (username + password). A TOTP QR code is shown **once** — scan it into your authenticator app.
- **`/login`** — sign in with username + password + a 6-digit TOTP code.
- **`/admin`** — admins issue single-use **invite links**, issue/revoke **API keys** (Bearer tokens for the CLI), and revoke users. Revoking a user also clears their sessions, invites, and API keys.
- New users onboard via an invite link, choose their own password, and scan their own TOTP QR.
- **`/change-password`** — rotate the password (requires the current password **and** a TOTP code).
- Sessions are cookie-based; lifetime via `--session-ttl` (default 30 days).

**Private notes** require either a valid session (browsers are redirected to `/login`) or a Bearer token (API clients get `401`).

Always public regardless of auth config: `/login`, `/setup`, `/logout`, `/totp-login`, `/static/*`, `/api`, `/api/openapi.yaml`, `/healthz`, `/readyz`.

### Editing without `edit_code` (owners)

If you were authenticated — a session cookie (web UI) or an API Bearer token (CLI/API) — at the
moment you **created** a note, that exact same credential can edit or delete it later with no
`edit_code` at all. This only requires `--enable-accounts=true`; it works whether the note is
public or `private`.

- **Strictly owner-scoped.** The bypass checks the exact user who created the note — not "any
  signed-in user" (unlike `private`, see above), and not admins either. A different authenticated
  user still needs the `edit_code`, exactly like an anonymous caller.
- **Anonymous notes are unaffected.** A note created without being signed in has no recorded
  owner, so `edit_code` is always required for it, from everyone — this matches the behavior
  before this feature existed.
- **Web UI:** the edit page ([`/edit/{id}`](#1-web-ui)) still shows the edit-code field, but
  leaves it optional for the note's owner — save without filling it in.
- **CLI/API:** just omit `--edit-code` / the `edit_code` field; the Bearer token or session does
  the rest (see the CLI and REST API examples above).

### Security at rest

- Note content is **encrypted at rest**.
- Passwords and `edit_code`s are hashed with **argon2id**.
- Each user's TOTP secret is encrypted under a key derived from their password.
- TOTP codes are **single-use** (replay-protected) and the protection survives server restarts.

---

## Configuration reference

### Server

| Flag | Env var | Default | Description |
|---|---|---|---|
| `--addr` | `PADMARK_ADDR` | `:8080` | Listen address |
| `--storage` | `PADMARK_STORAGE` | `sqlite` | `sqlite` or `postgres` |
| `--dsn` | `PADMARK_DSN` | `padmark.db` | DB path or connection string |
| `--auth-tokens` | `PADMARK_AUTH_TOKENS` | — | **Deprecated.** Legacy comma-separated Bearer tokens for write endpoints; superseded by the TOTP account system + admin API keys. Will be removed in a future release |
| `--enable-accounts` | `PADMARK_ENABLE_ACCOUNTS` | `false` | Enable the TOTP account system (`/setup`, `/login`, `/admin`, private-note gating); off = fully public |
| `--totp-issuer` | `PADMARK_TOTP_ISSUER` | `padmark` | TOTP issuer shown in the authenticator app |
| `--session-ttl` | `PADMARK_SESSION_TTL` | `2592000` | Session lifetime in seconds (default 30 days) |
| `--argon2-memory` | `PADMARK_ARGON2_MEMORY` | `24576` | argon2id memory cost in KiB for **account password** hashing (default 24 MiB); lower for low-RAM hosts. Edit codes use a fast hash and are unaffected |
| `--argon2-time` | `PADMARK_ARGON2_TIME` | `2` | argon2id iterations (OWASP minimum at 64 MiB) — raise when lowering memory |
| `--argon2-threads` | `PADMARK_ARGON2_THREADS` | `1` | argon2id parallelism (CPU threads per hash) |
| `--cookie-max-age` | `PADMARK_COOKIE_MAX_AGE` | `7776000` | Auth cookie max-age in seconds (default 90 days) |
| `--rate-limit` | `PADMARK_RATE_LIMIT` | `10` | Requests/sec per IP (`0` = disabled) |
| `--rate-burst` | `PADMARK_RATE_BURST` | `20` | Burst size per IP |
| `--read-timeout` | `PADMARK_READ_TIMEOUT` | `30` | HTTP read timeout in seconds |
| `--write-timeout` | `PADMARK_WRITE_TIMEOUT` | `60` | HTTP write timeout in seconds |
| `--max-header-bytes` | `PADMARK_MAX_HEADER_BYTES` | `65536` | Max request header size in bytes (64 KB) |
| `--max-body-bytes` | `PADMARK_MAX_BODY_BYTES` | `4194304` | Max request body size in bytes (4 MB) |
| `--tls-cert` | `PADMARK_TLS_CERT` | — | TLS certificate path |
| `--tls-key` | `PADMARK_TLS_KEY` | — | TLS private key path |
| `--http-redirect-addr` | `PADMARK_HTTP_REDIRECT_ADDR` | — | HTTP → HTTPS redirect listener (TLS only) |
| `--allowed-hosts` | `PADMARK_ALLOWED_HOSTS` | — | Host allowlist for the HTTP→HTTPS redirect; non-listed hosts get `400` (empty = redirect to request Host) |
| `--log-level` | `PADMARK_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `--log-format` | `PADMARK_LOG_FORMAT` | `json` | `json` or `text` |
| `--trusted-proxies` | `PADMARK_TRUSTED_PROXIES` | — | Proxy CIDRs/IPs for real client IPs; also the addresses whose `X-Forwarded-Proto` is trusted when building absolute links (note URLs, invite links, API-token envelopes) |
| `--public-scheme` | `PADMARK_PUBLIC_SCHEME` | — | Force `http` or `https` for absolute links back to this server, bypassing TLS/`X-Forwarded-Proto` detection; use this instead of `--trusted-proxies` when the proxy's IP isn't stable (cloud load balancers, k8s ingress) |
| `--custom-slugs` | `PADMARK_CUSTOM_SLUGS` | `false` | Allow user-supplied human-readable slugs (e.g. `project/GUIDE.md`) at creation. Off by default: only random slugs are issued, since a custom slug is low-entropy content-encryption key material. Enable for internal/trusted deployments. A slug's first path segment must not be a reserved route name: `login`, `setup`, `logout`, `totp-login`, `admin`, `api`, `success`, `healthz`, `readyz`, `notes`, `edit`, `change-password`, `static` |

### CLI global flags

| Flag | Description |
|---|---|
| `--url`, `-u` | Padmark server URL (env: `PADMARK_URL`, default `http://localhost:8080`) |
| `--token` | Bearer token for authentication (env: `PADMARK_TOKEN`; falls back to `~/.config/padmark/token`) |
| `--timeout` | Per-request HTTP timeout; `0` disables it (env: `PADMARK_TIMEOUT`, default `30s`) |

---

## Development

```bash
make build    # compile
make test     # run tests
make cover    # generate HTML coverage report
make lint     # run linter
make gen      # regenerate mocks (mockgen)
```

---

## Stack

Go · [ogen](https://github.com/ogen-go/ogen) · [goldmark](https://github.com/yuin/goldmark) · [bluemonday](https://github.com/microcosm-cc/bluemonday) · [bun](https://github.com/uptrace/bun) · [goose](https://github.com/pressly/goose) · SQLite / PostgreSQL