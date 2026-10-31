GOOSE_VERSION ?= v3.27.0
GOOSE        := $(CURDIR)/bin/goose
DB_SQLITE    ?= ./padmark.db
POSTGRES_DSN := postgres://padmark:padmark@localhost:5432/padmark?sslmode=disable

# Let the selected Go toolchain locate its own standard library.
unexport GOROOT

include go.mk

go.mk:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/go.mk $(CURDIR)/go.mk

GOOSE := $(CURDIR)/bin/goose

$(GOOSE):
	@echo "Installing goose $(GOOSE_VERSION)..."
	GOBIN=$(CURDIR)/bin go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

.PHONY: run
run: build
	set -a && . ./.env && set +a && go run ./cmd/padmark-server

.PHONY: test
test:
	go test -v -count=1 -race -tags=integration ./... -coverprofile=cover.out

UI_ARGS ?=

.PHONY: test-ui-setup test-ui test-ui-headed test-ui-debug test-ui-update test-ui-report test-ui-lint
test-ui-setup:
	$(MAKE) -C tests/ui setup

test-ui:
	$(MAKE) -C tests/ui test ARGS="$(UI_ARGS)"

test-ui-headed:
	$(MAKE) -C tests/ui headed ARGS="$(UI_ARGS)"

test-ui-debug:
	$(MAKE) -C tests/ui debug ARGS="$(UI_ARGS)"

test-ui-update:
	$(MAKE) -C tests/ui update ARGS="$(UI_ARGS)"

test-ui-report:
	$(MAKE) -C tests/ui report

test-ui-lint:
	$(MAKE) -C tests/ui lint ARGS="$(UI_ARGS)"

.PHONY: cover
cover: test
	go tool cover -html cover.out

DOCKER_IMAGE ?= partyzanex/padmark
DOCKER_TAG   ?= latest

.PHONY: docker-build
docker-build:
	DOCKER_BUILDKIT=1 docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

.PHONY: migrate-sqlite-up
migrate-sqlite-up: $(GOOSE)
	$(GOOSE) -dir migrations/sqlite sqlite3 $(DB_SQLITE) up

.PHONY: migrate-sqlite-down
migrate-sqlite-down: $(GOOSE)
	$(GOOSE) -dir migrations/sqlite sqlite3 $(DB_SQLITE) down

.PHONY: migrate-sqlite-status
migrate-sqlite-status: $(GOOSE)
	$(GOOSE) -dir migrations/sqlite sqlite3 $(DB_SQLITE) status

migrate-postgres-up: $(GOOSE)
	$(GOOSE) -dir migrations/postgres postgres "$(POSTGRES_DSN)" up
