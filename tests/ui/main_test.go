package ui_test

import (
	"cmp"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

var (
	withBrowser  = flag.Bool("headed", false, "show the Chromium window")
	updateImages = flag.Bool("update", false, "update screenshot references")
	runner       testRunner
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

func runTests(m *testing.M) int {
	flag.Parse()
	if flag.Lookup("test.list").Value.String() != "" {
		return m.Run()
	}
	if err := os.MkdirAll(".cache", 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for name, value := range map[string]string{
		"PLAYWRIGHT_DRIVER_PATH":   filepath.Join(".cache", "driver"),
		"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(".cache", "browsers"),
	} {
		if os.Getenv(name) == "" {
			absolute, err := filepath.Abs(value)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			if err = os.Setenv(name, absolute); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
	}
	runner.binary = os.Getenv("PADMARK_UI_SERVER_BINARY")
	if runner.binary == "" {
		directory, err := os.MkdirTemp("", "padmark-ui-build-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(directory)
		runner.binary, err = filepath.Abs(filepath.Join(directory, "server"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		// CI runs as root in a container on a checkout owned by the runner user, where git refuses
		// the repository and VCS stamping fails the build; the test server needs no build info.
		// The Makefile exports GO, so `make test-ui GO=go1.27.3` builds with the same toolchain.
		goTool := cmp.Or(os.Getenv("GO"), "go")
		command := exec.Command(goTool, "build", "-buildvcs=false", "-o", runner.binary, "./cmd/padmark-server")
		command.Dir, command.Stdout, command.Stderr = "../..", os.Stdout, os.Stderr
		if err = command.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build UI server:", err)
			return 1
		}
	}
	var err error
	runner.playwright, err = playwright.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "run make test-ui-setup first:", err)
		return 1
	}
	defer runner.playwright.Stop()
	runner.browser, err = runner.playwright.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(!*withBrowser),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch Chromium:", err)
		return 1
	}
	defer runner.browser.Close()
	if err = os.RemoveAll("artifacts"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err = os.MkdirAll("artifacts", 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code := m.Run()
	if err = runner.report.write(); err != nil {
		fmt.Fprintln(os.Stderr, "write browser report:", err)
		return 1
	}
	return code
}
