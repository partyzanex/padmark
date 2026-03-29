# padmark

A self-hosted Markdown pastebin with a web UI, REST API, burn-after-reading, edit codes, and token-based auth.

## Features

- **Web UI** — create, view, and edit notes in the browser with live Markdown preview
- **REST API** — JSON CRUD with OpenAPI 3.1 spec and generated Go client (`pkg/client`)
- **Content negotiation** — `application/json`, `text/html`, `text/plain`, `text/markdown` via `Accept` header
- **Short URLs** — notes accessible at `/{id}` (8-char random slug or custom)
- **Burn after reading** — auto-delete on first read (without TTL) or auto-expire after TTL
- **Edit codes** — secret 12-char token returned on creation, required for edit/delete
- **Token auth** — optional Bearer token + cookie-based login for the web UI
- **Storage backends** — SQLite (default) or PostgreSQL
- **Spec-first API** — [ogen](https://github.com/ogen-go/ogen) generated server and client from `openapi.yaml`
- **Three themes** — light, dim (default), dark — persisted in cookie
- **API docs** — embedded Redoc UI at `/api`

## Quick start

```bash
# SQLite (default)
go run ./cmd/server --addr :4000

# PostgreSQL
go run ./cmd/server --storage postgres --dsn "postgres://user:pass@localhost:5432/padmark?sslmode=disable"
```

Open `http://localhost:4000` in the browser.

## Configuration

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--addr` | `PADMARK_ADDR` | `:8080` | HTTP listen address |
| `--storage` | `PADMARK_STORAGE` | `sqlite` | Storage backend: `sqlite`, `postgres` |
| `--dsn` | `PADMARK_DSN` | `padmark.db` | Database DSN |
| `--log-level` | `PADMARK_LOG_LEVEL` | `info` | Log level: debug, info, warn, error |
| `--log-format` | `PADMARK_LOG_FORMAT` | `json` | Log format: json, text |
| `--auth-tokens` | `PADMARK_AUTH_TOKENS` | *(empty)* | Comma-separated Bearer tokens (empty = no auth) |

## API

### Create a note

```bash
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{"title":"Hello","content":"# Hello\n\nThis is **padmark**."}'
```

Response includes `edit_code` — save it to edit or delete the note later.

### Get a note

```bash
# JSON
curl http://localhost:4000/notes/{id}

# Rendered HTML
curl -H "Accept: text/html" http://localhost:4000/notes/{id}

# Raw Markdown
curl http://localhost:4000/{id}?raw=1
```

### Update (requires edit code)

```bash
curl -X PUT http://localhost:4000/notes/{id} \
  -H "Content-Type: application/json" \
  -d '{"title":"Updated","content":"# New","edit_code":"JmNkn0LdjbMw"}'
```

### Delete (requires edit code)

```bash
curl -X DELETE http://localhost:4000/notes/{id} \
  -H "X-Edit-Code: JmNkn0LdjbMw"
```

### Burn after reading + TTL

```bash
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{"title":"Secret","content":"eyes only","burn_after_reading":true,"ttl":3600}'
```

### Auth (when tokens are configured)

```bash
# API
curl -H "Authorization: Bearer my-token" http://localhost:4000/notes

# Browser — visit /login and enter the token
```

### API docs

Interactive docs at `/api`, raw spec at `/api/openapi.yaml`.

## Project structure

```
cmd/server/main.go                          Entry point
openapi.yaml                                OpenAPI 3.1 spec (source of truth)
pkg/client/                                 Generated Go API client (ogen)
internal/
  domain/                                   Entities, sentinel errors
  usecases/notes/                           Business logic (Manager)
  infra/
    storage/sqlite/                         SQLite repository + migrations
    storage/postgres/                       PostgreSQL repository + migrations
    render/                                 Markdown → HTML (goldmark + bluemonday)
    cmd/                                    DI, CLI flags, graceful shutdown
  adapters/http/
    handler.go                              Handler struct, interfaces, templates
    router.go                               Routing, middleware (auth, logging, recovery)
    ogen_handler.go                         ogen Handler implementation
    ogen_convert.go                         Domain ↔ ogen type mapping
    ogenapi/                                Generated ogen server code
    web_*.go                                HTML page handlers
    middleware_auth.go                      Token auth (Bearer + cookie)
    templates/                              HTML templates (shared header)
    static/                                 CSS, favicon
    spec/                                   Embedded OpenAPI spec
migrations/
  sqlite/                                   Goose SQL migrations
  postgres/                                 Goose SQL migrations
```

## Build and test

```bash
make build                    # Build binary
make test                     # Unit tests (-race)
make lint                     # golangci-lint
make gen                      # go generate (mocks + ogen)

# Integration tests (requires Docker)
go test -tags=integration -v ./internal/infra/storage/postgres/
```

## Docker

```bash
docker build -t padmark .
docker run -p 4000:8080 padmark
```

## Stack

- Go 1.25+
- [ogen](https://github.com/ogen-go/ogen) — spec-first API server and client generation
- [goldmark](https://github.com/yuin/goldmark) — Markdown parsing
- [bluemonday](https://github.com/microcosm-cc/bluemonday) — HTML sanitization
- [uptrace/bun](https://github.com/uptrace/bun) — database toolkit (SQLite + PostgreSQL)
- [pressly/goose](https://github.com/pressly/goose) — migrations
- [urfave/cli](https://github.com/urfave/cli) — CLI and configuration
- [testcontainers-go](https://github.com/testcontainers/testcontainers-go) — Postgres integration tests
- [testify](https://github.com/stretchr/testify) + [gomock](https://github.com/uber-go/mock) — testing

## License

MIT
