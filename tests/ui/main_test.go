package ui_test

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

var (
	//nolint:gochecknoglobals // go test needs a package-level flag, which TestMain reads
	withBrowser = flag.Bool("headed", false, "show the Chromium window")
	//nolint:gochecknoglobals // go test needs a package-level flag, which scenarios read
	updateImages = flag.Bool("update", false, "update screenshot references")
	//nolint:gochecknoglobals // TestMain fills the shared browser state once for all scenarios
	runner testRunner
)

type testRunner struct {
	playwright *playwright.Playwright
	browser    playwright.Browser
	binary     string
	report     report
}

func TestMain(m *testing.M) {
	if os.Getenv(supervisorEnv) != "" {
		os.Exit(superviseServer())
	}

	os.Exit(runTests(m))
}

// checkFlags refuses -update with -headed: the references come from headless Chromium, and a
// headed capture would replace them with images the regular run can never match.
func checkFlags() error {
	if *withBrowser && *updateImages {
		return errors.New("-update needs a headless run: drop -headed to update screenshot references")
	}

	return nil
}

func runTests(m *testing.M) int {
	flag.Parse()

	if flag.Lookup("test.list").Value.String() != "" {
		return m.Run()
	}

	err := checkFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	err = setCacheEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	runner.binary = os.Getenv("PADMARK_UI_SERVER_BINARY")
	if runner.binary == "" {
		directory, buildErr := buildServer()
		if buildErr != nil {
			fmt.Fprintln(os.Stderr, buildErr)

			return 1
		}

		defer os.RemoveAll(directory)
	}

	stopBrowser, err := startBrowser()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}
	defer stopBrowser()

	err = resetArtifacts()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	code := m.Run()

	err = runner.report.write()
	if err != nil {
		fmt.Fprintln(os.Stderr, "write browser report:", err)

		return 1
	}

	return code
}

// resetArtifacts removes the artifacts of the previous run and makes an empty directory.
func resetArtifacts() error {
	err := os.RemoveAll("artifacts")
	if err != nil {
		return err
	}

	return os.MkdirAll("artifacts", 0o700)
}

// setCacheEnv creates the local cache directory and points Playwright at it unless the caller
// already set the paths.
func setCacheEnv() error {
	err := os.MkdirAll(".cache", 0o700)
	if err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	for name, value := range map[string]string{
		"PLAYWRIGHT_DRIVER_PATH":   filepath.Join(".cache", "driver"),
		"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(".cache", "browsers"),
	} {
		if os.Getenv(name) != "" {
			continue
		}

		absolute, err := filepath.Abs(value)
		if err != nil {
			return err
		}

		err = os.Setenv(name, absolute)
		if err != nil {
			return err
		}
	}

	return nil
}

// buildServer builds the server binary into runner.binary and returns the temporary build directory.
// The caller removes that directory.
func buildServer() (string, error) {
	directory, err := os.MkdirTemp("", "padmark-ui-build-")
	if err != nil {
		return "", err
	}

	runner.binary, err = filepath.Abs(filepath.Join(directory, "server"))
	if err != nil {
		return directory, err
	}
	// CI runs as root in a container on a checkout owned by the runner user, where git refuses
	// the repository and VCS stamping fails the build; the test server needs no build info.
	// The Makefile exports GO, so `make test-ui GO=go1.27.3` builds with the same toolchain.
	goTool := cmp.Or(os.Getenv("GO"), "go")
	//nolint:gosec // G204: the Go tool path comes from the developer's own environment
	command := exec.Command(goTool, "build", "-buildvcs=false", "-o", runner.binary, "./cmd/padmark-server")

	command.Dir, command.Stdout, command.Stderr = "../..", os.Stdout, os.Stderr

	err = command.Run()
	if err != nil {
		return directory, fmt.Errorf("build UI server: %w", err)
	}

	return directory, nil
}

// startBrowser starts Playwright and Chromium. The returned function closes the browser, then stops Playwright.
func startBrowser() (func(), error) {
	var err error

	runner.playwright, err = playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("run make test-ui-setup first: %w", err)
	}

	runner.browser, err = runner.playwright.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: new(!*withBrowser),
	})
	if err != nil {
		_ = runner.playwright.Stop()

		return nil, fmt.Errorf("launch Chromium: %w", err)
	}

	return func() {
		_ = runner.browser.Close()
		_ = runner.playwright.Stop()
	}, nil
}
