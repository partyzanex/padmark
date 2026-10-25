# Browser UI tests

The browser suite uses Go, [playwright-go](https://github.com/mxschmitt/playwright-go)
and the standard `testing` runner. Its separate `go.mod` and `go.sum` keep browser
dependencies out of the application module. Sources, configuration, screenshots,
downloaded tools and reports live under `tests/ui/`. The test server binary and each
server's database and keyring go to the system temporary directory and are removed
after the run.

Every scenario runs on desktop (1280 × 900) and mobile (390 × 844).
They cover field entry, editor formatting, validation,
publish/save, clipboard actions, raw view, burn confirmation, theme persistence,
setup/login, password changes, admin invites, keys and user revocation. Link scenarios cover
creation, validation, failure/retry, editing/deletion, expiry preservation/reset, redirect
settings, burn after click and owner-only access to the editor.

Each scenario starts a real Padmark server with its own temporary SQLite database,
keyring and browser context. Inherited `PADMARK_*` settings are removed from the
server environment. Test servers use lower Argon2 costs. The test binary starts each
server through a copy of itself that kills the server when the test process exits,
also after a `-timeout` panic. A start that loses its port to a parallel server is
retried on a new port. Every scenario fails on
uncaught JavaScript errors, console errors, HTTP errors and failed resource requests.
Intentional HTTP errors must be allowed explicitly; missing static resources always
fail. One simulated 503 checks the editor's failure/retry UI.

## Run

Requires the Go version in the root `go.mod`. A separate Node.js/npm installation is
not required: playwright-go downloads its internal Node.js driver and Chromium into
`tests/ui/.cache/`. This is still a Playwright driver internally, not a pure Go browser
engine. Minimal Linux hosts also need Chromium's OS libraries; the CI container
already includes those libraries and the matching browser.

```sh
make test-ui-setup        # Download Go dependencies, driver and Chromium (first run)
make test-ui              # All browser checks, including screenshots
make test-ui-headed       # Watch the Chromium window (screenshots are not compared)
make test-ui-debug        # Step through actions in the Playwright Inspector
make test-ui-update       # Update references after an intentional design change
make test-ui-report       # Open the last HTML report; Ctrl+C stops the local server
make test-ui-lint         # golangci-lint for this module with the root .golangci.yml
```

The root Makefile delegates to `tests/ui/Makefile`. `UI_ARGS` now takes standard
`go test` arguments:

```sh
make test-ui UI_ARGS='-run TestEditor/desktop/Toolbar'
make test-ui-headed UI_ARGS='-run TestAccounts/mobile/Login'
make test-ui UI_ARGS='-race'
```

You can also run `go test -v -count=1 -parallel=2 .` from `tests/ui/` after setup.
Use `-args -headed` or `-args -update` for the custom browser/snapshot flags. The
Makefile clears inherited `GOROOT` so the selected Go executable locates its SDK.
The nested module is excluded from root `go test ./...`; run browser tests explicitly.
Headed and debug runs skip screenshot comparisons: the references come from headless
Chromium, and the full headed browser renders fonts and scrollbars differently. `-update`
with `-headed` is refused for the same reason.

## Writing scenarios

Scenarios are ordinary Go functions in `editor_test.go`, `links_test.go` and `accounts_test.go`.
Add a case to `TestEditor`, `TestLinks` or `TestAccounts`; `runScenarios` runs it at both screen
sizes with a fresh server, context, diagnostics and failure artifacts.

```go
func testExample(e *ui) {
    e.gotoPage("/")
    e.fill("#titleInput", "Example")
    e.fill("#editor", "# Hello")
    e.button("Publish")
    e.must(e.expect.Page(e.page).ToHaveURL(regexpURL("/success?")))
    e.link("Go to paste")
    e.visible(".markdown-body h1")
}
```

Assertions use Playwright's waiting assertions and `testify/require`. Prefer semantic
role/label locators where available and stable IDs for editor controls. Check the
visible result after submitting. Avoid fixed sleeps and dependencies between cases.
The auth helper provisions a fixture admin over JSON and signs in through the real
browser form; TOTP codes use the same `pquerna/otp` library as the application.

## Screenshots and reports

Committed PNGs live in `testdata/screenshots/<desktop|mobile>/<OS>/`. Layout checks
verify that controls are visible and stay inside the viewport. Image comparisons
use Go pixelmatch, with a 0.2 color threshold and at most 100 differing pixels.
Random secrets/URLs are masked, dates normalized, and captures must stabilize before
comparison. Missing references fail until reviewed and explicitly updated with
`make test-ui-update`.

References are maintained for macOS and Linux. Review PNG changes alongside intentional UI
changes. OS rendering can differ, so
update references on each supported OS. CI uses the pinned Playwright 1.62.1 `noble`
image corresponding to playwright-go v0.6201.1.

The HTML summary and machine-readable results are written to `artifacts/index.html`
and `artifacts/results.json`. Failed scenarios retain a screenshot, server log and
Playwright trace; visual failures also save expected/actual PNGs and a diff when the
dimensions match. CI uploads `tests/ui/artifacts/`.

To inspect a failure trace from `tests/ui/`:

```sh
PLAYWRIGHT_DRIVER_PATH="$PWD/.cache/driver" go run github.com/mxschmitt/playwright-go/cmd/playwright \
  show-trace artifacts/<scenario>/trace.zip
```

`PADMARK_UI_SERVER_BINARY` optionally selects a prebuilt server for cross-platform
container checks. It must point to the current source's binary; tests still create
their own database/keyring and never connect to an existing installation.
