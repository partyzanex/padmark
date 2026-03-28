# padmark — TODO

## 0. Project initialization
- [x] `go mod init github.com/partyzanex/padmark`
- [x] Create directory structure (`cmd/`, `internal/`, `templates/`, `migrations/`)
- [x] Add `README.md`, `LICENSE`
- [x] Configure linter (`golangci-lint`)

## 1. Domain layer
- [x] `internal/domain/errors.go` — sentinel errors (`ErrNotFound`, `ErrTitleRequired`, `ErrContentTooLong`)
- [x] `internal/domain/note.go` — `Note` struct (`ID`, `Title`, `Content`, `CreatedAt`, `UpdatedAt`)

## 2. Usecases layer
- [x] `internal/usecases/notes/manager.go`
    - [x] `Storage` interface (next to consumer, not in infra)
        - [x] `Create(ctx, *Note) error`
        - [x] `Get(ctx, id int64) (*Note, error)`
        - [x] `Update(ctx, id int64, *Note) error`
        - [x] `Delete(ctx, id int64) error`
    - [x] `Renderer` interface — `Render(content string) (string, error)`
    - [x] `//go:generate mockgen` directive
    - [x] `Manager` struct with constructor `NewManager(storage Storage, renderer Renderer, log *slog.Logger) *Manager`
    - [x] CRUD methods on `Manager`
    - [x] Input validation (title not empty, content ≤ limit) → domain sentinel errors
- [x] `internal/usecases/notes/manager_test.go` — unit tests (testify/suite + gomock)
- [x] `internal/usecases/notes/manager_mocks_test.go` — generated mocks

## 3. Infra layer

### Storage
- [x] `internal/infra/storage/sqlite/repository.go` — SQLite implementation via `modernc.org/sqlite` + `uptrace/bun`
    - [x] Returns `*Repository`, implements `notes.Storage`
    - [x] Translates `sql.ErrNoRows` → `domain.ErrNotFound`
- [x] `migrations/` — goose SQL migrations (`pressly/goose/v3`, embedded via `//go:embed *.sql`)
- [x] Tests for SQLite storage (12 integration tests)

### Markdown renderer
- [x] `internal/infra/render/markdown.go` — goldmark wrapper
    - [x] Extensions: tables, strikethrough, autolinks
    - [x] Sanitize output with `bluemonday`
    - [x] Returns `*Renderer`, implements `notes.Renderer`
- [x] Tests for renderer

### CLI / config
- [x] `internal/infra/cmd/flags.go` — three constant groups: `Flag*`, `Env*` (`PADMARK_*`), `Default*`
- [x] `internal/infra/cmd/app.go` — `NewApp()`, `Run()`, `action()`
    - [x] DI order: logger → storage → renderer → manager → handler → router → server
    - [x] Graceful shutdown via `signal.NotifyContext` + `srv.Shutdown`

## 4. Adapters layer
- [x] `internal/adapters/http/handler.go` — `Handler` struct, `NewHandler(*notes.Manager, *slog.Logger) *Handler`
- [x] `internal/adapters/http/notes.go` — CRUD handlers
    - [x] `CreateNote` — parse JSON, 201 + `Location`
    - [x] `GetNote` — content negotiation by `Accept`
    - [x] `UpdateNote` — parse JSON, 200
    - [x] `DeleteNote` — 204
- [x] `internal/adapters/http/negotiate.go` — parse `Accept`, select format
    - [x] `application/json` → JSON object
    - [x] `text/html` → render markdown to HTML page
    - [x] `text/plain`, `text/markdown` → raw markdown
- [x] `internal/adapters/http/errors.go` — `writeError()`: translate domain errors to HTTP status codes
- [x] `internal/adapters/http/health.go` — `GET /healthz` (liveness), `GET /readyz` (readiness, checks DB)
- [x] `internal/adapters/http/router.go` — `NewRouter(*Handler) http.Handler`, middleware: logging, recovery, request ID
- [x] Handler tests (`httptest`)

## 5. Entry point
- [x] `cmd/server/main.go` — 10–15 lines, delegates to `internal/infra/cmd`
- [x] `templates/note.html` — HTML wrapper (charset, base CSS)
- [x] Smoke test: start → curl → stop

## 6. Documentation and CI
- [x] `README.md` — description, quick start, curl examples
- [x] Dockerfile (multi-stage → scratch)

## 7. Frontend / UI (design parity)

### Backend features required by the design
- [x] Note TTL / expiry — `ExpiresAt *time.Time` in domain, `ttl` param in `POST /notes`, auto-expire check on `GET`
- [x] Burn-after-reading — `BurnAfterReading bool` in domain, delete note on first `GET`
- [x] View count — `Views int` in domain, increment on `GET /notes/{id}`
- [x] Custom slug — allow client to supply a custom `id`/slug in `POST /notes`; validate uniqueness

### Pages
- [ ] View page (`GET /notes/{id}` HTML) — replace `templates/note.html` with template matching `docs/design/view.html`
    - [ ] Meta bar: created date, view count, expiry label
    - [ ] Action buttons: Copy (raw content), Raw (link to `text/plain`), Link (copy URL)
    - [ ] Compact theme switcher (icon buttons, no pill)
    - [ ] Footer links: Raw · Edit · New
- [ ] Success / confirmation page — template matching `docs/design/success.html` (shown after `POST /notes`)
    - [ ] Display generated URL with one-click copy
    - [ ] "Burn after reading" warning banner when enabled
    - [ ] TTL / expiry info
- [ ] Error page — template matching `docs/design/error.html`
    - [ ] 404 Not Found variant
    - [ ] Generic error variant

### Shared UI
- [ ] Add `--success-glow`, `--warn`, `--warn-bg`, `--warn-border`, `--err-red`, `--err-amber` CSS variables to `static/style.css`
- [ ] Add success-page and error-page component styles to `static/style.css`
- [ ] Restore Slug field to `GET /` editor (alongside Title) — maps to custom slug feature above
- [ ] Theme preference persisted in `localStorage` on view page and success page (already done on index)

## 9. Improvements (v2)
- [ ] Note search (`GET /notes?q=...`)
- [ ] Tags / categories
- [ ] Syntax highlighting in HTML (`goldmark-highlighting` + `chroma`)
- [ ] Note versioning — change history (`GET /notes/{id}/history`)
- [ ] Cursor-based pagination instead of offset
- [ ] ETag / `If-None-Match` for response caching
- [ ] Mermaid diagram support in renderer
- [ ] Middleware: rate limiting, CORS
- [ ] Metrics (Prometheus) via interface in usecases, implementation in `internal/infra/metrics/`
- [ ] PostgreSQL storage implementation