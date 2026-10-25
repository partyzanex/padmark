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
| Per-note privacy levels (public / login required / owner only) | ✓ | ✗ | ✗ | ✗ | ✗ |
| Built-in CLI + stdin | ✓ | partial | ✗ | ✗ | ✗ |
| Single binary, SQLite by default | ✓ | — | — | ✗ | — |

**The key feature:** one URL, many consumers. An AI agent hits `/notes/{id}` with `Accept: application/json` and gets structured data. A browser hits the same URL and gets rendered HTML. A shell script pipes to `padmark-cli` from stdin. No adapters, no glue.

---

## What it does

- Share Markdown (or plain text) notes via a short URL
- Edit or delete notes using a secret `edit_code` — no login required
- If you created a note while signed in, edit or delete it later from any session or API token for that same account — no `edit_code` needed
- Make notes disappear after the first read (burn-after-reading)
- Choose who can read a note: anyone, any logged-in user, or only its owner
- Gate access with user accounts protected by TOTP two-factor auth, or with API Bearer tokens
- Store note content encrypted at rest
- Shorten URLs too — the same edit_code/ownership/privacy/burn model as notes, redirecting either instantly or through a transparency interstitial
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
| `ttl` | On create: post-read grace period with burn. On update without burn: seconds until hard expiry |
| `privacy` | Who can read it: `public` (default), `authenticated`, or `owner` |

> **Keep your `edit_code` safe.** It's shown once at creation and cannot be recovered. Exception:
> if you created the note while authenticated, any session or API token for that account edits
> and deletes it without the `edit_code` — see [Editing without `edit_code`](#editing-without-edit_code-owners).

### Burn-after-reading modes

| Config | Behavior |
|---|---|
| `burn_after_reading: true` | Note is deleted the moment it's first read |
| `burn_after_reading: true` + `ttl: 3600` | First read starts a 1-hour countdown, then it's gone |

The timer starts on first read, not on creation.

Only an intentional read burns a note. Browsers get a "reveal" confirmation page first; API
clients must name the format in `Accept` (`application/json`, `text/markdown`, `text/plain`).
A burn-after-reading note requested with only `*/*`, with `HEAD`, as a browser subresource
(`<img src>`, `<iframe>`), or at the short `/{id}` URL without `Accept` is answered `409`
(`code: burn_read_not_explicit`)
and stays intact — so link unfurlers, mail scanners and embeds in other notes cannot destroy it.
The editor (`/edit/{id}`) never shows a burn-after-reading note's content and answers `410` for
an expired note. `padmark-cli` and `padmark-mcp` always send `Accept: application/json`. The
same rule protects `burn_after_click` links on `GET /r/{id}`: only a browser navigation or an
explicit `Accept` follows (and burns) one; previews, scanners, `HEAD` and embeds get `409`.
For v0.1.6 clients, `GET /notes/{id}` with no `Accept` header at all still counts as a read.

---

## How short links work

Every short link has:

| Field | Description |
|---|---|
| `id` | Short URL identifier, auto-generated or custom — shares one global namespace with note slugs (a note and a link can never collide on the same identifier) |
| `target_url` | Destination URL. Only `http` and `https` schemes are accepted |
| `edit_code` | Your one-time secret for updating or deleting the link |
| `burn_after_click` | Delete the link the moment it's first followed |
| `ttl` | Seconds until the link expires, counted from creation (a plain hard expiry — unlike notes' `ttl`, unrelated to `burn_after_click`; the two may be combined freely) |
| `privacy` | Who can follow it: `public` (default), `authenticated`, or `owner` |
| `direct_redirect` | Per-link override of the redirect mode; omitted inherits the default below. Ignored for anonymously created links unless the operator enabled `--direct-redirect` |

Visiting `GET /r/{id}` either redirects immediately (`301`) or shows a brief transparency
interstitial with the destination and a manual continue link first. **By default a link created
anonymously gets the interstitial** (so the instance cannot be used as a free open redirector),
while a link created by a signed-in owner (session or API token) redirects directly. The operator
can restore the old behaviour — `301` for every link — with `--direct-redirect=true` (see
[Configuration reference](#server)); a link's `direct_redirect` at creation or update time then
overrides that, and for an owner's links it can opt back into the interstitial. In every mode an
expired link answers `410 Gone`, a missing one `404`, and a link denied by its `privacy` level
`401` (or a login redirect for a browser), exactly like notes.

> Same rule as `edit_code` for notes: shown once at creation, cannot be recovered. The same
> owner-bypass also applies — a link created while authenticated can be updated or deleted later by that same
> credential with no `edit_code` needed.

Frequently-followed public links use a small in-memory cache of decrypted destination URLs.
Every read still fetches current metadata from the shared database: privacy, ownership, expiry,
burn settings and counters are never cached. A cached target is reused only when its ciphertext
fingerprint matches that database read. Updates and deletes therefore take effect on the next
read by any replica, without waiting for the cache's 30-second TTL. Private, expired and
burn-after-click links bypass the cache. The cache saves decryption work; it does not remove
the database read or cache authorization decisions.

---

## Quick start

### SQLite (simplest)

```bash
go run ./cmd/padmark-server serve --addr :8080
```

Open `http://localhost:8080`. On a fresh file-backed SQLite installation the server creates
`padmark.db` and an installation keyring, `padmark-keys.json`, beside it. Keep both across
restarts and back up the keyring separately from the database. Losing the keys makes the
encrypted content unreadable. All replicas sharing a database need the same keyring.

An explicit `PADMARK_CONTENT_KEYRING_FILE` / `--content-keyring-file` overrides that default;
an explicitly named file must already exist. SQLite in-memory/temporary databases and
PostgreSQL require an explicit keyring. Existing installations should follow
[the migration workflow](#content-key-management-migration-and-rotation) before serving.

The SQLite database holds password hashes, sealed TOTP secrets, token hashes and encrypted
notes. The server creates it with mode `0600` and attempts to tighten an existing file, and
any leftover `-wal`/`-shm`/`-journal` sidecar, to `0600` on every start; a failed permission
change is logged as a warning. SQLite creates new sidecars with the same mode.
Run the service under `umask 077` and keep its data directory private to the service
account. Windows ACLs are not managed.

### Explicit keyring

Create this file **once**, outside the repository/build context, when using PostgreSQL or
when you want to choose the keyring location yourself:

```bash
install -d -m 700 "$HOME/.config/padmark"
export PADMARK_CONTENT_KEYRING_FILE="$HOME/.config/padmark/content-keys.json"
go run ./cmd/padmark-server encryption keygen \
  --key-id k1 --output "$PADMARK_CONTENT_KEYRING_FILE"
```

The command refuses to overwrite an existing file. Keep this file for subsequent starts and
back it up separately from the database: losing its keys makes new encrypted content unreadable.
You can also pass its path with `--content-keyring-file`. The server rejects a missing or
malformed explicit keyring, or one readable by
group or other users, before opening the DB; keep the file at mode `0600` or `0400`.

Only `serve` creates a missing default SQLite keyring. The `encryption` commands never create
one implicitly: use `encryption keygen` before an upgrade's first `encryption migrate` if the
old installation has never had a keyring.

### PostgreSQL

```bash
PADMARK_STORAGE=postgres \
PADMARK_DSN="postgres://user:pass@localhost:5432/padmark?sslmode=disable" \
  go run ./cmd/padmark-server serve --addr :8080
```

Create and export the keyring path as shown above before running this command.

### Docker

**SQLite (single container, data stored in a named volume):**

```bash
docker run -d \
  --name padmark \
  -p 8080:8080 \
  -v padmark_data:/data \
  --mount "type=bind,src=$PADMARK_CONTENT_KEYRING_FILE,dst=/run/secrets/content-keys.json,readonly" \
  -e PADMARK_CONTENT_KEYRING_FILE=/run/secrets/content-keys.json \
  -e PADMARK_DSN=/data/padmark.db \
  partydev/padmark:latest
```

Open `http://localhost:8080`.

The image runs as UID/GID `65534`. Give that UID read access to the mounted key file;
on Linux, use a dedicated copy owned by `65534:65534` with mode `0400`, and set the host's
`PADMARK_CONTENT_KEYRING_FILE` to that copy. Keep it outside the build context and never bake
the key into the image. The same file-permission requirement applies to Compose secrets below.
Keyrings under `/run/secrets/` are exempt from the owner-only mode check (Compose bind-mounts a
file secret with the host file's mode and Swarm defaults to `0444`); startup logs a warning
instead of refusing, so still restrict the host file yourself.

When you give the container a memory limit (`--memory 256m`), also set `GOMEMLIMIT` to about
75–80% of it (`-e GOMEMLIMIT=200MiB`) and size `PADMARK_ARGON2_MEMORY_BUDGET` to match. The
image sets no `GOMEMLIMIT` because it cannot know your limit; see
[Memory sizing](#memory-sizing-argon2id).

**PostgreSQL (Compose):**

Save the file below as `docker-compose.yml`, set `POSTGRES_PASSWORD` and the host
`PADMARK_CONTENT_KEYRING_FILE` path in `.env`, then run `docker compose up -d`.
The bundled Compose file defaults to `partydev/padmark:latest`. Set `DOCKER_IMAGE` and
`DOCKER_TAG` to the repository/tag you actually publish. `make docker-build` and CI currently
fall back to `partyzanex/padmark` unless overridden; for the Compose default, build with
`make docker-build DOCKER_IMAGE=partydev/padmark` and configure the CI repository variable
`DOCKER_IMAGE` to match when publishing there.

```yaml
services:
  db:
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: padmark
      POSTGRES_USER: padmark
      # Compose fails fast when unset; the same variable feeds the padmark DSN below.
      POSTGRES_PASSWORD: "${POSTGRES_PASSWORD:?Set POSTGRES_PASSWORD in .env or the environment}"
    volumes:
      - db_data:/var/lib/postgresql/data
    # Loopback-only: reachable for local debugging from the host, never from other machines.
    ports:
      - "127.0.0.1:5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U padmark -d padmark"]
      interval: 5s
      timeout: 5s
      retries: 10
    networks:
      - padmark

  padmark:
    image: ${DOCKER_IMAGE:-partydev/padmark}:${DOCKER_TAG:-latest}
    command: serve
    ports:
      - "8080:8080"
    environment:
      PADMARK_STORAGE: postgres
      PADMARK_DSN: "postgres://padmark:${POSTGRES_PASSWORD}@db:5432/padmark?sslmode=disable"
      PADMARK_ADDR: ":8080"
      PADMARK_LOG_FORMAT: json
      PADMARK_CONTENT_KEYRING_FILE: /run/secrets/padmark_content_keys
    secrets:
      - padmark_content_keys
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped
    networks:
      - padmark

networks:
  padmark:

secrets:
  padmark_content_keys:
    file: ${PADMARK_CONTENT_KEYRING_FILE:?Set the host keyring file path}

volumes:
  db_data:
```

This file is also included in the repo as [`docker-compose.yml`](docker-compose.yml).

Compose requires `POSTGRES_PASSWORD`; that value is used for both PostgreSQL and the app DSN.
The named `db_data` volume preserves database files when containers are recreated.

---

## Four ways to use padmark

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

# Custom slug and title (refused if the server runs with --custom-slugs=false; see Configuration reference)
padmark-cli create --title "Runbook" --content "..." --slug deploy-notes

# Burn-after-reading with a 1-hour window after first read
padmark-cli create --title "Secret" --content "eyes only" --burn --ttl 3600

# Requires login to read (any signed-in caller, not just the creator — see "API keys" below)
padmark-cli create --title "Internal" --content "..." --privacy authenticated

# Owner-only: readable through sessions/API tokens for the account that created it
padmark-cli create --title "Just for me" --content "..." --privacy owner

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

Short links:

```bash
# Create a short link
padmark-cli shorten --target-url https://example.com/some/long/path

# Custom slug, burn after first click, expires in 1 hour
padmark-cli shorten --target-url https://example.com --slug my-link --burn --ttl 3600

# Force this one link to always show the interstitial, even when it would redirect directly
padmark-cli shorten --target-url https://example.com --direct-redirect=false

# Get a link's metadata (does not follow it or consume burn-after-click)
padmark-cli expand my-link

# Delete a link
padmark-cli delete-link my-link --edit-code MySecretCode1
```

Set defaults via environment variables: `PADMARK_URL`, `PADMARK_TOKEN`, `PADMARK_TIMEOUT`, `PADMARK_EDIT_CODE`.

> A bearer token (or the `setup` password) over a non-HTTPS `--url` travels in cleartext. For a
> loopback host (`localhost`, `127.0.0.0/8`, `::1`) the CLI only prints a stderr warning; for any
> other host it **refuses** unless you pass `--allow-insecure-http` / `PADMARK_ALLOW_INSECURE_HTTP=true`.
> Use an `https://` server URL in production.

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
curl http://localhost:4000/notes/{id}                        # JSON (default; 409 for burn notes)
curl -H "Accept: application/json" http://localhost:4000/notes/{id}  # JSON, explicit
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

> `DELETE` reads the edit code from the `X-Edit-Code` header. The `?edit_code=` query parameter
> is **deprecated**: it is used only when the header is absent, kept for v0.1.6 clients, and logs
> a warning. A query string ends up in reverse-proxy/CDN access logs and `Referer` headers, so
> send the header.

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

For `PUT /notes/{id}` without `burn_after_reading`, `ttl: N` sets a hard expiry N seconds
from the update, `ttl: null` removes expiry, and an omitted TTL preserves it. Disabling an
existing burn timer clears that timer's expiry. With `burn_after_reading: true`, TTL remains
a post-read grace period; omitted, null or 0 means immediate burn. All API TTLs are bounded
to 0–9223372036 seconds.

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

Interactive API docs live at `/api` (Redoc, vendored into the binary and served from `/static/` — no
CDN fetch, works offline). Raw OpenAPI spec at `/api/openapi.yaml`. A short Markdown
overview of the machine surface, written for agents in the llms.txt convention, at `/llms.txt`.

**Short links**
```bash
# Create a short link
curl -X POST http://localhost:4000/links \
  -H "Content-Type: application/json" \
  -d '{"target_url": "https://example.com/some/long/path"}'

# Create up to 100 at once — best-effort, not transactional: one item failing (bad scheme, slug
# conflict, ...) doesn't stop the rest; check each result for an "error" field
curl -X POST http://localhost:4000/links/bulk \
  -H "Content-Type: application/json" \
  -d '{"links": [{"target_url": "https://example.com/a"}, {"target_url": "https://example.com/b"}]}'

# Get a link's metadata (JSON, does not follow it)
curl http://localhost:4000/links/{id}

# Update its destination without changing the slug, views, or edit code
curl -X PUT http://localhost:4000/links/{id} \
  -H "Content-Type: application/json" \
  -d '{"target_url":"https://example.com/new","edit_code":"MySecretCode1","ttl":3600}'

# Follow it — an HTML transparency interstitial for an anonymously created link (default),
# 301 for an owner's link or when the server runs with --direct-redirect=true
curl -i http://localhost:4000/r/{id}
# A burn_after_click link needs a deliberate access (otherwise 409, link intact):
curl -i -H "Accept: text/html" http://localhost:4000/r/{id}

# Delete a link
curl -X DELETE http://localhost:4000/links/{id} \
  -H "X-Edit-Code: MySecretCode1"
```

`GET /r/{id}` is a browser-facing redirect, not part of the JSON API spec — the same reason bare
`GET /{id}` (the note-view shortcut) isn't documented in `/api/openapi.yaml` either.

`PUT /links/{id}` requires `target_url`; an authenticated owner may omit `edit_code`.
Omitted `ttl`, `privacy`, and `direct_redirect` keep their current values. TTL is counted from
the update (`0` expires immediately, `null` removes expiry); `direct_redirect: null` restores
the server default. The slug, views, original edit code, owner, creation time, and
`burn_after_click` are preserved.

---

### 4. MCP server

`padmark-mcp` exposes the padmark surface as MCP tools for AI agents (Claude Code, Cursor, …).
It is a local stdio proxy: the agent spawns the binary, and the binary talks REST to your
running padmark server — credentials, edit codes, and privacy are enforced by the server, not
re-implemented here.

```bash
go install github.com/partyzanex/padmark/cmd/padmark-mcp@latest
```

Tools: `create_note`, `get_note`, `update_note`, `delete_note`, `create_link`,
`create_links_bulk`, `get_link`, `delete_link`, `check_server`.

Configure with the same credential and defaults as the CLI: `--token` / `PADMARK_TOKEN`
(a `pmk_…` envelope works — it carries the server URL too), `--base-url` / `PADMARK_URL`
(default `http://localhost:8080`), `--timeout` / `PADMARK_TIMEOUT`, `--allow-insecure-http` /
`PADMARK_ALLOW_INSECURE_HTTP` (the token is refused over plain HTTP to a non-loopback host unless set).

MCP logging uses `--log-level` / `PADMARK_LOG_LEVEL` (`debug`, `info`, `warn`, `error`);
logs go to stderr so stdout stays reserved for the protocol. Unknown levels fail startup.
Tool descriptions label note text, titles and destination URLs as untrusted data. That
framing is guidance to the consuming agent, not a sandbox: retrieved content never grants
permission to expose credentials, switch servers or perform unrelated operations.

Example `mcpServers` entry (Claude Code / Cursor JSON config):

```json
{
  "mcpServers": {
    "padmark": {
      "command": "/usr/local/bin/padmark-mcp",
      "env": { "PADMARK_TOKEN": "pmk_..." }
    }
  }
}
```

Notes for agents:
- `create_note`/`create_link` return an `edit_code` exactly once. By default the proxy keeps it
  for the session and the tool result only says so — pass `reveal_edit_code: true` to print it
  (it cannot be recovered later).
- `get_note` is not read-only: reads bump `views`, and the first read of a burn-after-reading
  note destroys it. `get_link` is read-only — burn-after-click fires on the browser redirect
  path, not on metadata.
- Machine-readable docs: `/llms.txt` (overview) and `/api/openapi.yaml` (full contract); both
  are served even without a token, and both go away with the REST surface under
  `--disable-api`.

---

## Authentication & accounts

padmark's auth is gated entirely behind the TOTP account system (`--enable-accounts`): off by
default (fully public), on enables sessions for the web UI and long-lived API keys for the CLI.

### API keys (TOTP account system)

With `--enable-accounts=true`, an admin can issue an API key for their own account from
`/admin` → **API keys** → **Create key**, choosing its lifetime in days (`0` = never expires). The
key is shown **once** in plaintext on that page; only its SHA-256 hash is stored
(`api_tokens.token_hash`, primary key). The same page lists all keys (hash prefix, owner,
created / expiry / issued-by / last-used) and lets the admin revoke them.

A leaked key must not be able to outlive the remedies, so:

- **A bearer key cannot multiply itself.** `POST /api/tokens` and `POST /api/invites` called with
  a key (not a browser session) require a step-up: the account password and a current TOTP code,
  replay-protected like a login. The admin panel (`/admin*`) accepts browser sessions only.
- **Issued keys follow their issuer.** A key minted with another key records it as its issuer;
  revoking a key revokes everything it issued, recursively.
- **Changing the password revokes every API key of the account** (and all other sessions).

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

> Sending a token to a non-HTTPS, non-loopback server is refused unless `--allow-insecure-http`
> (`PADMARK_ALLOW_INSECURE_HTTP`) is set; loopback hosts only log a cleartext-exposure warning.
> Use an `https://` server URL in production.

### User accounts with TOTP 2FA (web)

**Off by default — the site is fully public** (read and create notes, no login). Without accounts nothing can enforce
the `authenticated` and `owner` privacy levels: the editor offers only Public, the API answers `422` to them, and the
edit code alone authorises edits and deletion. The deprecated `private: true` of v0.1.6 is still accepted and, as in
v0.1.6, not enforced until accounts are enabled. Enable the account system with `--enable-accounts` /
`PADMARK_ENABLE_ACCOUNTS=true`:

```bash
PADMARK_ENABLE_ACCOUNTS=true go run ./cmd/padmark-server serve
```

Creating the first admin requires proof that you own the server: set
**`PADMARK_BOOTSTRAP_TOKEN`** (at least 32 characters, environment only — never a flag; shorter
fails startup) and enter it in the `/setup` form. Without it, first-admin setup is **disabled**:
otherwise whoever reached `/setup` first — say an internet scanner, a minute before you — would
become the single, permanent admin. The token is never logged; unset it once the admin exists.

```bash
export PADMARK_BOOTSTRAP_TOKEN="$(openssl rand -hex 32)"
PADMARK_ENABLE_ACCOUNTS=true go run ./cmd/padmark-server serve
```

Once enabled, on first run (no users yet) padmark logs one of:

```
No users found. Open /setup and enter the bootstrap token to create the first admin; unset it afterwards
No users found, but first-admin setup is disabled: set the bootstrap token env (at least 32 characters) and restart, then open /setup
```

When **disabled**, none of the routes below are gated and `/setup`, `/admin`, etc. return `404`.

- **`/setup`** — create the first admin (username + password + the bootstrap token). A TOTP QR code is shown **once** — scan it into your authenticator app.
- **`/login`** — sign in with username + password + a 6-digit TOTP code.
- **`/admin`** — admins issue single-use **invite links**, issue/revoke **API keys** (Bearer tokens for the CLI), and revoke users. Revoking a user also clears their sessions, invites, and API keys. Browser sessions only — an API key cannot open the admin panel.
- New users onboard via an invite link, choose their own password, and scan their own TOTP QR.
- **`/change-password`** — rotate the password (requires the current password **and** a TOTP code). Signs out every other session and revokes all of the account's API keys.
- Sessions are cookie-based; lifetime via `--session-ttl` (default 30 days).

Invite links expire after 24 hours and can be used once. Only the base64url-encoded SHA-256
hash of each invite token is stored in the database. Migration `20261014000002` revokes
previously issued invites on both SQLite and PostgreSQL; admins must issue new links.
Rolling back this migration also revokes invites, since raw tokens cannot be recovered.

Note content is capped at 1,048,576 characters (`MaxContentLength`, published as `maxLength` in
the OpenAPI spec); a longer body is rejected with `400` by the generated request validation, or
`422` on the routes that decode the body by hand. The request-body limit (`--max-body-bytes`, 4 MB)
is unchanged and still bounds JSON overhead and bulk requests. Rendered HTML of repeatedly viewed
notes is cached in memory for up to two minutes (128 entries, never for burn-after-reading notes)
and dropped on update and delete.

Usernames are unique case-insensitively (`Admin` cannot be registered next to `admin`) and sign-in
matches them case-insensitively; accounts keep the spelling they registered with. Migration
`20261021000002` adds the unique index on `lower(username)`. If two existing accounts already
differ only by case, the migration **fails and changes nothing** — rename or remove one of them
(the error names the colliding usernames on PostgreSQL) and restart. Case folding follows the
database: SQLite folds ASCII only, PostgreSQL follows the database collation.

Burn-after-reading reveal tokens (the one-time confirmation token behind the "Reveal" button)
are stored the same way, as a SHA-256 digest only. Migration `20261021000001` deletes the
outstanding plaintext-era tokens on both backends, since their raw values cannot be recovered;
they live at most 10 minutes, so a reader mid-flow just reloads the note. The notes themselves
are untouched. Rolling back also drops the rows.

**`privacy: authenticated`** notes require either a valid session (browsers are redirected to
`/login`) or a Bearer token (API clients get `401`) — any signed-in caller, not just the creator.
**`privacy: owner`** notes go further: only the account that created the note may read it,
through any of that account's sessions or API tokens (see "Editing without `edit_code`" below
for the same owner-scoping applied to writes) —
meaningless without `--enable-accounts`, since a note has no recorded owner otherwise.

Secret CLI flag values (`--password`, `--token`, `--csrf-secret`) are visible in process
listings such as `ps`. Pipe passwords through stdin; use the token file or `PADMARK_TOKEN`
for tokens and `PADMARK_CSRF_SECRET` for the CSRF secret.

**The `edit_code` workflow works without signing in.** Anonymous callers may create notes/links
and, with the correct `edit_code`, edit or delete anything whose `privacy` level lets them read
it (`public`) — exactly as on a no-accounts instance. Mutations on `privacy: authenticated` or
`privacy: owner` notes answer `401` to an anonymous caller — the same rule reads follow — before
the edit code is even checked; a wrong code on a readable note is `403`.

Always public regardless of auth config: `/login`, `/setup`, `/logout`, `/totp-login`, `/static/*`, `/api`, `/api/openapi.yaml`, `/healthz`, `/readyz`.

### Headless provisioning (accounts)

Everything the `/admin` page does for the first admin is also available without a browser, for
CI/agent deployments and scripting:

- **`padmark-cli setup`** — bootstraps the first admin (or enrolls via an invite with
  `--invite <token>`): sends JSON to `POST /setup` and prints the `otpauth://` enrollment URI
  **exactly once** to stdout — scan it or save it, it cannot be retrieved later. Pass
  `--username/--password`, or pipe the password via stdin (an interactive TTY without
  `--password` is refused rather than echoing what you type). Bootstrapping the first admin
  forwards the server's bootstrap token from `PADMARK_BOOTSTRAP_TOKEN` in the CLI's
  environment; an invite enrollment needs none.
- **`padmark-cli create-token`** — `POST /api/tokens`: issues an API token **for the calling user
  themselves**. Because the CLI authenticates with a key, issuing needs a step-up: the account
  password (piped to stdin, or `--password`) and `--totp-code <current code>`. `--ttl-days N`
  sets the lifetime (`0`, the default, = never expires). Prints the `pmk_…` envelope once; store
  it in `~/.config/padmark/token` or `PADMARK_TOKEN` yourself. The new token is linked to the key
  used: revoking that key revokes it too. A user may hold at most 20 live tokens (`429` at the
  cap); revocation stays admin-panel-only.
- **`padmark-cli create-invite`** — `POST /api/invites` (admin only, same step-up): prints the
  single-use invite URL once; the invite expires after 24 h.
- The JSON variant of `POST /setup` is also documented for machine callers in `/llms.txt`:
  send `{"username", "password", "invite"?}` with `Accept: application/json` — `201` with
  `{"otp_auth_uri"}`, `403` once any account exists, `409` taken username, `410` spent invite,
  `422` weak password. Without `--enable-accounts` the token/invite operations answer `503`.

### Editing without `edit_code` (owners)

If you were authenticated — a session cookie (web UI) or an API Bearer token (CLI/API) — at the
moment you **created** a note, any session or API token for that same account can edit or delete it later with no
`edit_code` at all. This only requires `--enable-accounts=true`; it works regardless of the
note's `privacy` level.

- **Strictly owner-scoped.** The bypass checks the exact user who created the note — not "any
  signed-in user" (unlike `privacy: authenticated`, see above), and not admins either. A different
  authenticated user still needs the `edit_code`, exactly like an anonymous caller.
- **Anonymous notes are unaffected.** A note created without being signed in has no recorded
  owner, so `edit_code` is always required for it, from everyone — this matches the behavior
  before this feature existed.
- **Web UI:** the edit page ([`/edit/{id}`](#1-web-ui)) still shows the edit-code field, but
  leaves it optional for the note's owner — save without filling it in.
- **CLI/API:** just omit `--edit-code` / the `edit_code` field; the Bearer token or session does
  the rest (see the CLI and REST API examples above).

Failed edit-code attempts are tracked per resource and authenticated user ID, shared across
that user's sessions and API tokens. Ten failures lock that pair for five minutes. Anonymous
callers are tracked per resource and client IP (IPv6 grouped per /64), so an anonymous attacker
locks only their own address, not the resource's anonymous editor elsewhere. The client IP is
resolved like the rate limiter's: behind a proxy not listed in `--trusted-proxies`, every
anonymous caller appears as the proxy and they share one bucket again. Successful updates or
deletes reset only the calling bucket, so another caller's failures cannot block the
authenticated owner. The counters live in process memory; replicas do not share them.

Because the lockout throttles guessing per bucket (an attacker with many addresses gets many
buckets), a caller-chosen `edit_code` (`edit_code` on `POST /notes`/`POST /links`,
`--edit-code` on `create`/`shorten`, `edit_code` in the MCP create tools) must be 8–128
characters with no leading or trailing whitespace or control characters — shorter or longer is
rejected with `400`, the rest with `422`. Omit it to get a random 12-character code. Only newly
chosen codes are checked: notes and links created earlier with a shorter code keep working.

Sign-in (`POST /totp-login`) has two independent brute-force limits. Each client IP gets
10 attempts per minute, and each username gets 10 unsuccessful attempts, counted across all
IPs, before it is locked for five minutes from the last counted attempt. While locked, every
attempt for that name, including one with the correct password and code, gets `429` with
`Retry-After` and never reaches the password check. Usernames are matched after trimming
whitespace and ignoring case. Unknown usernames lock the same way, so the `429` does not reveal
whether an account exists. A successful sign-in resets the counter. Anyone who knows a username
can keep it locked by sending ten failures every five minutes. The lock lifts five minutes
after they stop, and existing sessions and API tokens keep working throughout.

### Security at rest

- Note content and short-link target URLs are **encrypted at rest**.
- New writes use the `pmc2$p$…` envelope: each write generates a random 256-bit data key,
  encrypts the payload with AES-256-GCM, and encrypts that data key with the active installation
  key. The record's hashed ID and resource type authenticate the ciphertext; slugs do not
  derive either encryption key. Titles and other metadata remain unencrypted.
- Existing unversioned ciphertext requires the explicit migration below before `serve` starts.
  Migration protects it with an outer `pmc2$l$…` envelope. Its inner legacy layer still needs
  the original slug; `convert` replaces that layer with native `kind=p` when the slug is known.
  Reads/Peek never migrate records. Updating a note writes native `kind=p`.
- The keyring JSON has an `active` key ID and a `keys` object mapping IDs to base64-encoded
  **32-byte random keys**. The DB holds an installation UUID and authenticated key checks,
  never raw keys. Before opening listeners, `serve` verifies the registry, active key and
  every current envelope; missing/substituted keys, raw rows, corrupt envelopes and incomplete
  migrations block startup. Scans use bounded batches, so startup time grows with the DB size.
  Native-only existing databases are registered after their envelopes are authenticated.
- GCM nonces are random 96-bit values, so collision probability grows with every seal under
  one key: keep the aggregate number of seals per installation key (across replicas,
  migrations, key checks and rewraps) well below `2^32`, and rotate keys before approaching
  that bound. This operational limit is not automatically counted.
  Native plaintext is limited to 64 MiB; legacy wrappers allow the additional base64 overhead.
  Existing dumps, WAL, freed pages and backups retain their old security properties; migration
  does not securely erase those copies. Titles and record metadata remain readable in the DB.
- Passwords and caller-chosen `edit_code`s use **argon2id**. Server-generated random
  edit codes retain salted SHA-256; existing fast hashes remain readable.
- Each user's TOTP secret is encrypted under an argon2id key derived from their password, and
  the stored value records the cost it was derived at (`v1$<memory>$<time>$<threads>$…`). That
  is what makes `--argon2-*` safe to retune on a live deployment: decryption uses the parameters
  saved with each secret, not the ones currently configured. Secrets written under an older cost
  (or the pre-argon2id scheme) are re-encrypted at the current cost on the owner's next
  successful login — no manual migration, and a failed rewrite never blocks the login.
- Stored Argon2 costs and operator configuration are bounded **before** any KDF allocation:
  at most 262144 KiB (256 MiB), 10 iterations and 16 threads, with positive values and
  memory >= 8 × threads. Stored password/edit-code hashes also require the expected salt
  and digest sizes. Malformed versioned TOTP envelopes fail without legacy fallback.
  Costs are rejected, never clamped; existing legitimate costs above these limits need a
  planned password/secret reset with a trusted older installation before upgrading.
- TOTP codes are **single-use** (replay-protected) and the protection survives server restarts.
- New usernames are case-sensitive, 1–64 Unicode letters/digits or `._@-`, starting with a
  letter/digit. Whitespace, control characters, path/query delimiters and invalid UTF-8 are
  rejected before any account or invite write. Existing usernames still work for login.
- A password-change revocation failure is logged at error level and shown explicitly in the
  browser. The replacement session remains usable, but older sessions may still exist;
  the administrator must revoke them. The page does not report full success in this case.
- The keyring is the root of trust for content at rest: anyone who can read it and the
  database can decrypt every note and link. Keyring files must be owner-only (`0600`/`0400`);
  a keyring readable by group or other users **refuses startup** (`serve` and the
  `encryption` commands). Keyrings under `/run/secrets/` (Docker/Compose secrets) are exempt.
  For other secret managers that cannot provide an owner-only file, pass
  `--allow-shared-keyring` (`PADMARK_ALLOW_SHARED_KEYRING=true`). Each exemption is logged at
  WARN through the configured logger, with the path and mode. Symlinks are allowed for
  managed secret mounts and are logged too. The check uses the opened file, never changes
  its mode and never logs keys. Protect both the target and its parent directories.
- At-rest GCM authenticates the resource, kind and record ID, **not freshness**: a database
  writer can restore an older valid ciphertext for the same record. Preventing rollback
  requires a trusted monotonic state outside that database. Database readers without the
  installation keyring cannot decrypt native content; this is not protection against a
  compromised server or changes to unencrypted metadata.
- Login still takes extra work after a correct password (secret opening and TOTP validation).
  The dummy password hash, common factor-error message and per-IP and per-username login limits
  reduce exposure; no constant-time end-to-end login guarantee is claimed.
- The HTTP-to-HTTPS redirect uses the request Host when `--allowed-hosts` is empty for
  backwards compatibility. Set an explicit allowlist (and a trusted proxy Host policy)
  on deployments where clients can reach that listener directly.


---

## Content key management, migration and rotation

Build the current server with `go build -o bin/padmark-server ./cmd/padmark-server`.
Commands use `PADMARK_STORAGE`, `PADMARK_DSN` and `PADMARK_CONTENT_KEYRING_FILE`, or the
corresponding flags before the encryption subcommand. SQLite and PostgreSQL use the same
workflow. Key bytes are never CLI arguments or command output.

**Maintenance window:** stop every server replica and other database writer before schema/data
migration, registration, activation, rewrap, conversion or retirement. Run one administrative
operation at a time. CAS protects individual rows against a racing update/delete, but these
commands do not acquire a deployment-wide maintenance lock. Running replicas keep their
in-memory keys and must be restarted with the resulting keyring before accepting writes.

For an existing installation, preserve a consistent database backup and its current keyring
separately. The encryption commands never generate a missing keyring. If the old installation
has never used content encryption, create one with `encryption keygen` before proceeding;
otherwise restore and use its existing keys. File-backed SQLite defaults to
`padmark-keys.json` beside the configured database; PostgreSQL needs an explicit path.
See [the v0.1.6 upgrade checklist](CHANGELOG.md#upgrade-checklist-from-v016) for configuration
changes and the known legacy-slug edge case. Then:

```bash
bin/padmark-server migrate
bin/padmark-server encryption status
bin/padmark-server encryption migrate --dry-run
bin/padmark-server encryption migrate
bin/padmark-server encryption status
```

Schema migrations create the registry tables; `status` and `--dry-run` never initialize or
change that registry. The schema must already exist for those commands. The data migration is
resumable and uses compare-and-swap. For previously encrypted records it wraps
`content`/`target_url` without changing metadata. Tagged v0.1.6 plaintext notes are converted
in place: their stored ID becomes `sha256(slug)`, content is encrypted, and the edit code is
hashed. Their public URL and usable edit code stay the same, as do counters, TTL/burn settings,
privacy and timestamps. Deleted rows are never recreated. Any error exits nonzero; incomplete
migrations block `serve` until a successful rerun.
JSON reports contain `plaintext`, `raw`, `wrapped`, `native`, `failed`, `corrupt_raw`, `untagged_legacy`, `pruned`, `changed`,
`conflicts`, `missing` and counts by key ID, without plaintext content. Only `prune --corrupt --dry-run` adds
`candidates` with record IDs (for an untagged v0.1.6 note, the ID is its slug). Raw validation checks structure only; the
old encryption tag cannot be authenticated without the original slug.

`plaintext` counts tagged, unconverted v0.1.6 notes; successful migration brings it to zero.
Migration `20261024000001` tags v0.1.6 notes whose custom slug is 64 lowercase hex characters:
their plaintext edit code does not have the full shape of a stored hash. An untagged note with
such a code (for example from an earlier build of this release) is counted as `untagged_legacy`:
it blocks `serve`, is never wrapped as ciphertext and is never pruned. A caller-chosen v0.1.6 code
of the exact hash shape cannot be told apart and shows as `corrupt_raw`. In both cases, list the
IDs and tag each v0.1.6 note explicitly before any pruning:

```bash
bin/padmark-server encryption prune --corrupt --dry-run   # "candidates": IDs with a reason
bin/padmark-server encryption mark-legacy --id <id>
bin/padmark-server encryption migrate
```

`mark-legacy` refuses a note with an envelope, and a note with structurally valid legacy
ciphertext and a hashed edit code: tagging those would encrypt ciphertext as plaintext.

Old databases may still contain expired plaintext from the era before content encryption.
Structurally invalid raw records are counted in both `failed` and `corrupt_raw`. Migration
initializes the registry as `pending` and wraps valid legacy records, but exits nonzero until
these invalid records are handled; `serve` stays blocked. After stopping all writers and
backing up the database, explicitly preview and remove these records:

```bash
bin/padmark-server encryption prune --corrupt --dry-run
bin/padmark-server encryption prune --corrupt
bin/padmark-server encryption migrate
bin/padmark-server encryption status
```

`prune --corrupt` permanently deletes corrupt raw values. A value is a versioned envelope only
if its first `$`-separated segment matches `pmc<digits>`; any other value (with or without `$`)
that is not structurally valid legacy base64 ciphertext is corrupt raw. It checks both notes and
links, including expired records; it never deletes tagged or untagged v0.1.6 plaintext notes,
structurally valid legacy ciphertext or versioned envelopes, and it refuses to run while any
untagged v0.1.6 note remains. Key, registry,
envelope-authentication and database errors abort the preflight without deletion. Compare-and-delete
preserves records whose content changed after scanning; conflicts are counted. Dry-run writes
nothing, including registry state. Pruning does not initialize the registry or finish a pending
migration; rerun `migrate` before serving. Partial cleanup is resumable; `pruned` counts committed
deletions. Versioned envelopes — including unknown future versions and malformed `pmc2$…` values —
are never selected by this command; they keep blocking startup until diagnosed separately.

When original slugs are available, feed one per line from a protected file or pipe:

```bash
bin/padmark-server encryption convert --dry-run < /secure/known-slugs.txt
bin/padmark-server encryption convert < /secure/known-slugs.txt
```

Both notes and links are checked. Missing records, decryption errors and CAS conflicts produce
counts and a nonzero exit; retrying is safe. Unknown slugs cannot be recovered from their stored
hashes: those records remain `wrapped`, protected by the installation key, until converted,
updated, expired or deleted through their normal lifecycle.

To rotate from `k1` to `k2`, keep all generated files outside the repository:

```bash
bin/padmark-server encryption key-add --key-id k2 \
  --output "$HOME/.config/padmark/content-keys.next.json"
export PADMARK_CONTENT_KEYRING_FILE="$HOME/.config/padmark/content-keys.next.json"
bin/padmark-server encryption register --key-id k2
# Distribute this file (both keys, k1 active) to every replica before switching writers.
bin/padmark-server encryption key-activate --key-id k2 \
  --output "$HOME/.config/padmark/content-keys.active.json"
export PADMARK_CONTENT_KEYRING_FILE="$HOME/.config/padmark/content-keys.active.json"
bin/padmark-server encryption rewrap --dry-run
bin/padmark-server encryption rewrap
bin/padmark-server encryption status
bin/padmark-server encryption key-remove --key-id k1 \
  --output "$HOME/.config/padmark/content-keys.current.json"
export PADMARK_CONTENT_KEYRING_FILE="$HOME/.config/padmark/content-keys.current.json"
# Distribute this final file and restart every replica with the same configuration.
```

`rewrap` preserves encrypted payload bytes and their nonces for both native and wrapped legacy
records; only the DEK wrapper changes. An interrupted rewrap is readable with both keys and can
be rerun. `key-remove` refuses the active key or a key referenced by live rows. Registry checks
for retired IDs remain, preventing silent reuse of an old ID with different key material.
Every key-file command creates a complete **0600** file without overwriting an existing path.
Container deployments must also grant their UID read access, as described under Docker.

Retain old keyrings separately for **old backups**, even after live records stop referencing
those keys. To verify restore, restore the database (including both encryption metadata tables)
and its matching keyring into an isolated installation, run schema migration if necessary,
then `encryption status` and a read of a known record. Wrong/missing keys must fail validation.
Do not delete registry rows to bypass that failure. SQLite file backups must include a consistent
WAL state (use the backup API or `VACUUM INTO`); PostgreSQL can use `pg_dump`/`pg_restore`.
Restoring a pre-migration backup also requires running `encryption migrate` before serving.
There is no destructive encryption down-migration, and older binaries cannot read the new data.

## Configuration reference

### Server

| Flag | Env var | Default | Description |
|---|---|---|---|
| `--addr` | `PADMARK_ADDR` | `:8080` | Listen address |
| `--storage` | `PADMARK_STORAGE` | `sqlite` | `sqlite` or `postgres` |
| `--dsn` | `PADMARK_DSN` | `padmark.db` | DB path or connection string. A Postgres DSN carries the password, and a command-line flag is visible to every local user in the process listing (`ps`) — pass it via `PADMARK_DSN` instead |
| `--content-keyring-file` | `PADMARK_CONTENT_KEYRING_FILE` | `<db dir>/padmark-keys.json` for file-backed SQLite | Persisted JSON keyring for note/link encryption. `serve` creates a missing default SQLite keyring; explicit paths must already exist. Required explicitly for PostgreSQL and in-memory/temporary SQLite. Encryption tools never create it implicitly. Back it up separately from the DB |
| `--skip-migration-backup` | `PADMARK_SKIP_MIGRATION_BACKUP` | `false` | Skip the automatic SQLite backup before applying pending schema migrations to an existing database. PostgreSQL logs a backup reminder; take `pg_dump` separately |
| `--allow-shared-keyring` | `PADMARK_ALLOW_SHARED_KEYRING` | `false` | Accept a keyring file readable by group or other users (logged at WARN) instead of refusing startup. Only for secret managers that cannot provide an owner-only file; keyrings under `/run/secrets/` are already exempt. Prefer mode `0600`/`0400` |
| `--enable-accounts` | `PADMARK_ENABLE_ACCOUNTS` | `false` | Enable the TOTP account system (`/setup`, `/login`, `/admin`, privacy-level gating); off = fully public |
| — | `PADMARK_BOOTSTRAP_TOKEN` | — | Environment only (never a flag). Required to create the first admin via `/setup` without an invite; at least 32 characters (shorter fails startup). Unset = first-admin setup disabled. Never logged; unset it once the admin exists |
| `--disable-api` | `PADMARK_DISABLE_API` | `false` | Disable the REST/JSON API (`/notes*`, `/links*`, `/api`, `/api/openapi.yaml`, `/llms.txt`, `/api/tokens`, `/api/invites`): the middleware returns `503` with `{"message":"API is disabled","code":"api_disabled"}`. Match `code` to identify the cause; outer middleware may reject a request first. The editor becomes **read-only**: viewing `/{id}` (including JSON/raw reads), `/r/{id}`, account/admin forms and health checks keep working, but publishing or saving from the editor fails with the API-disabled error, because the editor calls `POST /notes` and `PUT /notes/{id}` |
| `--totp-issuer` | `PADMARK_TOTP_ISSUER` | `padmark` | TOTP issuer shown in the authenticator app |
| `--session-ttl` | `PADMARK_SESSION_TTL` | `2592000` | Session lifetime in seconds (default 30 days) |
| `--csrf-secret` | `PADMARK_CSRF_SECRET` | — | HMAC key signing CSRF form tokens; raw bytes, **at least 32 characters** (a shorter value is rejected at startup). Empty (default) mints a fresh random secret per process — fine for a single instance, but restarts then invalidate open forms and multiple replicas `403` each other's form submits. Generate with `openssl rand -base64 32` and share the same value across replicas |
| `--argon2-memory` | `PADMARK_ARGON2_MEMORY` | `24576` | argon2id memory cost in KiB for **account passwords**, caller-chosen edit codes and the key wrapping each user's TOTP secret (default 24 MiB); lower for low-RAM hosts. Every derivation allocates this much; see [Memory sizing](#memory-sizing-argon2id). Server-generated edit codes use a fast salted SHA-256 instead |
| `--argon2-time` | `PADMARK_ARGON2_TIME` | `2` | argon2id iterations for new account-password hashes, caller-chosen edit-code hashes and TOTP-secret wrapping |
| `--argon2-threads` | `PADMARK_ARGON2_THREADS` | `1` | argon2id parallelism (CPU threads per hash) |
| `--argon2-memory-budget` | `PADMARK_ARGON2_MEMORY_BUDGET` | `131072` | Total argon2id memory in KiB that concurrent password, edit-code and TOTP-key derivations may hold at once (default 128 MiB); further requests queue in FIFO order. Must be at least `--argon2-memory` (startup fails otherwise); a single derivation larger than the budget still runs, alone. See [Memory sizing](#memory-sizing-argon2id) |
| `--rate-limit` | `PADMARK_RATE_LIMIT` | `10` | Requests/sec per IP (`0` = disabled) |
| `--rate-burst` | `PADMARK_RATE_BURST` | `20` | Burst size per IP |
| `--read-timeout` | `PADMARK_READ_TIMEOUT` | `30` | HTTP read timeout in seconds |
| `--write-timeout` | `PADMARK_WRITE_TIMEOUT` | `60` | HTTP write timeout in seconds |
| `--max-header-bytes` | `PADMARK_MAX_HEADER_BYTES` | `65536` | Max request header size in bytes (64 KB) |
| `--max-body-bytes` | `PADMARK_MAX_BODY_BYTES` | `4194304` | Max request body size in bytes (4 MB) |
| `--tls-cert` | `PADMARK_TLS_CERT` | — | TLS certificate path |
| `--tls-key` | `PADMARK_TLS_KEY` | — | TLS private key path |
| `--http-redirect-addr` | `PADMARK_HTTP_REDIRECT_ADDR` | — | HTTP → HTTPS redirect listener (TLS only) |
| `--allowed-hosts` | `PADMARK_ALLOWED_HOSTS` | — | Host allowlist, case-insensitive and port-less. On the main listener non-listed hosts get `421`, before any handler (`/healthz` and `/readyz` are exempt so IP-addressed probes keep working); on the HTTP→HTTPS redirect listener they get `400`. Empty = no Host check |
| `--log-level` | `PADMARK_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `--log-format` | `PADMARK_LOG_FORMAT` | `json` | `json` or `text` |
| `--trusted-proxies` | `PADMARK_TRUSTED_PROXIES` | — | Proxy CIDRs/IPs whose `X-Forwarded-For` / `X-Real-IP` is believed when resolving the real client IP; also the addresses whose `X-Forwarded-Proto` is trusted when building absolute links (note URLs, invite links, API-token envelopes). Set it to the proxy's own addresses and nothing wider — see [Getting `--trusted-proxies` wrong](#getting---trusted-proxies-wrong) |
| `--public-scheme` | `PADMARK_PUBLIC_SCHEME` | — | Force `http` or `https` for absolute links back to this server, bypassing TLS/`X-Forwarded-Proto` detection; `https` also marks session/CSRF cookies `Secure` and enables HSTS. Use this instead of `--trusted-proxies` when the proxy's IP isn't stable (cloud load balancers, k8s ingress) |
| `--custom-slugs` | `PADMARK_CUSTOM_SLUGS` | `true` | Allow user-supplied human-readable slugs/link aliases (e.g. `project/GUIDE.md`) at creation. On by default, as in v0.1.6; set `false` to issue only random slugs, because public human-readable URLs are easy to guess. New content keys are independent of the slug; existing legacy ciphertext keeps its old protection until migrated. A slug's first path segment must not be a reserved route name: `login`, `setup`, `logout`, `totp-login`, `admin`, `api`, `success`, `healthz`, `readyz`, `notes`, `edit`, `change-password`, `static`, `links`, `r`, and the exact segments `llms.txt` and `favicon.ico` |
| `--direct-redirect` | `PADMARK_DIRECT_REDIRECT` | `false` | `GET /r/{id}` behavior for short links. **Default changed from `true` to `false`:** links created anonymously now render an interstitial page (meta-refresh + `<noscript>` fallback) showing the destination, so the instance is not an open redirector; links created by a signed-in owner still get an immediate `301`. Set `true` to restore the `301` for every link |
| `--metrics-addr` | `PADMARK_METRICS_ADDR` | — | Address for a separate internal Prometheus `/metrics` listener (e.g. `:9090`); empty (default) disables it entirely. Exposes `padmark_links_created_total`, `padmark_links_redirects_total`, `padmark_links_collisions_total`, plus standard Go/process collectors. Bind this to a private interface only — metrics are never served on the main, public-facing listener |

#### Memory sizing (argon2id)

Every argon2id derivation allocates its full memory cost up front. New password hashes,
caller-chosen edit-code hashes and TOTP-secret wrapping use `--argon2-memory` (24 MiB by
default). Verification uses the cost recorded in each stored hash or secret; older records
may therefore still require 64 MiB or another accepted cost. Unknown usernames still pay for
a dummy check. Work can be triggered before a caller has authenticated, and the per-IP
rate limiter does not serialise it, so a handful of concurrent requests can allocate
hundreds of MiB.

`--argon2-memory-budget` caps the argon2id memory held by concurrent derivations; requests
beyond it wait their turn (bounded by the HTTP server timeouts). The default 128 MiB fits two
64 MiB or five 24 MiB derivations at once. Also set `GOMEMLIMIT` to about **75–80% of the
container memory limit**. You need both:

- **The budget alone is not enough.** A finished derivation's memory becomes garbage, and with
  the default `GOGC=100` the Go runtime lets the heap grow to about twice the live set before
  collecting it, so a 128 MiB budget can still reach ~256 MiB of heap during a burst.
- **`GOMEMLIMIT` alone is not enough.** It is a soft limit: the GC can only free memory that is
  no longer referenced. Without the budget, every concurrent derivation is live, the heap grows
  past the limit regardless, and the kernel OOM-kills the process.

Example for a 256 MB container (`docker run --memory 256m`, or a Kubernetes limit of `256Mi`):

```bash
GOMEMLIMIT=200MiB
PADMARK_ARGON2_MEMORY=19456         # 19 MiB for newly written hashes; default is 24576
PADMARK_ARGON2_MEMORY_BUDGET=65536  # 64 MiB; up to 131072 leaves less headroom for the rest
```

A smaller budget queues logins and edit-code requests sooner under load; a larger one needs a
correspondingly larger container. Keep the budget comfortably below `GOMEMLIMIT`.

#### Getting `--trusted-proxies` wrong

The per-IP rate limits (`--rate-limit`, and the fixed 10/min cap on TOTP login) are only as
good as the client IP they bucket by, and this flag is what decides where that IP comes from.
Both directions of misconfiguration have a cost, and neither surfaces as an error:

- **Set too wide** (`0.0.0.0/0`, or any range covering arbitrary clients) — every client is
  treated as a proxy, so its `X-Real-IP` header is believed verbatim. A client can then pick a
  fresh bucket for every request and the per-IP limits stop applying at all, TOTP login
  included. `X-Forwarded-For` happens to survive this (a chain of entirely trusted entries
  collapses back to the real peer address), but `X-Real-IP` is a single value with no chain to
  fall back on. The server logs a warning at startup for ranges this broad.
- **Left empty while a proxy is in front** — the forwarding headers are ignored, so every
  request is attributed to the proxy's own address and all clients share one bucket. Nothing
  is spoofable, but one noisy client throttles everyone else. The server logs a warning the
  first time a request arrives carrying a forwarding header while no proxy is trusted.

The per-username sign-in lock (10 failures per name per five minutes) does not depend on the
client IP, so it still holds under either misconfiguration.

Leaving it empty when the server really is exposed directly is correct and stays silent. If the
proxy's address is not stable (cloud load balancers, k8s ingress), prefer `--public-scheme https`
for the scheme half of the flag's job (links, `Secure` cookies, HSTS) rather than widening the
trusted range.

**Behind a TLS-terminating reverse proxy** the server itself sees plain HTTP, so it must be told
the public scheme: set `--trusted-proxies` to the proxy's addresses (so its `X-Forwarded-Proto`
is honoured) or `--public-scheme https`, and configure the proxy to preserve the original `Host`
header. Otherwise the same-origin check rejects browser `PUT`/`DELETE` on notes and links, the
account endpoints (`POST /api/tokens`, `POST /api/invites`) and every HTML form `POST` (login,
logout, change-password, admin, setup, burn-after-reading confirmation) with `403`, because the
browser's `https://` `Origin` does not match the scheme/host the server derived. Form `POST`s are
also refused when the browser reports `Sec-Fetch-Site` other than `same-origin`/`none`.
CSRF tokens are bound to the session cookie (a token minted before sign-in does not validate
for a signed-in `POST`; it is re-issued automatically on login, logout and password change). Session and CSRF cookies
get the `Secure` attribute, and responses carry HSTS, under any of the three: direct TLS, a
trusted proxy's `X-Forwarded-Proto: https`, or `--public-scheme https`.

`--csrf-secret` is the one flag a multi-replica deployment must set: without it each process
signs form tokens with its own random key, so a form rendered by one replica is rejected by the
next one that receives the POST. Leaving it empty logs a warning at startup rather than failing,
because a single instance works fine that way — the only cost is that forms opened before a
restart have to be reloaded.

Changing any `--argon2-*` value on a running deployment is safe: password hashes and encrypted
TOTP secrets each carry the cost they were written with, so existing accounts keep working. The
new cost applies to everything written from then on; an existing TOTP secret is rewritten at it
on its owner's next login, while an existing password hash keeps verifying under its recorded
cost until the password is changed. The change costs RAM and CPU per login, not access — see
[Security at rest](#security-at-rest).

### CLI global flags

| Flag | Description |
|---|---|
| `--url`, `-u` | Padmark server URL (env: `PADMARK_URL`, default `http://localhost:8080`) |
| `--token` | Bearer token for authentication (env: `PADMARK_TOKEN`; falls back to `~/.config/padmark/token`) |
| `--timeout` | Per-request HTTP timeout; `0` disables it (env: `PADMARK_TIMEOUT`, default `30s`) |
| `--allow-insecure-http` | Permit a token or setup password over plain HTTP to a non-loopback host (env: `PADMARK_ALLOW_INSECURE_HTTP`, default `false`) |

---

## Development

This checkout includes unreleased development; [CHANGELOG.md](CHANGELOG.md) separates the
current draft from tagged releases. References to `v0.1.6` describe historical migration
fixtures rather than the current release number.

Use Go 1.27.3 (see [`go.mod`](go.mod)). The application module and browser-test module have
separate dependencies and test commands:

```bash
make build          # compile binaries from cmd/
go test ./...       # application-module unit tests
make test           # application unit + integration tests, -race and coverage; Docker required
make cover          # application-module HTML coverage report
make lint           # application-module linter
make gen            # regenerate generated code and mocks

make test-ui-setup   # install Go browser-test dependencies, driver and Chromium
make test-ui         # 38 browser checks on desktop/mobile, including visual comparisons
make test-ui-headed  # same checks with a visible browser
make test-ui-debug   # Playwright Inspector
make test-ui-report  # open the last browser-test report
make test-ui-update  # update reference PNGs after an intentional UI change
```

Browser tests are Go code in a separate module under [`tests/ui`](tests/ui/README.md).
Their `go.mod`, `go.sum`, Makefile, PNG references, downloaded tools and reports stay there.
`go test ./...` and `make test` at the repository root do not include this nested module.
There is no npm/TypeScript setup; playwright-go downloads its internal Node.js driver itself.
For one scenario: `make test-ui UI_ARGS='-run TestEditor/desktop/Toolbar'`.

CI runs lint, application unit tests, integration tests and browser tests before publishing
the container image. Browser tests run in the pinned Playwright Linux image matching the Go
binding and retain failure artifacts. See [the development guide](docs/GUIDE.md) for the
architecture and [the browser-test guide](tests/ui/README.md) for adding scenarios and
reviewing screenshots.

---

## Stack

Go · [ogen](https://github.com/ogen-go/ogen) · [goldmark](https://github.com/yuin/goldmark) · [bluemonday](https://github.com/microcosm-cc/bluemonday) · [bun](https://github.com/uptrace/bun) · [goose](https://github.com/pressly/goose) · SQLite / PostgreSQL
