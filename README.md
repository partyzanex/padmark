# padmark

A self-hosted notepad for sharing Markdown snippets by URL — no accounts, no friction.

Publish a note, get a short link, and manage it later with a one-time edit code. That's it.

---

## What it does

- Share Markdown (or plain text) notes via a short URL
- Edit or delete notes using a secret `edit_code` — no login required
- Make notes disappear after the first read (burn-after-reading)
- Keep notes private so only authenticated users can see them
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

> **Keep your `edit_code` safe.** It's shown once at creation and cannot be recovered.

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

# Custom slug and title
padmark-cli create --title "Runbook" --content "..." --slug deploy-notes

# Burn-after-reading with a 1-hour window after first read
padmark-cli create --title "Secret" --content "eyes only" --burn --ttl 3600

# Fetch a note (JSON, raw, or default)
padmark-cli get abc123def4
padmark-cli get --raw abc123def4
padmark-cli get --json abc123def4

# Update a note
padmark-cli edit abc123def4 --edit-code JmNkn0LdjbMw --file updated.md

# Delete a note
padmark-cli delete abc123def4 --edit-code JmNkn0LdjbMw

# Check server health
padmark-cli ping
```

Set defaults via environment variables: `PADMARK_URL`, `PADMARK_TOKEN`, `PADMARK_EDIT_CODE`.

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

> **Note:** `private: true` is available in the API and OpenAPI spec but is not exposed as a CLI flag.

---

## Authentication

Authentication is **off by default**. Enable it by setting `--auth-tokens` or `PADMARK_AUTH_TOKENS`:

```bash
PADMARK_AUTH_TOKENS="token-a,token-b" go run ./cmd/padmark-server serve
```

Once enabled:

- **Public notes** are still readable without a token
- **Private notes** require a valid session (browsers are redirected to `/login`, API clients get `401`)
- **Write operations** (create, update, delete) require `Authorization: Bearer <token>`

Always public regardless of token config: `/login`, `/static/*`, `/api`, `/api/openapi.yaml`, `/healthz`, `/readyz`.

---

## Configuration reference

### Server

| Flag | Env var | Default | Description |
|---|---|---|---|
| `--addr` | `PADMARK_ADDR` | `:8080` | Listen address |
| `--storage` | `PADMARK_STORAGE` | `sqlite` | `sqlite` or `postgres` |
| `--dsn` | `PADMARK_DSN` | `padmark.db` | DB path or connection string |
| `--auth-tokens` | `PADMARK_AUTH_TOKENS` | — | Comma-separated Bearer tokens |
| `--rate-limit` | `PADMARK_RATE_LIMIT` | `10` | Requests/sec per IP (`0` = disabled) |
| `--rate-burst` | `PADMARK_RATE_BURST` | `20` | Burst size per IP |
| `--tls-cert` | `PADMARK_TLS_CERT` | — | TLS certificate path |
| `--tls-key` | `PADMARK_TLS_KEY` | — | TLS private key path |
| `--http-redirect-addr` | `PADMARK_HTTP_REDIRECT_ADDR` | — | HTTP → HTTPS redirect listener |
| `--log-level` | `PADMARK_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `--log-format` | `PADMARK_LOG_FORMAT` | `json` | `json` or `text` |
| `--trusted-proxies` | `PADMARK_TRUSTED_PROXIES` | — | Proxy CIDRs/IPs for real client IPs |

### CLI global flags

| Flag | Description |
|---|---|
| `--url`, `-u` | Padmark server URL |
| `--token` | Bearer token |

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