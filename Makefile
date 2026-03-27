include go.mk

go.mk:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/go.mk $(CURDIR)/go.mk

.PHONY: test
test:
	go test -v -count=1 -race ./... -coverprofile=cover.out

.PHONE: cover
cover: test
	go tool cover -html cover.out
