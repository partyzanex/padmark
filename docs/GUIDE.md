# Go Service Development Guide

> Architectural patterns applicable to padmark and similar Go services.

---

## Contents

1. [Clean Architecture](#1-clean-architecture)
2. [Project Structure](#2-project-structure)
3. [Domain Layer](#3-domain-layer)
4. [Usecases Layer](#4-usecases-layer)
5. [Infra Layer](#5-infra-layer)
6. [Adapters Layer](#6-adapters-layer)
7. [CLI and Configuration](#7-cli-and-configuration)
8. [Dependency Injection](#8-dependency-injection)
9. [Error Handling](#9-error-handling)
10. [Metrics](#10-metrics)
11. [Graceful Shutdown](#11-graceful-shutdown)
12. [Testing](#12-testing)
13. [Security](#13-security)
14. [Modern Go](#14-modern-go)
15. [Anti-patterns](#15-anti-patterns)
16. [Deploy](#16-deploy)
17. [Cheatsheet](#17-cheatsheet)

---

## 1. Clean Architecture

### Dependency Rule

```
domain  <--  usecases  <--  infra
                        <--> adapters
```

| Layer | Responsibility | Dependencies |
|-------|----------------|--------------|
| **Domain** | Entities, domain errors | stdlib only |
| **Usecases** | Business logic, dependency interfaces | Domain only |
| **Infra** | Implementations: DB, cache, HTTP clients, CLI | Implements usecases interfaces |
| **Adapters** | Protocol translation: HTTP handlers | Converts between domain and protocol |

### Key Principles

| Principle | Implementation |
|-----------|----------------|
| Dependencies point inward only | infra/adapters → usecases → domain |
| Interfaces next to consumer | `Repository` in usecases, not in domain or infra |
| Zero dependencies in domain | stdlib only, no interfaces |
| SQL-first | uptrace/bun + goose migrations |
| Mock generation | `//go:generate` with `go.uber.org/mock/mockgen` |
| Vendor | `go mod vendor`, committed to the repo |

---

## 2. Project Structure

```
padmark/
├── cmd/padmark-server/
│   └── main.go                  # 10-15 lines, delegates to infra/cmd
│
├── internal/
│   ├── domain/                  # Entities and errors (no interfaces)
│   │   ├── errors.go
│   │   └── note.go
│   │
│   ├── usecases/notes/          # Business logic + dependency interfaces
│   │   ├── manager.go           # //go:generate mockgen ...
│   │   ├── manager_test.go
│   │   └── manager_mocks_test.go
│   │
│   ├── infra/                   # Implementations
│   │   ├── cmd/                 # DI, CLI, startup
│   │   │   ├── app.go           # NewApp(), Run(), action()
│   │   │   └── flags.go         # Flag/Env/Default constants
│   │   ├── storage/             # DB implementations (memory, sqlite, postgres)
│   │   └── render/              # Markdown renderer (goldmark + bluemonday)
│   │
│   └── adapters/                # Protocol translation
│       └── http/                # HTTP handlers, content negotiation
│
├── tests/                       # Integration tests
│   └── {feature}_test.go        # //go:build integration
│
├── migrations/                  # goose SQL + embed
│   ├── migrations.go
│   └── YYYYMMDD_name.sql
│
├── templates/                   # HTML templates
├── docs/
├── Makefile                     # include go.mk
├── go.mk                        # partyzanex/go-makefile
├── Dockerfile                   # multi-stage -> scratch
└── .golangci.yml                # v2
```

---

## 3. Domain Layer

Entities and sentinel errors only. No interfaces, no external dependencies.

```go
// internal/domain/errors.go
package domain

import "errors"

var (
    ErrNotFound      = errors.New("not found")
    ErrTitleRequired = errors.New("title is required")
    ErrContentTooLong = errors.New("content exceeds maximum length")
)
```

```go
// internal/domain/note.go
package domain

import "time"

type Note struct {
    ID        int64
    Title     string
    Content   string
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

**Rules:**
- No `interface` in domain — they belong in usecases
- Stdlib only
- Sentinel errors via `errors.New`
- Business rules as methods on entities (if needed)

---

## 4. Usecases Layer

Business logic + dependency interfaces. Interfaces defined **next to the consumer** (DIP).

```go
//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=notes

package notes

// Interfaces — defined here, not in domain or infra
type Storage interface {
    Create(ctx context.Context, note *domain.Note) error
    List(ctx context.Context, limit, offset int) ([]domain.Note, int, error)
    Get(ctx context.Context, id int64) (*domain.Note, error)
    Update(ctx context.Context, id int64, note *domain.Note) error
    Delete(ctx context.Context, id int64) error
}

type Renderer interface {
    Render(content string) (string, error)
}

// Manager — constructor accepts interfaces
type Manager struct {
    storage  Storage
    renderer Renderer
    log      *slog.Logger
}

func NewManager(storage Storage, renderer Renderer, log *slog.Logger) *Manager {
    return &Manager{storage: storage, renderer: renderer, log: log}
}
```

**Rules:**
- Struct is named `Manager` (not `UseCase`, not `Service`)
- Constructor takes all dependencies explicitly — no `WithOption` for required deps
- `//go:generate mockgen` in the file with interfaces
- Logger: `*slog.Logger`
- No nil-checks — if a dependency is required, it's passed via constructor

---

## 5. Infra Layer

### Storage (in-memory)

```go
// internal/infra/storage/memory.go
package storage

type Repository struct {
    mu    sync.RWMutex
    notes map[int64]*domain.Note
    seq   int64
}

func NewRepository() *Repository {
    return &Repository{notes: make(map[int64]*domain.Note)}
}

func (r *Repository) Get(ctx context.Context, id int64) (*domain.Note, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    note, ok := r.notes[id]
    if !ok {
        return nil, domain.ErrNotFound
    }
    return note, nil
}
```

### Migrations (goose)

```go
// migrations/migrations.go
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

**Rules:**
- Constructors return concrete type: `NewRepository() *Repository`
- Structs named by role: `Repository`, `Cache` (not `MemoryRepository`)
- DB errors translated to domain errors: `sql.ErrNoRows` → `domain.ErrNotFound`

---

## 6. Adapters Layer

### HTTP Handler

```go
// internal/adapters/http/handler.go
type Handler struct {
    manager *notes.Manager
    log     *slog.Logger
}

func (h *Handler) GetNote(w http.ResponseWriter, r *http.Request) {
    note, err := h.manager.Get(r.Context(), id)
    if err != nil {
        h.writeError(w, err)
        return
    }
    h.negotiate(w, r, note)
}
```

### Error translation (HTTP)

```go
func (h *Handler) writeError(w http.ResponseWriter, err error) {
    switch {
    case errors.Is(err, domain.ErrNotFound):
        http.Error(w, "not found", http.StatusNotFound)
    case errors.Is(err, domain.ErrTitleRequired):
        http.Error(w, err.Error(), http.StatusBadRequest)
    default:
        http.Error(w, "internal error", http.StatusInternalServerError)
    }
}
```

### Content negotiation

```go
// internal/adapters/http/negotiate.go
func (h *Handler) negotiate(w http.ResponseWriter, r *http.Request, note *domain.Note) {
    accept := r.Header.Get("Accept")
    switch {
    case strings.Contains(accept, "text/html"):
        // render markdown to HTML page
    case strings.Contains(accept, "text/plain"), strings.Contains(accept, "text/markdown"):
        // return raw markdown
    default:
        // return JSON
    }
}
```

**Rules:**
- Each adapter defines **its own** interfaces — does not reuse from usecases
- Adapter defines: `NoteRenderer interface { Render(string) (string, error) }`

---

## 7. CLI and Configuration

### flags.go

Three constant groups: flag names, env vars, defaults.

```go
// internal/infra/cmd/flags.go
package cmd

const (
    // Flags
    FlagAddr     = "addr"
    FlagLogLevel = "log-level"

    // Environment variables
    EnvAddr     = "PADMARK_ADDR"
    EnvLogLevel = "PADMARK_LOG_LEVEL"

    // Defaults
    DefaultAddr     = ":8080"
    DefaultLogLevel = "info"
)
```

### main.go

```go
// cmd/padmark-server/main.go
package main

import "github.com/partyzanex/padmark/internal/infra/cmd"

func main() {
    app := cmd.NewApp()
    if err := cmd.Run(app); err != nil {
        slog.Error("failed to run", "error", err)
        os.Exit(1)
    }
}
```

10-15 lines. All logic in `internal/infra/cmd/`.

---

## 8. Dependency Injection

All dependency wiring in `action()` in `app.go`. Manual DI, no frameworks.

```go
// internal/infra/cmd/app.go
func action(ctx context.Context, cmd *cli.Command) error {
    log := newLogger(cmd.String(FlagLogLevel))

    // 1. Infrastructure
    storage := memory.NewRepository()

    // 2. Business logic
    renderer := render.NewMarkdownRenderer()
    manager := notes.NewManager(storage, renderer, log)

    // 3. Adapters
    handler := httphandler.NewHandler(manager, log)

    // 4. HTTP server + graceful shutdown
    srv := &http.Server{
        Addr:    cmd.String(FlagAddr),
        Handler: newRouter(handler),
    }

    go func() {
        <-ctx.Done()
        _ = srv.Shutdown(context.Background())
    }()

    return srv.ListenAndServe()
}
```

**Order:**
1. Logger
2. Infrastructure (storage, renderer)
3. Business logic
4. Adapters (HTTP handler, router)
5. Server start + graceful shutdown

---

## 9. Error Handling

### Sentinel errors in domain

```go
var (
    ErrNotFound      = errors.New("not found")
    ErrTitleRequired = errors.New("title is required")
)
```

### Wrapping with context

```go
// Always wrap with operation description
return fmt.Errorf("create note: %w", err)
return fmt.Errorf("storage.Get id=%d: %w", id, err)
```

### Error chain

```
domain.ErrNotFound (sentinel)
    → usecases: fmt.Errorf("get note: %w", err)
    → adapters/http: writeError() → 404 Not Found
```

---

## 10. Metrics

### Structure

```go
// internal/infra/metrics/metrics.go
type Metrics struct {
    NotesCreatedTotal *prometheus.CounterVec
    RequestDuration   *prometheus.HistogramVec
}
```

### DI pattern for metrics

- In production: `metrics.New()` → Prometheus
- In unit tests: `MockMetrics` (mockgen) with `.AnyTimes()`
- In integration tests: `discardMetrics{}` (no-op struct)

```go
// Integration test — no-op stub
type discardMetrics struct{}
func (discardMetrics) IncCreated() {}
```

---

## 11. Graceful Shutdown

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
defer stop()

srv := &http.Server{...}

go func() {
    <-ctx.Done()
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _ = srv.Shutdown(shutdownCtx)
}()

return srv.ListenAndServe()
```

### sync.Once for safe Close

```go
type Worker struct {
    stop      chan struct{}
    closeOnce sync.Once
}

func (w *Worker) Close() error {
    w.closeOnce.Do(func() { close(w.stop) })
    return nil
}
```

---

## 12. Testing

### Mock generation

```go
// In the file with interfaces:
//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=notes
```

### Unit tests (testify/suite + gomock)

```go
type ManagerTestSuite struct {
    suite.Suite
    ctrl    *gomock.Controller
    storage *MockStorage
    log     *slog.Logger
}

func (s *ManagerTestSuite) SetupTest() {
    s.ctrl = gomock.NewController(s.T())
    s.storage = NewMockStorage(s.ctrl)
}

func (s *ManagerTestSuite) TearDownTest() {
    s.ctrl.Finish()
}

func TestManagerTestSuite(t *testing.T) {
    suite.Run(t, new(ManagerTestSuite))
}
```

### Integration tests (testcontainers + testify/suite)

```go
//go:build integration

package tests

type NotesTestSuite struct {
    suite.Suite
    db  *sql.DB
    log *slog.Logger
}

func (s *NotesTestSuite) SetupTest() {
    // Truncate tables between tests
    _, _ = s.db.Exec("DELETE FROM notes")
}

func (s *NotesTestSuite) TestCreateAndGet() {
    ctx := s.T().Context() // always s.T().Context()
    // ...
}

func TestNotesTestSuite(t *testing.T) {
    t.Parallel()
    suite.Run(t, new(NotesTestSuite))
}
```

### Rules

- `//go:build integration` — integration tests in `tests/` or `*_integration_test.go`
- `s.T().Context()` — always, never `context.Background()`
- Never store `context.Context` in suite fields
- `SetupTest()` — clean state between tests
- Mock metrics with `.AnyTimes()` — don't overload tests with exact call counts

### Running

```bash
make test                          # All tests (unit + integration, -race)
go test ./internal/usecases/...   # Unit only
go test -tags=integration ./tests/ # Integration only
go generate ./...                  # Regenerate mocks
```

---

## 13. Security

### API keys and secrets

- Secrets passed **only** via env vars
- Never hardcode secrets in code, tests, or committed configs
- Local secrets in `.env.local` (add to `.gitignore`)

---

## 14. Modern Go

### Go 1.22+: range over int

```go
// Instead of:
for i := 0; i < 10; i++ { ... }
// Use:
for range 10 { ... }
for i := range 10 { ... }
```

### Go 1.24+: t.Context()

```go
// In tests — automatically cancelled when test finishes
func TestSomething(t *testing.T) {
    ctx := t.Context()
}

// In testify/suite:
func (s *Suite) TestSomething() {
    ctx := s.T().Context()
}
```

### Go 1.25+: sync.WaitGroup.Go

```go
// Instead of:
wg.Add(1)
go func() {
    defer wg.Done()
    doWork()
}()

// Use:
wg.Go(func() {
    doWork()
})
```

### Go 1.26: go fix ./...

Automatically modernizes code. Run when upgrading the toolchain:

```bash
go fix ./...
```

### Structured logging (slog)

Use `log/slog`. JSON format in production, text in development.

```go
log.InfoContext(ctx, "note created", "id", note.ID, "title", note.Title)
```

---

## 15. Anti-patterns

### Context stored in struct fields

```go
// WRONG
type Service struct {
    ctx context.Context // anti-pattern
}

// RIGHT: context passed as first method argument
func (s *Service) Process(ctx context.Context, ...) error
```

### Interface next to implementation (Java-style)

In Go, interfaces are defined by the **consumer**, not the provider.

```go
// RIGHT: each consumer defines its own interface
// usecases/notes/manager.go
type Storage interface {
    Get(ctx context.Context, id int64) (*domain.Note, error)
}

// adapters/http/handler.go — its OWN interface, not reusing notes.Storage
type NoteGetter interface {
    Get(ctx context.Context, id int64) (*domain.Note, error)
}
```

### Constructor returns interface

```go
// WRONG
func NewRepository(db *sql.DB) Storage { ... }

// RIGHT: return concrete type
func NewRepository(db *sql.DB) *Repository { ... }
```

### Premature interfaces

Don't create an interface if it has one consumer and one implementation. Create an interface when:
- Mocking in tests is needed
- Multiple implementations exist
- Decoupling between packages is required

### Nil-checks for required dependencies

```go
// WRONG: nil-check before every call
if m.storage != nil {
    m.storage.Create(ctx, note)
}

// RIGHT: required dep via constructor, no-op in tests via mock
func NewManager(storage Storage, log *slog.Logger) *Manager
```

---

## 16. Deploy

### Dockerfile

Multi-stage: builder (Go image) → scratch (minimal image).

```dockerfile
FROM golang:1.25 AS builder
WORKDIR /src
COPY . .
RUN make build

FROM scratch
COPY --from=builder /src/bin/server /usr/local/bin/server
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENTRYPOINT ["server"]
```

---

## 17. Cheatsheet

### Commands

```bash
make build            # Build
make test             # All tests (-race)
make lint             # golangci-lint
go generate ./...     # Regenerate mocks
docker compose up -d  # Local infrastructure
```

### Tech stack

| Component | Library |
|-----------|---------|
| CLI | `urfave/cli/v3` |
| Logging | `log/slog` |
| HTTP router | `net/http` (Go 1.22+ patterns) or `go-chi/chi` |
| Markdown | `yuin/goldmark` |
| HTML sanitization | `microcosm-cc/bluemonday` |
| PostgreSQL | `uptrace/bun` + `pgdriver` |
| SQLite | `modernc.org/sqlite` |
| Migrations | `pressly/goose/v3` (embedded SQL) |
| Tests | `stretchr/testify/suite` + `go.uber.org/mock` |
| Containers | `testcontainers/testcontainers-go` |

