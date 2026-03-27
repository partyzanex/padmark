# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**padmark** — a Go HTTP service for creating, storing, and rendering Markdown notes with content negotiation (JSON, rendered HTML, raw markdown). See `docs/TODO.md` for the roadmap and `docs/GUIDE.md` for full architectural conventions.

## Commands

```bash
make test                                               # All tests (-v -count=1 -race, coverage)
go test -v -count=1 -race ./internal/... -run TestName  # Single test
go test -tags=integration ./tests/                      # Integration tests only
make lint                                               # golangci-lint (auto-installs)
make build                                              # Build binaries from cmd/
make gen                                                # go generate (mockgen)
make cover                                              # HTML coverage report
```

## Architecture — Clean Architecture

```
domain  <--  usecases  <--  infra
                        <--> adapters
```

- **domain** (`internal/domain/`) — entities and sentinel errors. Stdlib only, **no interfaces**.
- **usecases** (`internal/usecases/notes/`) — business logic. Dependency interfaces defined **here**, next to consumer (DIP). Struct named `Manager`.
- **infra** (`internal/infra/`) — implementations: storage (`memory`, `sqlite`, `postgres`), markdown renderer, CLI/config (`infra/cmd/`).
- **adapters** (`internal/adapters/http/`) — HTTP handlers, content negotiation, error translation.

### Naming

- Business logic struct: `Manager` (not `UseCase`, not `Service`)
- Infra structs: `Repository`, `Cache` — package name provides context, don't prefix with storage type
- Constructors return **concrete types**: `NewRepository() *Repository`, never an interface
- Each adapter defines **its own** interfaces — never reuse from another layer

### Dependency injection

Manual DI in `internal/infra/cmd/app.go`, `action()` function. Order:
1. Logger → 2. Infrastructure (storage, renderer) → 3. Business logic → 4. HTTP handler/router → 5. Server + graceful shutdown

### Configuration (`flags.go`)

Three constant groups: flag names (`Flag*`), env vars (`Env*`), defaults (`Default*`). Env prefix: `PADMARK_*`.

### Error handling

- Sentinel errors in domain: `var ErrNotFound = errors.New("not found")`
- Always wrap with operation context: `fmt.Errorf("get note: %w", err)`
- Translate storage errors to domain errors: `sql.ErrNoRows` → `domain.ErrNotFound`
- Adapters translate to HTTP status codes via a dedicated `writeError()` function

## Coding Rules

- **Never** store `context.Context` in struct fields — pass as first method argument
- Required dependencies via constructor; no nil-checks needed
- Metrics via interface: mock with `.AnyTimes()` in unit tests, no-op struct in integration tests
- Only create an interface when: mocking in tests, multiple implementations, or cross-package decoupling
- No mutex around network I/O

## Testing

- Unit tests: `testify/suite` + `go.uber.org/mock/mockgen`
- Declare mocks: `//go:generate go run go.uber.org/mock/mockgen@latest -source=manager.go -destination=manager_mocks_test.go -package=notes`
- Always use `s.T().Context()` in tests, never `context.Background()`
- Integration tests: `//go:build integration`, placed in `tests/`, use testcontainers
- `SetupTest()` cleans state between tests (truncate tables, reset in-memory store)
- Async assertions via a polling helper `waitFor(timeout, fn)`

## Modern Go

- `for range N` instead of `for i := 0; i < N; i++` (1.22+)
- `t.Context()` / `s.T().Context()` instead of `context.Background()` in tests (1.24+)
- `wg.Go(func() { ... })` instead of manual `wg.Add(1)` + goroutine (1.25+)
- `log/slog` for structured logging; JSON format in production, text in development
- Run `go fix ./...` when upgrading the toolchain

## Linting

golangci-lint v2, nearly all linters enabled:
- Max line length: 121, tab width 2
- Max cyclomatic complexity: 12
- JSON tags: `snake_case`; YAML tags: `camelCase`
- Min variable name length: 2
- Build tags include `integration`; Go version: 1.25
- Relaxed rules for `_test.go` (dupl, wrapcheck, errcheck, etc. excluded)

## Anti-patterns to Avoid

- Storing `context.Context` in struct fields
- Defining interfaces next to the implementation (Java-style) — define at consumer
- Constructor returning an interface instead of a concrete type
- Creating interfaces preemptively (only one consumer + one implementation = no interface needed)
- Nil-checking required dependencies instead of passing them via constructor

## Infrastructure Notes

- `*.mk` files are auto-generated from `github.com/partyzanex/go-makefile` — do not edit manually
- Binaries go to `bin/` (gitignored); vendor is committed (`go mod vendor`)
- `cmd/server/main.go` is 10–15 lines; all logic lives in `internal/infra/cmd/`
- Dockerfile: multi-stage build → scratch image
