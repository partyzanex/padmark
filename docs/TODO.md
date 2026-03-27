# padmark — TODO

## 0. Project initialization
- [ ] `go mod init github.com/partyzanex/padmark`
- [ ] Create directory structure (`cmd/`, `internal/`, `templates/`, `migrations/`)
- [ ] Add `README.md`, `LICENSE`
- [ ] Configure linter (`golangci-lint`)

## 1. Domain layer
- [ ] `internal/domain/errors.go` — sentinel errors (`ErrNotFound`, `ErrTitleRequired`, `ErrContentTooLong`)
- [ ] `internal/domain/note.go` — `Note` struct (`ID`, `Title`, `Content`, `CreatedAt`, `UpdatedAt`)

## 2. Usecases layer
- [ ] `internal/usecases/notes/manager.go`
    - [ ] `Storage` interface (next to consumer, not in infra)
        - [ ] `Create(ctx, *Note) error`
        - [ ] `List(ctx, limit, offset int) ([]Note, int, error)`
        - [ ] `Get(ctx, id int64) (*Note, error)`
        - [ ] `Update(ctx, id int64, *Note) error`
        - [ ] `Delete(ctx, id int64) error`
    - [ ] `Renderer` interface — `Render(content string) (string, error)`
    - [ ] `//go:generate mockgen` directive
    - [ ] `Manager` struct with constructor `NewManager(storage Storage, renderer Renderer, log *slog.Logger) *Manager`
    - [ ] CRUD methods on `Manager`
    - [ ] Input validation (title not empty, content ≤ limit) → domain sentinel errors
- [ ] `internal/usecases/notes/manager_test.go` — unit tests (testify/suite + gomock)
- [ ] `internal/usecases/notes/manager_mocks_test.go` — generated mocks

## 3. Infra layer

### Storage
- [ ] `internal/infra/storage/sqlite.go` — SQLite implementation via `modernc.org/sqlite` + `uptrace/bun`
    - [ ] Returns `*Repository`, implements `notes.Storage`
    - [ ] Translates `sql.ErrNoRows` → `domain.ErrNotFound`
- [ ] `migrations/` — goose SQL migrations (`pressly/goose/v3`, embedded via `//go:embed *.sql`)
- [ ] `internal/infra/storage/memory.go` — in-memory implementation (`sync.RWMutex` + `map`) for tests only
- [ ] Tests for both storage implementations

### Markdown renderer
- [ ] `internal/infra/render/markdown.go` — goldmark wrapper
    - [ ] Extensions: tables, strikethrough, autolinks
    - [ ] Sanitize output with `bluemonday`
    - [ ] Returns `*Renderer`, implements `notes.Renderer`
- [ ] Tests for renderer

### CLI / config
- [ ] `internal/infra/cmd/flags.go` — three constant groups: `Flag*`, `Env*` (`PADMARK_*`), `Default*`
- [ ] `internal/infra/cmd/app.go` — `NewApp()`, `Run()`, `action()`
    - [ ] DI order: logger → storage → renderer → manager → handler → router → server
    - [ ] Graceful shutdown via `signal.NotifyContext` + `srv.Shutdown`

## 4. Adapters layer
- [ ] `internal/adapters/http/handler.go` — `Handler` struct, `NewHandler(*notes.Manager, *slog.Logger) *Handler`
- [ ] `internal/adapters/http/notes.go` — CRUD handlers
    - [ ] `CreateNote` — parse JSON, 201 + `Location`
    - [ ] `ListNotes` — pagination, 200
    - [ ] `GetNote` — content negotiation by `Accept`
    - [ ] `UpdateNote` — parse JSON, 200
    - [ ] `DeleteNote` — 204
- [ ] `internal/adapters/http/negotiate.go` — parse `Accept`, select format
    - [ ] `application/json` → JSON object
    - [ ] `text/html` → render markdown to HTML page
    - [ ] `text/plain`, `text/markdown` → raw markdown
- [ ] `internal/adapters/http/errors.go` — `writeError()`: translate domain errors to HTTP status codes
- [ ] `internal/adapters/http/health.go` — `GET /healthz` (liveness), `GET /readyz` (readiness, checks DB)
- [ ] `internal/adapters/http/router.go` — `NewRouter(*Handler) http.Handler`, middleware: logging, recovery, request ID
- [ ] Handler tests (`httptest`)

## 5. Entry point
- [ ] `cmd/server/main.go` — 10–15 lines, delegates to `internal/infra/cmd`
- [ ] `templates/note.html` — HTML wrapper (charset, base CSS)
- [ ] Smoke test: start → curl → stop

## 6. Documentation and CI
- [ ] `README.md` — description, quick start, curl examples
- [ ] Dockerfile (multi-stage → scratch)
- [ ] `docker-compose.yml` — local dev environment
- [ ] GitHub Actions: lint + test on push

## 7. Improvements (v2)
- [ ] Note search (`GET /notes?q=...`)
- [ ] Tags / categories
- [ ] Middleware: rate limiting, CORS
- [ ] Metrics (Prometheus) via interface in usecases, implementation in `internal/infra/metrics/`
- [ ] PostgreSQL storage implementation