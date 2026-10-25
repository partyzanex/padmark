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
	password       = "Padmark-UI-Password-42!"
)

type viewport struct {
	name, device  string
	width, height int
}

var viewports = []viewport{{"desktop", "Desktop Chrome", 1280, 900}, {"mobile", "Pixel 7", 390, 844}}
var nonempty = regexp.MustCompile(`\S+`)

func regexpURL(path string) *regexp.Regexp { return regexp.MustCompile(regexp.QuoteMeta(path)) }

type scenario struct {
	name            string
	accounts        bool
	allowedStatuses []int
	run             func(*ui)
}

func runScenarios(t *testing.T, cases []scenario) {
	t.Helper()
	for _, screen := range viewports {
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

type ui struct {
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
func (log *safeLog) String() string { log.mu.Lock(); defer log.mu.Unlock(); return log.buffer.String() }

func newUI(t *testing.T, screen viewport, test scenario) *ui {
	t.Helper()
	started := time.Now()
	baseURL, serverLog := startServer(t, test.accounts)
	device := runner.playwright.Devices[screen.device]
	context, err := runner.browser.NewContext(playwright.BrowserNewContextOptions{
		BaseURL:           playwright.String(baseURL),
		Viewport:          &playwright.Size{Width: screen.width, Height: screen.height},
		UserAgent:         playwright.String(device.UserAgent),
		DeviceScaleFactor: playwright.Float(device.DeviceScaleFactor),
		IsMobile:          playwright.Bool(device.IsMobile), HasTouch: playwright.Bool(device.HasTouch),
		Locale: playwright.String("en-US"), TimezoneId: playwright.String("UTC"),
		ColorScheme: playwright.ColorSchemeDark, ReducedMotion: playwright.ReducedMotionReduce,
	})
	require.NoError(t, err)
	context.SetDefaultTimeout(5_000)
	if os.Getenv("PWDEBUG") != "" {
		context.SetDefaultTimeout(0)
	}
	e := &ui{t: t, browserContext: context, screen: screen, baseURL: baseURL,
		expect: playwright.NewPlaywrightAssertions(), artifacts: filepath.Join("artifacts", strings.ReplaceAll(t.Name(), "/", "-"))}
	require.NoError(t, os.MkdirAll(e.artifacts, 0o700))
	context.OnWebError(func(event playwright.WebError) { e.errorf("JavaScript: %v", event.Error()) })
	context.OnConsole(func(message playwright.ConsoleMessage) {
		if message.Type() != "error" {
			return
		}
		text := message.Text()
		for _, status := range test.allowedStatuses {
			if strings.HasPrefix(text, "Failed to load resource:") && strings.Contains(text, fmt.Sprintf("status of %d", status)) {
				return
			}
		}
		e.errorf("Console: %s", text)
	})
	context.OnResponse(func(response playwright.Response) {
		if response.Status() < 400 {
			return
		}
		ownRequest := strings.HasPrefix(response.URL(), baseURL+"/")
		if ownRequest && slices.Contains(test.allowedStatuses, response.Status()) &&
			!strings.HasPrefix(strings.TrimPrefix(response.URL(), baseURL), "/static/") {
			return
		}
		e.errorf("HTTP %d: %s", response.Status(), response.URL())
	})
	context.OnRequestFailed(func(request playwright.Request) {
		failure := fmt.Sprint(request.Failure())
		if !strings.Contains(failure, "ERR_ABORTED") {
			e.errorf("Network: %s: %s", request.URL(), failure)
		}
	})
	require.NoError(t, context.Tracing().Start(playwright.TracingStartOptions{
		Screenshots: playwright.Bool(true), Snapshots: playwright.Bool(true), Sources: playwright.Bool(true),
	}))
	t.Cleanup(func() {
		if diagnostics := e.errors.String(); diagnostics != "" {
			t.Errorf("browser errors:\n%s", diagnostics)
		}
		if t.Failed() && e.page != nil && !e.page.IsClosed() {
			_, screenshotErr := e.page.Screenshot(playwright.PageScreenshotOptions{
				Path: playwright.String(filepath.Join(e.artifacts, "failure.png")), FullPage: playwright.Bool(true),
				Animations: playwright.ScreenshotAnimationsDisabled, Scale: playwright.ScreenshotScaleCss,
			})
			if screenshotErr != nil {
				t.Log("capture failure:", screenshotErr)
			}
		}
		var tracePath []string
		if t.Failed() {
			tracePath = append(tracePath, filepath.Join(e.artifacts, "trace.zip"))
		}
		if traceErr := context.Tracing().Stop(tracePath...); traceErr != nil {
			t.Error("stop trace:", traceErr)
		}
		if closeErr := context.Close(); closeErr != nil {
			t.Error("close browser context:", closeErr)
		}
		if t.Failed() {
			if writeErr := os.WriteFile(filepath.Join(e.artifacts, "server.log"), []byte(serverLog.String()), 0o600); writeErr != nil {
				t.Error(writeErr)
			}
		}
		runner.report.add(result{Name: t.Name(), Failed: t.Failed(), Duration: time.Since(started).Round(time.Millisecond).String(), Directory: e.artifacts})
	})
	e.page, err = context.NewPage()
	require.NoError(t, err)
	return e
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
	keygen := exec.CommandContext(t.Context(), runner.binary, "encryption", "keygen", "--key-id", "ui", "--output", keyring)
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
	command := exec.Command(self, runner.binary,
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

func (e *ui) errorf(format string, args ...any) { _, _ = fmt.Fprintf(&e.errors, format+"\n", args...) }
func (e *ui) must(err error)                    { e.t.Helper(); require.NoError(e.t, err) }
func (e *ui) gotoPage(path string)              { e.t.Helper(); _, err := e.page.Goto(path); e.must(err) }
func (e *ui) fill(selector, value string)       { e.t.Helper(); e.must(e.page.Locator(selector).Fill(value)) }
func (e *ui) click(selector string)             { e.t.Helper(); e.must(e.page.Locator(selector).Click()) }
func (e *ui) selectOption(selector, value string) {
	e.t.Helper()
	_, err := e.page.Locator(selector).SelectOption(playwright.SelectOptionValues{Values: &[]string{value}})
	e.must(err)
}
func (e *ui) text(selector, value string) {
	e.t.Helper()
	e.must(e.expect.Locator(e.page.Locator(selector)).ToHaveText(value))
}
func (e *ui) value(selector, value string) {
	e.t.Helper()
	e.must(e.expect.Locator(e.page.Locator(selector)).ToHaveValue(value))
}
func (e *ui) visible(selector string) {
	e.t.Helper()
	e.must(e.expect.Locator(e.page.Locator(selector)).ToBeVisible())
}
func (e *ui) eval(expression string, args ...any) any {
	e.t.Helper()
	value, err := e.page.Evaluate(expression, args...)
	e.must(err)
	return value
}
func (e *ui) role(role *playwright.AriaRole, name string) playwright.Locator {
	return e.page.GetByRole(*role, playwright.PageGetByRoleOptions{Name: name, Exact: playwright.Bool(true)})
}
func (e *ui) button(name string) {
	e.t.Helper()
	e.must(e.role(playwright.AriaRoleButton, name).Click())
}
func (e *ui) link(name string) { e.t.Helper(); e.must(e.role(playwright.AriaRoleLink, name).Click()) }

func (e *ui) expectLayout(selectors ...string) {
	e.t.Helper()
	e.eval("() => document.fonts.ready")
	for _, selector := range selectors {
		locator := e.page.Locator(selector)
		e.must(e.expect.Locator(locator).ToBeVisible())
		box, err := locator.BoundingBox()
		e.must(err)
		require.NotNil(e.t, box, "%s has a layout box", selector)
		require.Greater(e.t, box.Width, float64(0), selector)
		require.GreaterOrEqual(e.t, box.X, float64(-1), selector)
		require.LessOrEqual(e.t, box.X+box.Width, float64(e.screen.width+1), selector)
	}
	require.LessOrEqual(e.t, e.eval("() => document.documentElement.scrollWidth - innerWidth").(int), 1, "no horizontal overflow")
}

func (e *ui) publishNote() (string, string) {
	e.t.Helper()
	e.gotoPage("/")
	e.fill("#titleInput", "UI test note")
	e.fill("#editor", "# Hello\n\nThis is **Markdown**.")
	e.button("Publish")
	e.must(e.expect.Page(e.page).ToHaveURL(regexpURL("/success?")))
	e.must(e.expect.Locator(e.page.Locator("#editCodeValue")).ToHaveText(nonempty))
	code, err := e.page.Locator("#editCodeValue").InnerText()
	e.must(err)
	link, err := e.role(playwright.AriaRoleLink, "Go to paste").GetAttribute("href")
	e.must(err)
	require.NotEmpty(e.t, link)
	return strings.TrimSpace(code), link
}

func (e *ui) admin() string {
	e.t.Helper()
	response, err := e.browserContext.Request().Post("/setup", playwright.APIRequestContextPostOptions{
		Headers: map[string]string{"Accept": "application/json"},
		Data:    map[string]string{"username": "ui-admin", "password": password, "bootstrap_token": bootstrapToken},
	})
	e.must(err)
	require.Equal(e.t, http.StatusCreated, response.Status())
	var body struct {
		URI string `json:"otp_auth_uri"`
	}
	e.must(response.JSON(&body))
	enrollment, err := url.Parse(body.URI)
	e.must(err)
	secret := enrollment.Query().Get("secret")
	require.NotEmpty(e.t, secret)
	e.gotoPage("/login?next=/admin")
	e.fill("#username", "ui-admin")
	e.fill("#password", password)
	e.fill("#code", e.code(secret, time.Now()))
	e.button("Sign in")
	e.must(e.expect.Page(e.page).ToHaveURL(e.baseURL + "/admin"))
	return secret
}

func (e *ui) code(secret string, at time.Time) string {
	e.t.Helper()
	code, err := totp.GenerateCode(secret, at)
	e.must(err)
	return code
}
