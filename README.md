# padmark

Padmark is a self-hosted Markdown pastebin with a web UI, JSON API, and CLI.

It is built for one simple job: publish a note, get a short link, and keep editing or deleting that note with a private edit code instead of a user account.

## What Padmark Is For

Padmark is useful when you want to:

- share Markdown notes, snippets, logs, or short instructions by URL;
- run a private pastebin inside your own infrastructure;
- create notes that disappear after the first read;
- script note creation and retrieval from the terminal or another service.

Padmark is not a team wiki, not a collaborative editor, and not a document management system. It is intentionally lightweight.

## Core Model

Every note has:

- a public URL slug such as `abc123def4` or a custom slug you choose;
- a title and body;
- a content type: `text/markdown` or `text/plain`;
- an `edit_code` returned once at creation time;
- optional burn-after-reading behavior;
- a view counter.

The `edit_code` is the only secret required to update or delete a note. There are no per-note user accounts.

## Burn-After-Reading

Padmark supports two burn modes:

- `burn_after_reading: true` with no TTL: the note is deleted on the first successful read;
- `burn_after_reading: true` with `ttl: N`: the first read starts a countdown, and the note expires `N` seconds later.

This is different from a normal fixed expiry. The timer starts on first read, not on creation.

## Interfaces

Padmark exposes the same note service in three ways:

- Web UI: create, read, and edit notes in the browser
- REST API: JSON CRUD with OpenAPI 3.1
- CLI: `padmark-cli` for terminal workflows

The API spec is the source of truth for the generated server and Go client.

## Features

- Markdown and plain-text notes
- Short URLs with generated or custom slugs
- One-time `edit_code` for update and delete operations
- Burn-after-reading with optional post-read TTL
- HTML, JSON, Markdown, and plain-text responses depending on `Accept`
- Sanitized Markdown rendering for browser views
- SQLite or PostgreSQL storage
- Optional token-based authentication
- Per-IP rate limiting
- TLS support with optional HTTP to HTTPS redirect
- Embedded API docs at `/api`

## Quick Start

### Run with SQLite

```bash
go run ./cmd/padmark-server serve --addr :4000
```

Or, since `serve` is the default action:

```bash
go run ./cmd/padmark-server --addr :4000
```

Then open `http://localhost:4000`.

### Run with PostgreSQL

```bash
go run ./cmd/padmark-server serve \
  --storage postgres \
  --dsn "postgres://user:pass@localhost:5432/padmark?sslmode=disable" \
  --addr :4000
```

### Run migrations only

```bash
go run ./cmd/padmark-server migrate --storage sqlite --dsn padmark.db
```

## Using the Web UI

Open `/` and publish a note:

1. Enter the note content
2. Optionally set a title
3. Optionally choose a custom slug
4. Optionally enable burn-after-reading
5. Click `Publish`

After creation, Padmark shows:

- the note URL;
- the `edit_code`.

Store the `edit_code`. It is required for editing and deleting, and it cannot be recovered later.

## Using the CLI

Install:

```bash
go install github.com/partyzanex/padmark/cmd/padmark-cli@latest
```

Defaults:

- server URL: `http://localhost:8080`
- env vars: `PADMARK_URL`, `PADMARK_TOKEN`, `PADMARK_EDIT_CODE`

Examples:

```bash
# Create from a file
padmark-cli create --file note.md

# Create from stdin
echo "# Hello" | padmark-cli create

# Create with a custom slug
padmark-cli create --title "Runbook" --content "..." --slug deploy-notes

# Create a burn-after-reading note that lives for 1 hour after first read
padmark-cli create --title "Secret" --content "eyes only" --burn --ttl 3600

# Create with a custom edit code (instead of auto-generated)
padmark-cli create --title "Note" --content "hello" --edit-code MySecretCode1

# Fetch a note
padmark-cli get abc123def4

# Print only raw content
padmark-cli get --raw abc123def4

# Print JSON
padmark-cli get --json abc123def4

# Update a note
padmark-cli edit abc123def4 --edit-code JmNkn0LdjbMw --file updated.md

# Delete a note
padmark-cli delete abc123def4 --edit-code JmNkn0LdjbMw

# Health checks
padmark-cli ping
```

If `--title` is omitted, the CLI derives it from the first non-empty line of content.

## Using the API

### Create a note

```bash
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Hello",
    "content": "# Hello\n\nThis is **padmark**."
  }'
```

The response includes `id`, `edit_code`, and note metadata.

You can supply your own `edit_code` instead of having one generated:

```bash
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Hello",
    "content": "# Hello",
    "edit_code": "MySecretCode1"
  }'
```

### Read a note

```bash
# JSON
curl http://localhost:4000/notes/{id}

# Rendered HTML
curl -H "Accept: text/html" http://localhost:4000/notes/{id}

# Raw Markdown or text
curl -H "Accept: text/plain" http://localhost:4000/notes/{id}

# Browser-friendly short URL
curl http://localhost:4000/{id}
```

### Update a note

```bash
curl -X PUT http://localhost:4000/notes/{id} \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Updated",
    "content": "# New content",
    "edit_code": "JmNkn0LdjbMw"
  }'
```

### Delete a note

```bash
curl -X DELETE http://localhost:4000/notes/{id} \
  -H "X-Edit-Code: JmNkn0LdjbMw"
```

The edit code may also be sent as the `edit_code` query parameter.

### Burn-after-reading examples

```bash
# Delete immediately on first read
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Secret",
    "content": "eyes only",
    "burn_after_reading": true
  }'

# Start a 1 hour expiry window on first read
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Secret",
    "content": "eyes only",
    "burn_after_reading": true,
    "ttl": 3600
  }'
```

## Authentication

Authentication is optional. When no tokens are configured, Padmark is open.

When `--auth-tokens` or `PADMARK_AUTH_TOKENS` is set, Padmark behaves like a private service:

- browser users must authenticate at `/login`;
- API and CLI clients must send `Authorization: Bearer <token>`;
- static assets, `/login`, `/api`, `/api/openapi.yaml`, `/healthz`, and `/readyz` remain public.

Example:

```bash
PADMARK_AUTH_TOKENS="token-a,token-b" go run ./cmd/padmark-server serve
```

CLI example:

```bash
padmark-cli --token token-a create --content "private note"
```

API example:

```bash
curl -H "Authorization: Bearer token-a" http://localhost:4000/notes/{id}
```

## Configuration

### Server flags

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--addr` | `PADMARK_ADDR` | `:8080` | HTTP listen address |
| `--storage` | `PADMARK_STORAGE` | `sqlite` | Storage backend: `sqlite` or `postgres` |
| `--dsn` | `PADMARK_DSN` | `padmark.db` | SQLite file path or PostgreSQL connection string |
| `--log-level` | `PADMARK_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `--log-format` | `PADMARK_LOG_FORMAT` | `json` | Log format: `json` or `text` |
| `--auth-tokens` | `PADMARK_AUTH_TOKENS` | empty | Comma-separated Bearer tokens |
| `--cookie-max-age` | `PADMARK_COOKIE_MAX_AGE` | `7776000` | Auth cookie lifetime in seconds |
| `--read-timeout` | `PADMARK_READ_TIMEOUT` | `30` | HTTP read timeout in seconds |
| `--max-header-bytes` | `PADMARK_MAX_HEADER_BYTES` | `65536` | Maximum request header size |
| `--max-body-bytes` | `PADMARK_MAX_BODY_BYTES` | `262144` | Maximum request body size |
| `--rate-limit` | `PADMARK_RATE_LIMIT` | `10` | Requests per second per IP; `0` disables it |
| `--rate-burst` | `PADMARK_RATE_BURST` | `20` | Burst size per IP |
| `--tls-cert` | `PADMARK_TLS_CERT` | empty | TLS certificate path |
| `--tls-key` | `PADMARK_TLS_KEY` | empty | TLS private key path |
| `--http-redirect-addr` | `PADMARK_HTTP_REDIRECT_ADDR` | empty | HTTP listener that redirects to HTTPS |
| `--trusted-proxies` | `PADMARK_TRUSTED_PROXIES` | empty | Trusted proxy CIDRs or IPs for forwarded client IPs |

### CLI flags

Global CLI options:

- `--url`, `-u`: Padmark server URL
- `--token`: Bearer token

Command-specific options:

- `create`: `--title`, `--content`, `--file`, `--slug`, `--plain`, `--burn`, `--ttl`, `--edit-code`
- `get`: `--raw`, `--json`
- `edit`: `--edit-code`, `--title`, `--content`, `--file`, `--plain`, `--burn`, `--ttl`
- `delete`: `--edit-code`

## Content Negotiation

For `GET /notes/{id}` and the short URL view:

- `Accept: application/json` returns JSON metadata and content
- `Accept: text/html` returns rendered HTML
- `Accept: text/plain` returns raw content
- `Accept: text/markdown` is treated like plain/raw output

The short route `/{id}` is convenient for browser access.

## API Docs

- Interactive docs: `/api`
- Raw OpenAPI spec: `/api/openapi.yaml`

The main API definition lives in [`openapi.yaml`](openapi.yaml).

## Project Layout

```text
cmd/padmark-server/            Server entry point
cmd/padmark-cli/               CLI entry point
openapi.yaml                   OpenAPI 3.1 source of truth
pkg/client/                    Generated Go client
internal/domain/               Domain models and errors
internal/usecases/notes/       Business logic
internal/adapters/http/        HTTP handlers, templates, middleware
internal/infra/storage/        SQLite and PostgreSQL repositories
internal/infra/render/         Markdown rendering and sanitization
internal/infra/server/         Server bootstrap and CLI commands
internal/infra/cli/            CLI implementation
migrations/                    SQL migrations
docs/                          Development notes
```

## Build and Test

```bash
make build
make test
make lint
make gen
```

Coverage HTML:

```bash
make cover
```

## Docker

Build:

```bash
make docker-build
make docker-build DOCKER_TAG=v1.0.0
```

Run:

```bash
docker run -p 4000:8080 partyzanex/padmark
```

Run with PostgreSQL via Compose:

```bash
docker compose up
```

The included [`docker-compose.yml`](docker-compose.yml) starts:

- PostgreSQL
- a migration job
- the Padmark app

## Stack

- Go
- ogen
- goldmark
- bluemonday
- bun
- goose
