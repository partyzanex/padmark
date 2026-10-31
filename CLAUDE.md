# CLAUDE.md

Project guidance for development and maintenance of this repository.

## Project

**padmark** is a Go service for Markdown notes, short links and account-based access,
with HTML/JSON/raw content negotiation and encrypted storage. Use Go 1.27.3 as specified
in [go.mod](go.mod). Read [README.md](README.md) for operation and
[docs/GUIDE.md](docs/GUIDE.md) for architecture.

## Commands

```bash
go test ./...                         # Application-module unit tests
make test                             # Unit + integration, -race, coverage; needs Docker
go test -v -count=1 -race ./internal/... -run TestName
go test -tags=integration ./tests/... ./internal/infra/storage/postgres
make test-ui-setup                    # Install Go UI-test dependencies, driver and Chromium
make test-ui                          # Browser tests in the separate tests/ui module
make test-ui-headed                   # Show the browser
make test-ui-debug                    # Playwright Inspector
make test-ui-report                   # Open the browser-test report
make test-ui-update                   # Update reference PNGs after intentional UI changes
make lint                            # Application-module golangci-lint
make build                           # Build binaries from cmd/
make gen                             # Generate code and mocks
make cover                           # Application-module HTML coverage report
```

Browser sources, dependencies, configuration, snapshots and artifacts belong in `tests/ui/`.
That directory is a separate Go module: root `go test ./...` and `make test` do not include it.
Select a case with `make test-ui UI_ARGS='-run TestEditor/desktop/Toolbar'`.
There is no npm/TypeScript setup; playwright-go downloads its internal Node.js driver.

## Architecture

Dependencies point inward: adapters and infrastructure depend on usecases and domain.

- **domain** (`internal/domain/`): entities, validation and sentinel errors; no interfaces.
  Dependencies are stdlib plus `github.com/google/uuid` for account ownership IDs.
- **usecases** (`internal/usecases/`): note/link lifecycle, account management and content-key
  migration/rotation. Each consumer defines its dependency interfaces beside its implementation.
- **infra** (`internal/infra/`): SQLite/PostgreSQL repositories, rendering, crypto, caches,
  metrics and server composition/configuration in `infra/server/`.
- **adapters** (`internal/adapters/`): HTTP, CLI and MCP. HTTP templates, CSS, JavaScript,
  fonts, OpenAPI and `/llms.txt` are embedded from `adapters/web/`.

### Naming and dependency injection

- Business logic uses `Manager`; persistence roles include `NoteRepository`, `LinkRepository`
  and `UserRepository`. Backend names live in package names, not type prefixes.
- Constructors return concrete types. Use constructor arguments for required dependencies;
  optional caches/configuration may use explicit options.
- Each adapter defines its own interfaces instead of importing another adapter's interfaces.
- Manual wiring lives in `internal/infra/server/server.go`, through `serverAction`,
  `buildHandlers` and `buildRouter`; do not introduce a DI framework.
- HTTP middleware and its request context live in `internal/adapters/web/middleware/`.
  Keep it independent of the parent `web` package; shared JSON errors live in `web/response/`.
- Notes and links share the same slug-lock instance for local preflight/cache coordination.
  Storage triggers enforce cross-process uniqueness through the `slug_namespace` primary key.
- `NewHandler(manager, linkManager, log)` requires both managers.
  `NewRouter(handler, ogenHandler, opts)` receives `*RouterOptions` and returns `(http.Handler, error)`.
  Use `NoPinger{}` for test setups without a DB pinger.

### Configuration and storage

- Flag names, environment variables and defaults are grouped in `internal/infra/server/flags.go`.
  Server variables use `PADMARK_*`; `PADMARK_BOOTSTRAP_TOKEN` is environment-only.
- File-backed SQLite defaults to `padmark-keys.json` beside the DB; PostgreSQL uses the
  working directory. The container works in `/data`, persisted by a named volume in Compose.
  Only `serve` generates missing default keys. Explicit paths must already exist;
  in-memory/temporary SQLite needs an explicit path.
- `serve` completes pending schema/content migrations automatically before listening.
  `content_encryption_state.phase` records completion; ready restarts skip migration writes.
  Stop old-version writers and preserve DB/keyring backups when upgrading. The `encryption`
  commands remain optional maintenance tools and never create a keyring implicitly.
- Keep installation keys and real credentials out of the repository. Test credentials must
  belong only to isolated fixtures. Back up database and keyring separately.
- SQL migrations live in `migrations/sqlite/` and `migrations/postgres/` and are embedded per dialect.

### HTTP and access control

- `domain.ReservedSlugPrefixes()` is the source of truth for named first-segment routes.
  Add new route prefixes there so they are not mistaken for note IDs.
- Public note reads use `/{id}` or `/notes/{id}`. Handlers enforce per-record privacy.
  Ownership follows the creating account ID across that account's sessions and API tokens.
- Browser auth failures redirect to `/login?next=...`; `safeNextURL` rejects external URLs.
- `--disable-api` makes the editor read-only: publish/save call the disabled REST endpoints.
  Note views, link redirects, account/admin forms and health checks remain available.
- Delete credentials use `X-Edit-Code`. Notes retain the deprecated query-string fallback
  for v0.1.6 clients; links ignore query-string edit codes.
- Translate domain errors centrally through `errors.go`/`ogen_errors.go`; JSON responses use
  `{"message": ..., "code"?: ...}`. Do not expose wrapped internal errors to clients.

## Coding rules

- Never store `context.Context` in struct fields; pass it as the first argument.
- Wrap errors with operation context and translate DB errors to domain errors.
- Required dependencies need no repeated nil checks. Use mocks or no-op implementations in tests.
- Add interfaces for testing, multiple implementations or cross-package decoupling.
- Do not hold a mutex across network I/O.
- Use `log/slog` (JSON in production, text for local debugging).
- Prefer `for range N`, `t.Context()` / `s.T().Context()` and `sync.WaitGroup.Go` where applicable.
  Run and review `go fix ./...` when upgrading the toolchain.

## Testing

- Unit tests live beside application packages and use `testify/suite`, `require` and gomock.
- Generate mocks from consumer interfaces with the checked-in `//go:generate` directives;
  HTTP mock directives may target `web_test`, not the implementation package.
- Use `t.Context()` / `s.T().Context()` in tests, never a stored background context.
- Integration suites in `tests/` and PostgreSQL repository tests use the `integration` tag.
  SQLite can use temporary/in-memory databases; PostgreSQL uses testcontainers and needs Docker.
- Clean state between tests. Use polling/waiting assertions instead of fixed sleeps.
- Browser tests are Go scenarios in `tests/ui/`, with separate dependencies and their own Makefile.
  See [tests/ui/README.md](tests/ui/README.md) before changing the harness or PNG references.
- UI assertions cover form behavior, browser diagnostics and desktop/mobile layout. Review
  screenshot changes before updating references; baseline images record the current UI.
- Mock metrics with `.AnyTimes()` when exact metric call counts are outside the test's scope.

## Linting and generated files

- Change linter configuration, exclusions or `nolint` directives only at the repository
  owner's explicit request. Fix code under the existing checks instead of weakening them.
- `.golangci.yml` defines enabled linters, exclusions and thresholds (121-column lines,
  complexity 12, JSON `snake_case`, YAML `camelCase`). `_test.go` has specific exclusions.
- CI pins golangci-lint v2.15.0 and Go 1.27.3. The lint config currently sets analysis
  language `go: '1.25'`; that is separate from the build-toolchain requirement.
- `*.mk` files are fetched from `github.com/partyzanex/go-makefile`; do not edit them manually.
  Add project targets to `Makefile` and browser-specific targets to `tests/ui/Makefile`.
- Application binaries go in ignored `bin/`. `vendor/` is ignored and is not a committed
  dependency source; normal clean-checkout builds use `go.mod`/`go.sum`.
- The production Dockerfile builds with `CGO_ENABLED=0` and runs only the server as UID/GID
  65534 in a scratch image. It contains no UI-test dependencies or report server.

## Frontend guidelines

- Current UI sources are `internal/adapters/web/templates/` and
  `internal/adapters/web/static/style.css`, with page-specific nonce-protected styles/scripts.
  There is no checked-in `docs/design/` mockup directory.
- Read the affected template, shared styles and any design reference supplied for the task.
  `docs/screens/` contains example screenshots; browser regression PNGs live in
  `tests/ui/testdata/screenshots/`.
- Preserve the established design unless the task requests a visual change. Reuse its CSS
  variables, spacing, classes and existing colors instead of introducing another style system.
- Fonts are self-hosted Source Serif 4 (display/prose) and JetBrains Mono (monospace UI/code).
  Themes are `light`, `dim` (default) and `dark`, selected through `[data-theme]`.
- Use `html/template`, keep CSP-compatible script/style loading, and check affected forms and
  desktop/mobile screenshots with `make test-ui` after a frontend change.
