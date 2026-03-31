include go.mk

GOOSE_VERSION ?= v3.27.0
GOOSE        := $(CURDIR)/bin/goose
DB_SQLITE    ?= ./padmark.db

go.mk:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/go.mk $(CURDIR)/go.mk

$(GOOSE):
	@echo "Installing goose $(GOOSE_VERSION)..."
	GOBIN=$(CURDIR)/bin go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

.PHONY: run
run: build
	set -a && . ./.env && set +a && go run ./cmd/padmark-server

.PHONY: test
test:
	go test -v -count=1 -race ./... -coverprofile=cover.out

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
