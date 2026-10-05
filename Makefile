# Build herdr-kata with a version it can report.
#
# This is a convenience, never a dependency: the Herdr plugin and anyone
# installing herdr-kata build with plain `go build`, so the Go toolchain is the only
# requirement. Nothing here may become necessary to build or run herdr-kata.
#
# Go already stamps the git revision into a plain `go build`, so this exists for
# the two cases it cannot cover:
#
#   1. a released version — a tag says more than a hash
#   2. a build from a git worktree — Go skips VCS stamping there, because the
#      worktree's .git is a file rather than a repository, so an unaided build
#      reports "dev"
#
# Both are handled by asking git what this commit is called and passing the
# answer through -ldflags.

BIN     := bin/herdr-kata
PKG     := ./cmd/herdr-kata
VERSION_PKG := github.com/salmonumbrella/herdr-kata/internal/version

# `git describe` gives the most specific name this commit has:
#   v1.2.3            exactly a tag
#   v1.2.3-4-gabc1234 four commits past v1.2.3
#   abc1234           no tags in the repo yet
# --dirty appends -dirty when the tree has uncommitted changes, so a build is
# never mistaken for the commit it was merely started from.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null)

ifeq ($(VERSION),)
LDFLAGS :=
else
LDFLAGS := -ldflags "-X $(VERSION_PKG).Tag=$(VERSION)"
endif

.PHONY: build build-all check ci sec test test-race vet fmt fmt-check cross-build native-smoke version clean install-plugin

## build: compile the binary with its version stamped in
build:
	go build $(LDFLAGS) -o $(BIN) $(PKG)

## check: everything that must pass before a PR is merged
check: fmt-check build-all vet test

## ci: the same formatting/build/vet/race/cross-build checks as GitHub Actions
ci: fmt-check build-all vet test-race cross-build

build-all:
	go build ./...

fmt-check:
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }

test-race:
	go test ./... -race -count=1 -cover -timeout=20m

## cross-build: compile supported targets; this is not a platform runtime test
cross-build:
	@build_dir=$$(mktemp -d); trap 'rm -rf "$$build_dir"' EXIT; \
	for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
	  echo "compile $$target"; \
	  GOOS=$${target%/*} GOARCH=$${target#*/} CGO_ENABLED=0 go build -o "$$build_dir/herdr-kata-$${target%/*}-$${target#*/}" $(PKG) || exit $$?; \
	done

## native-smoke: explicitly supplied branch Kata and real Herdr, isolated state
native-smoke:
	@test -n "$$KATA_NATIVE_TEST_BINARY" -a -n "$$HERDR_NATIVE_TEST_BINARY" || { echo 'Set KATA_NATIVE_TEST_BINARY and HERDR_NATIVE_TEST_BINARY to explicit binaries'; exit 1; }
	go test ./cmd/herdr-kata -run '^TestRealTask10' -count=1 -v -timeout=3m

test:
	go test ./... -count=1 -cover -timeout=20m

vet:
	go vet ./...

## sec: the two security scans CI runs, with the same rules and exclusions
##      (see .github/workflows/security.yml for why each rule is left out)
sec:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go run github.com/securego/gosec/v2/cmd/gosec@latest \
		-exclude=G104,G202,G203,G204,G304,G702,G703 \
		-severity=medium -confidence=medium \
		-exclude-generated -quiet ./...

fmt:
	gofmt -w .

## version: show what a build would stamp, without building
version:
	@echo "$(if $(VERSION),$(VERSION),unstamped — go build will use the embedded revision)"

clean:
	rm -f $(BIN)

## install-plugin: rebuild and re-register the Herdr plugin from this checkout
install-plugin: build
	herdr plugin unlink salmonumbrella.herdr-kata 2>/dev/null || true
	herdr plugin link $(CURDIR)
