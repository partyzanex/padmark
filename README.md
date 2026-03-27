# padmark

A minimalist Markdown notes service with a REST API.

Write a note in Markdown — get it back as JSON, HTML, or plain text depending on the `Accept` header.

## Features

- CRUD for notes via REST API
- Markdown → HTML rendering (goldmark + bluemonday)
- Content negotiation: `application/json`, `text/html`, `text/plain`
- OpenAPI 3.1 specification
- SQLite storage (`modernc.org/sqlite`)

## Quick start

```bash
go run ./cmd/server
```

The server starts at `http://localhost:8080`.

## API

### Create a note

```bash
curl -X POST http://localhost:8080/notes \
  -H "Content-Type: application/json" \
  -d '{"title": "First note", "content": "# Hello\n\nThis is **padmark**."}'
```

### Get a note

```bash
# JSON (default)
curl http://localhost:8080/notes/{id}

# Rendered HTML
curl -H "Accept: text/html" http://localhost:8080/notes/{id}

# Raw Markdown
curl -H "Accept: text/plain" http://localhost:8080/notes/{id}
```

### Update

```bash
curl -X PUT http://localhost:8080/notes/{id} \
  -H "Content-Type: application/json" \
  -d '{"title": "Updated", "content": "# New content"}'
```

### Delete

```bash
curl -X DELETE http://localhost:8080/notes/{id}
```

## Project structure

```
cmd/server/main.go                        — entry point (10–15 lines)
internal/
  domain/                                 — entities and sentinel errors
  usecases/notes/manager.go               — business logic, Storage and Renderer interfaces
  infra/
    storage/sqlite.go                     — SQLite implementation (uptrace/bun)
    storage/memory.go                     — in-memory implementation (for tests)
    render/markdown.go                    — Markdown → HTML (goldmark + bluemonday)
    cmd/app.go                            — DI, CLI, graceful shutdown
    cmd/flags.go                          — configuration (Flag*, Env*, Default*)
  adapters/http/
    notes.go                              — CRUD handlers
    negotiate.go                          — content negotiation
    health.go                             — /healthz, /readyz
    errors.go                             — domain errors → HTTP status translation
migrations/                               — goose SQL migrations
templates/note.html                       — HTML wrapper for rendering
```

## Build and test

```bash
make build     # build binary
make test      # run tests
make lint      # golangci-lint
make run       # build and run
```

## Docker

```bash
docker build -t padmark .
docker run -p 8080:8080 padmark
```

## Stack

- Go 1.25+
- [goldmark](https://github.com/yuin/goldmark) — Markdown parsing
- [bluemonday](https://github.com/microcosm-cc/bluemonday) — HTML sanitization
- [uptrace/bun](https://github.com/uptrace/bun) + [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — SQLite
- [pressly/goose](https://github.com/pressly/goose) — migrations
- [urfave/cli](https://github.com/urfave/cli) — CLI and configuration
- [testify](https://github.com/stretchr/testify) + [gomock](https://github.com/uber-go/mock) — tests

## Roadmap

- [ ] Full-text search (`GET /notes?q=...`)
- [ ] Tags and categories
- [ ] Syntax highlighting in HTML (`goldmark-highlighting` + `chroma`)
- [ ] Note versioning (`GET /notes/{id}/history`)
- [ ] Cursor-based pagination instead of offset
- [ ] ETag / `If-None-Match` for response caching
- [ ] Mermaid diagram support
- [ ] Rate limiting and CORS
- [ ] Metrics (Prometheus)
- [ ] PostgreSQL storage

## License

MIT
