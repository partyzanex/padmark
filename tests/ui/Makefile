GO ?= go
ARGS ?=
# main_test.go builds the test server with the same Go command.
export GO

# The nested module is outside the root `./...`, so it gets its own lint target with the
# root linter binary and the root configuration: one standard for the whole repository.
ROOT := $(abspath ../..)
GOLANGCI_LINT := $(ROOT)/bin/golangci-lint

unexport GOROOT
export PLAYWRIGHT_DRIVER_PATH := $(CURDIR)/.cache/driver
PLAYWRIGHT_BROWSERS_PATH ?= $(CURDIR)/.cache/browsers
export PLAYWRIGHT_BROWSERS_PATH

.PHONY: setup test headed debug update report lint
setup:
	$(GO) mod download
	$(GO) run github.com/mxschmitt/playwright-go/cmd/playwright install chromium

test:
	$(GO) test -v -count=1 -parallel=2 -timeout=10m $(ARGS) .

headed:
	$(GO) test -v -count=1 -parallel=2 -timeout=10m $(ARGS) . -args -headed

debug:
	PWDEBUG=1 $(GO) test -v -count=1 -parallel=1 -timeout=0 $(ARGS) . -args -headed

update:
	$(GO) test -v -count=1 -parallel=2 -timeout=10m $(ARGS) . -args -update

report:
	$(GO) run ./cmd/report

lint:
	$(MAKE) -C $(ROOT) golangci-lint-install
	$(GOLANGCI_LINT) run --config $(ROOT)/.golangci.yml $(ARGS) ./...
