.PHONY: build test lint check test-live docs-check install-hooks
build:
	go build -o bin/herdr-kata ./cmd/herdr-kata
test:
	go test -race -shuffle=on -timeout 30m ./...
.cache/golangci-lint: .custom-gcl.yml
	mkdir -p .cache
	golangci-lint custom -v
	mv custom-gcl .cache/golangci-lint
lint: .cache/golangci-lint
	.cache/golangci-lint run
check: lint test
test-live:
	go test -tags live -timeout 30m ./internal/live/...
docs-check:
	git diff --check
install-hooks:
	@echo 'No repository-installed hooks; use the host-managed hooks.'
