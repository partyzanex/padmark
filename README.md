# padmark

A self-hosted Markdown pastebin with a web UI, REST API, burn-after-reading, edit codes, and token-based auth.

## Features

- **Web UI** — create, view, and edit notes in the browser with live Markdown preview
- **REST API** — JSON CRUD with OpenAPI 3.1 spec and generated Go client (`pkg/client`)
- **Content negotiation** — `application/json`, `text/html`, `text/plain`, `text/markdown` via `Accept` header
- **Short URLs** — notes accessible at `/{id}` (10-char random slug or custom)
- **Burn after reading** — deleted immediately on first read; with TTL — survives for N seconds after first read
- **Edit codes** — secret 12-char token returned on creation, required for edit/delete
- **Rate limiting** — per-IP token bucket (configurable RPS and burst)
- **TLS support** — HTTPS with optional HTTP→HTTPS redirect listener
- **Token auth** — optional Bearer token + cookie-based login for the web UI
- **Storage backends** — SQLite (default) or PostgreSQL
- **Spec-first API** — [ogen](https://github.com/ogen-go/ogen) generated server and client from `openapi.yaml`
- **Three themes** — light, dim (default), dark — persisted in localStorage
- **API docs** — embedded Redoc UI at `/api`

## Quick start

```bash
# SQLite (default)
go run ./cmd/padmark-server --addr :4000

# PostgreSQL
go run ./cmd/padmark-server --storage postgres --dsn "postgres://user:pass@localhost:5432/padmark?sslmode=disable"
```

Open `http://localhost:4000` in the browser.

## Configuration

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--addr` | `PADMARK_ADDR` | `:8080` | HTTP listen address |
| `--storage` | `PADMARK_STORAGE` | `sqlite` | Storage backend: `sqlite`, `postgres` |
| `--dsn` | `PADMARK_DSN` | `padmark.db` | Database DSN (file path for SQLite, connection string for PostgreSQL) |
| `--log-level` | `PADMARK_LOG_LEVEL` | `info` | Log level: debug, info, warn, error |
| `--log-format` | `PADMARK_LOG_FORMAT` | `json` | Log format: json, text |
| `--auth-tokens` | `PADMARK_AUTH_TOKENS` | *(empty)* | Comma-separated Bearer tokens for write endpoints (empty = no auth) |
| `--cookie-max-age` | `PADMARK_COOKIE_MAX_AGE` | `7776000` | Auth cookie max-age in seconds (default: 3 months) |
| `--read-timeout` | `PADMARK_READ_TIMEOUT` | `30` | HTTP read timeout in seconds |
| `--max-header-bytes` | `PADMARK_MAX_HEADER_BYTES` | `65536` | Maximum request header size in bytes |
| `--max-body-bytes` | `PADMARK_MAX_BODY_BYTES` | `262144` | Maximum request body size in bytes |
| `--rate-limit` | `PADMARK_RATE_LIMIT` | `10` | Requests per second per IP (0 = disabled) |
| `--rate-burst` | `PADMARK_RATE_BURST` | `20` | Max burst size per IP |
| `--tls-cert` | `PADMARK_TLS_CERT` | *(empty)* | Path to TLS certificate PEM file (enables HTTPS) |
| `--tls-key` | `PADMARK_TLS_KEY` | *(empty)* | Path to TLS private key PEM file (enables HTTPS) |
| `--http-redirect-addr` | `PADMARK_HTTP_REDIRECT_ADDR` | *(empty)* | HTTP→HTTPS redirect listener address (e.g. `:80`); TLS only |
| `--trusted-proxies` | `PADMARK_TRUSTED_PROXIES` | *(empty)* | Comma-separated trusted proxy CIDRs/IPs for `X-Forwarded-For` |

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

### Burn after reading

```bash
# Delete immediately on first read (no TTL)
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{"title":"Secret","content":"eyes only","burn_after_reading":true}'

# Survive for 1 hour after first read, then expire
curl -X POST http://localhost:4000/notes \
  -H "Content-Type: application/json" \
  -d '{"title":"Secret","content":"eyes only","burn_after_reading":true,"ttl":3600}'
```

`ttl` is the grace period in seconds **after the first read** — the note is not deleted immediately but becomes inaccessible once the TTL elapses. Without `ttl` (or `ttl=0`) the note is deleted on the first read.

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
cmd/padmark-server/main.go                          Entry point
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
go test -tags=integration ./tests/
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
