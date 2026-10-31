package ui_test

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

const (
	bootstrapToken = "padmark-ui-only-bootstrap-token-1234567890"
	password       = "Padmark-UI-Password-42!" //nolint:gosec // G101: a test-only password for a throwaway server
)

type viewport struct {
	name, device  string
	width, height int
}

// viewports returns the screen sizes that every scenario runs on.
func viewports() []viewport {
	return []viewport{{"desktop", "Desktop Chrome", 1280, 900}, {"mobile", "Pixel 7", 390, 844}}
}

var nonempty = regexp.MustCompile(`\S+`)

func regexpURL(path string) *regexp.Regexp { return regexp.MustCompile(regexp.QuoteMeta(path)) }

type scenario struct {
	name            string
	accounts        bool
	allowedStatuses []int
	run             func(*uiSession)
}

func runScenarios(t *testing.T, cases []scenario) {
	t.Helper()

	for _, screen := range viewports() {
		t.Run(screen.name, func(t *testing.T) {
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					t.Parallel()
					test.run(newUI(t, screen, test))
				})
			}
		})
	}
}

type uiSession struct {
	t              *testing.T
	page           playwright.Page
	browserContext playwright.BrowserContext
	expect         playwright.PlaywrightAssertions
	screen         viewport
	baseURL        string
	artifacts      string
	errors         safeLog
}

type safeLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (log *safeLog) Write(data []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()

	return log.buffer.Write(data)
}
func (log *safeLog) String() string {
	log.mu.Lock()
	defer log.mu.Unlock()

	return log.buffer.String()
}

func newUI(t *testing.T, screen viewport, test scenario) *uiSession {
	t.Helper()

	started := time.Now()
	baseURL, serverLog := startServer(t, test.accounts)
	device := runner.playwright.Devices[screen.device]
	context, err := runner.browser.NewContext(playwright.BrowserNewContextOptions{
		BaseURL:           new(baseURL),
		Viewport:          &playwright.Size{Width: screen.width, Height: screen.height},
		UserAgent:         new(device.UserAgent),
		DeviceScaleFactor: new(device.DeviceScaleFactor),
		IsMobile:          new(device.IsMobile), HasTouch: new(device.HasTouch),
		Locale: new("en-US"), TimezoneId: new("UTC"),
		ColorScheme: playwright.ColorSchemeDark, ReducedMotion: playwright.ReducedMotionReduce,
	})
	require.NoError(t, err)
	context.SetDefaultTimeout(5_000)

	if os.Getenv("PWDEBUG") != "" {
		context.SetDefaultTimeout(0)
	}

	sess := &uiSession{
		t: t, browserContext: context, screen: screen, baseURL: baseURL,
		expect:    playwright.NewPlaywrightAssertions(),
		artifacts: filepath.Join("artifacts", strings.ReplaceAll(t.Name(), "/", "-")),
	}
	require.NoError(t, os.MkdirAll(sess.artifacts, 0o700))
	sess.collectBrowserErrors(test.allowedStatuses)
	require.NoError(t, context.Tracing().Start(playwright.TracingStartOptions{
		Screenshots: new(true), Snapshots: new(true), Sources: new(true),
	}))
	t.Cleanup(func() { sess.finish(started, serverLog) })

	sess.page, err = context.NewPage()
	require.NoError(t, err)

	return sess
}

// collectBrowserErrors records JavaScript, console, HTTP and network errors, except the allowed statuses.
func (sess *uiSession) collectBrowserErrors(allowedStatuses []int) {
	baseURL := sess.baseURL

	sess.browserContext.OnWebError(func(event playwright.WebError) { sess.errorf("JavaScript: %v", event.Error()) })
	sess.browserContext.OnConsole(func(message playwright.ConsoleMessage) {
		if message.Type() != "error" {
			return
		}

		text := message.Text()
		for _, status := range allowedStatuses {
			if strings.HasPrefix(text, "Failed to load resource:") &&
				strings.Contains(text, fmt.Sprintf("status of %d", status)) {
				return
			}
		}

		sess.errorf("Console: %s", text)
	})
	sess.browserContext.OnResponse(func(response playwright.Response) {
		if response.Status() < 400 {
			return
		}

		ownRequest := strings.HasPrefix(response.URL(), baseURL+"/")
		if ownRequest && slices.Contains(allowedStatuses, response.Status()) &&
			!strings.HasPrefix(strings.TrimPrefix(response.URL(), baseURL), "/static/") {
			return
		}

		sess.errorf("HTTP %d: %s", response.Status(), response.URL())
	})
	sess.browserContext.OnRequestFailed(func(request playwright.Request) {
		failure := fmt.Sprint(request.Failure())
		if !strings.Contains(failure, "ERR_ABORTED") {
			sess.errorf("Network: %s: %s", request.URL(), failure)
		}
	})
}

// finish runs as the test cleanup. It reports browser errors, saves failure artifacts and closes the context.
func (sess *uiSession) finish(started time.Time, serverLog *safeLog) {
	t := sess.t

	if diagnostics := sess.errors.String(); diagnostics != "" {
		t.Errorf("browser errors:\n%s", diagnostics)
	}

	if t.Failed() && sess.page != nil && !sess.page.IsClosed() {
		_, screenshotErr := sess.page.Screenshot(playwright.PageScreenshotOptions{
			Path: new(filepath.Join(sess.artifacts, "failure.png")), FullPage: new(true),
			Animations: playwright.ScreenshotAnimationsDisabled, Scale: playwright.ScreenshotScaleCss,
		})
		if screenshotErr != nil {
			t.Log("capture failure:", screenshotErr)
		}
	}

	var tracePath []string
	if t.Failed() {
		tracePath = append(tracePath, filepath.Join(sess.artifacts, "trace.zip"))
	}

	traceErr := sess.browserContext.Tracing().Stop(tracePath...)
	if traceErr != nil {
		t.Error("stop trace:", traceErr)
	}

	closeErr := sess.browserContext.Close()
	if closeErr != nil {
		t.Error("close browser context:", closeErr)
	}

	if t.Failed() {
		writeErr := os.WriteFile(filepath.Join(sess.artifacts, "server.log"), []byte(serverLog.String()), 0o600)
		if writeErr != nil {
			t.Error(writeErr)
		}
	}

	runner.report.add(result{
		Name: t.Name(), Failed: t.Failed(), Directory: sess.artifacts,
		Duration: time.Since(started).Round(time.Millisecond).String(),
	})
}

// serverStartAttempts bounds retries of a start that lost its port to a parallel server.
const serverStartAttempts = 3

// errAddressInUse marks a server start that lost its port; startServer retries it.
var errAddressInUse = errors.New("address already in use")

func startServer(t *testing.T, accounts bool) (string, *safeLog) {
	t.Helper()
	// t.TempDir is removed after the server cleanup below, which runs first (LIFO).
	directory := t.TempDir()

	var env []string

	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PADMARK_") {
			env = append(env, value)
		}
	}

	keyring := filepath.Join(directory, "keys.json")
	keygen := exec.CommandContext( //nolint:gosec // G204: the test starts its own server binary
		t.Context(), runner.binary, "encryption", "keygen", "--key-id", "ui", "--output", keyring)
	keygen.Env = env
	output, err := keygen.CombinedOutput()
	require.NoError(t, err, "generate test keyring: %s", output)
	// The free port is released before the server binds it, so a parallel server can take it first.
	for attempt := 1; ; attempt++ {
		baseURL, logs, launchErr := launchServer(t, env, directory, keyring, accounts)
		if launchErr == nil {
			return baseURL, logs
		}

		if !errors.Is(launchErr, errAddressInUse) || attempt == serverStartAttempts {
			t.Fatal(launchErr)
		}
	}
}

// launchServer starts one server through the supervisor (see supervisorEnv) and waits until it is ready.
func launchServer(t *testing.T, env []string, directory, keyring string, accounts bool) (string, *safeLog, error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	self, err := os.Executable()
	require.NoError(t, err)

	command := exec.Command(self, runner.binary, //nolint:gosec // G204: the test re-runs its own binary
		"--addr", address, "--storage", "sqlite", "--dsn", filepath.Join(directory, "test.db"),
		"--content-keyring-file", keyring, fmt.Sprintf("--enable-accounts=%t", accounts),
		"--custom-slugs", "--rate-limit", "0", "--argon2-memory", "8192", "--argon2-time", "1")

	command.Env = append(slices.Clone(env), "PADMARK_BOOTSTRAP_TOKEN="+bootstrapToken, supervisorEnv+"=1")
	logs := &safeLog{}
	command.Stdout, command.Stderr = logs, logs
	// Nothing is written to this pipe: it only closes when this process exits (or below).
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())

	done := make(chan struct{})

	var exitErr error
	go func() { exitErr = command.Wait(); close(done) }()

	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}

		_ = command.Process.Signal(syscall.SIGTERM)

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			// On EOF the supervisor kills the server and exits.
			_ = stdin.Close()

			<-done
		}
	})

	baseURL := "http://" + address

	return baseURL, logs, waitReady(t, baseURL, done, func() error { return exitErr }, logs)
}

// waitReady polls /readyz until the server answers 200, exits, or the 10-second limit passes.
func waitReady(t *testing.T, baseURL string, done <-chan struct{}, exitErr func() error, logs *safeLog) error {
	t.Helper()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	defer client.CloseIdleConnections()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	deadline := time.After(10 * time.Second)

	for {
		request, requestErr := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/readyz", nil)
		require.NoError(t, requestErr)

		response, requestErr := client.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}

		select {
		case <-done:
			if strings.Contains(logs.String(), errAddressInUse.Error()) {
				return fmt.Errorf("UI server on %s: %w", baseURL, errAddressInUse)
			}
			//nolint:errorlint // the exit error may be nil after a clean exit
			return fmt.Errorf("UI server exited: %v\n%s", exitErr(), logs.String())
		case <-deadline:
			return fmt.Errorf("UI server did not become ready:\n%s", logs.String())
		case <-t.Context().Done():
			return t.Context().Err()
		case <-ticker.C:
		}
	}
}

func (sess *uiSession) errorf(format string, args ...any) {
	_, _ = fmt.Fprintf(&sess.errors, format+"\n", args...)
}
func (sess *uiSession) must(err error) { sess.t.Helper(); require.NoError(sess.t, err) }
func (sess *uiSession) gotoPage(path string) {
	sess.t.Helper()
	_, err := sess.page.Goto(path)
	sess.must(err)
}
func (sess *uiSession) fill(selector, value string) {
	sess.t.Helper()
	sess.must(sess.page.Locator(selector).Fill(value))
}
func (sess *uiSession) click(selector string) {
	sess.t.Helper()
	sess.must(sess.page.Locator(selector).Click())
}
func (sess *uiSession) selectOption(selector, value string) {
	sess.t.Helper()
	_, err := sess.page.Locator(selector).SelectOption(playwright.SelectOptionValues{Values: &[]string{value}})
	sess.must(err)
}
func (sess *uiSession) text(selector, value string) {
	sess.t.Helper()
	sess.must(sess.expect.Locator(sess.page.Locator(selector)).ToHaveText(value))
}
func (sess *uiSession) value(selector, value string) {
	sess.t.Helper()
	sess.must(sess.expect.Locator(sess.page.Locator(selector)).ToHaveValue(value))
}
func (sess *uiSession) visible(selector string) {
	sess.t.Helper()
	sess.must(sess.expect.Locator(sess.page.Locator(selector)).ToBeVisible())
}

//nolint:ireturn // playwright-go returns any
func (sess *uiSession) eval(expression string) any {
	sess.t.Helper()
	value, err := sess.page.Evaluate(expression)
	sess.must(err)

	return value
}

//nolint:ireturn // playwright-go returns an interface
func (sess *uiSession) role(role *playwright.AriaRole, name string) playwright.Locator {
	return sess.page.GetByRole(*role, playwright.PageGetByRoleOptions{Name: name, Exact: new(true)})
}
func (sess *uiSession) button(name string) {
	sess.t.Helper()
	sess.must(sess.role(playwright.AriaRoleButton, name).Click())
}
func (sess *uiSession) link(name string) {
	sess.t.Helper()
	sess.must(sess.role(playwright.AriaRoleLink, name).Click())
}

func (sess *uiSession) expectLayout(selectors ...string) {
	sess.t.Helper()
	sess.eval("() => document.fonts.ready")

	for _, selector := range selectors {
		locator := sess.page.Locator(selector)
		sess.must(sess.expect.Locator(locator).ToBeVisible())
		box, err := locator.BoundingBox()
		sess.must(err)
		require.NotNil(sess.t, box, "%s has a layout box", selector)
		require.Greater(sess.t, box.Width, float64(0), selector)
		require.GreaterOrEqual(sess.t, box.X, float64(-1), selector)
		require.LessOrEqual(sess.t, box.X+box.Width, float64(sess.screen.width+1), selector)
	}

	overflow, ok := sess.eval("() => document.documentElement.scrollWidth - innerWidth").(int)
	require.True(sess.t, ok, "scrollWidth overflow must be an int")
	require.LessOrEqual(sess.t, overflow, 1, "no horizontal overflow")
}

func (sess *uiSession) publishNote() (string, string) {
	sess.t.Helper()
	sess.gotoPage("/")
	sess.fill("#titleInput", "UI test note")
	sess.fill("#editor", "# Hello\n\nThis is **Markdown**.")
	sess.button("Publish")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.must(sess.expect.Locator(sess.page.Locator("#editCodeValue")).ToHaveText(nonempty))
	code, err := sess.page.Locator("#editCodeValue").InnerText()
	sess.must(err)
	link, err := sess.role(playwright.AriaRoleLink, "Go to paste").GetAttribute("href")
	sess.must(err)
	require.NotEmpty(sess.t, link)

	return strings.TrimSpace(code), link
}

func (sess *uiSession) admin() string {
	sess.t.Helper()
	response, err := sess.browserContext.Request().Post("/setup", playwright.APIRequestContextPostOptions{
		Headers: map[string]string{"Accept": "application/json"},
		Data:    map[string]string{"username": "ui-admin", "password": password, "bootstrap_token": bootstrapToken},
	})
	sess.must(err)
	require.Equal(sess.t, http.StatusCreated, response.Status())

	var body struct {
		URI string `json:"otp_auth_uri"`
	}
	sess.must(response.JSON(&body))
	enrollment, err := url.Parse(body.URI)
	sess.must(err)

	secret := enrollment.Query().Get("secret")
	require.NotEmpty(sess.t, secret)
	sess.gotoPage("/login?next=/admin")
	sess.fill("#username", "ui-admin")
	sess.fill("#password", password)
	sess.fill("#code", sess.code(secret, time.Now()))
	sess.button("Sign in")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(sess.baseURL + "/admin"))

	return secret
}

func (sess *uiSession) code(secret string, at time.Time) string {
	sess.t.Helper()

	code, err := totp.GenerateCode(secret, at)
	sess.must(err)

	return code
}

// expectPrivacyOptions checks the values the editor's privacy select offers, in order.
func (sess *uiSession) expectPrivacyOptions(values ...string) {
	sess.t.Helper()

	options := sess.page.Locator("#privacySelect option")
	sess.must(sess.expect.Locator(options).ToHaveCount(len(values)))

	for index, value := range values {
		sess.must(sess.expect.Locator(options.Nth(index)).ToHaveAttribute("value", value))
	}
}
