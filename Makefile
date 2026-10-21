.PHONY: build check lint fmt test test-crash test-kata test-kata-children test-kata-replies test-herdr install-hooks clean

# Go applies this timeout to each package, not the whole suite. Durable-state
# and crash tests can exceed the default 10m on a loaded development host.
GOFLAGS_TEST := -race -shuffle=on -timeout=30m
LINT_TAGS := crashpoint,kata,kata_children,kata_replies,herdr,herdr_live
# .custom-gcl.yml pins the host linter and its kit plugin together.
GCL_VERSION := $(shell sed -n 's/^version: //p' .custom-gcl.yml)
GCL_DIR ?= $(CURDIR)/.cache/golangci-lint
GCL := $(GCL_DIR)/custom-gcl

# Existing test-timing findings remain a backlog; reject new findings.
ifeq ($(GITHUB_EVENT_NAME),push)
LINT_NEW_FLAGS ?= --new-from-rev=HEAD^
else
LINT_NEW_FLAGS ?= --new-from-merge-base=origin/main
endif

# Mirrors the build step in herdr-plugin.toml, which stays the install contract.
build:
	go build -trimpath -o ./bin/herdr-kata ./cmd/herdr-kata

check: lint test

# Build for the host even when linting another platform's source files.
$(GCL): .custom-gcl.yml go.mod
	env -u GOOS -u GOARCH golangci-lint custom --destination "$(GCL_DIR)" --name custom-gcl --version $(GCL_VERSION)

lint: $(GCL)
	@if [ "$${GITHUB_ACTIONS:-}" = true ] && [ "$$(git rev-parse --is-shallow-repository)" = true ]; then \
		git fetch --no-tags --unshallow origin main:refs/remotes/origin/main; \
	fi
	GOLANGCI_LINT_CACHE="$(GCL_DIR)" "$(GCL)" run $(LINT_NEW_FLAGS) ./...
	GOLANGCI_LINT_CACHE="$(GCL_DIR)" "$(GCL)" run $(LINT_NEW_FLAGS) --build-tags $(LINT_TAGS) ./...
	actionlint

fmt: $(GCL)
	"$(GCL)" fmt ./...

test:
	go test $(GOFLAGS_TEST) ./...

test-crash:
	go test $(GOFLAGS_TEST) -tags crashpoint ./...

test-kata:
	go test $(GOFLAGS_TEST) -tags kata ./...

test-kata-children:
	go test $(GOFLAGS_TEST) -tags kata_children ./...

test-kata-replies:
	go test $(GOFLAGS_TEST) -tags kata_replies ./...

test-herdr:
	go test $(GOFLAGS_TEST) -tags herdr ./internal/herdr

install-hooks:
	prek install

clean:
	rm -rf bin .cache/golangci-lint docs/site

.PHONY: docs-install docs-build docs-serve docs-check

docs-install:
	cd docs && uv sync --frozen --no-dev

docs-build:
	cd docs && uv run --frozen bash ./zensical-docs.sh build

docs-serve:
	cd docs && uv run --frozen bash ./zensical-docs.sh serve

docs-check:
	cd docs && uv run --frozen python -B -m unittest discover -s scripts -p 'test_*.py'
	cd docs && uv run --frozen python scripts/check_frontmatter.py
	cd docs && uv run --frozen python scripts/check_nav.py
	cd docs && uv run --frozen python scripts/llms_txt.py
	$(MAKE) docs-build
	cd docs && uv run --frozen python scripts/check_source_links.py
