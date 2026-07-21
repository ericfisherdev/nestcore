# nestcore developer task runner.
#
# Targets are the single source of truth for build/quality commands so the
# Git hooks and CI invoke `make <target>` rather than duplicating tool
# invocations.

# Pinned golangci-lint version (single source of truth shared with CI, which
# installs this exact version via golangci/golangci-lint-action). Keep in sync
# with the version documented in the README.
GOLANGCI_LINT_VERSION := v2.11.4

.DEFAULT_GOAL := build

# Coverage profile written by `make test` and read by `make cover`.
COVERAGE_OUT := coverage.out

.PHONY: all build test cover lint fmt hooks hooks-uninstall tidy clean help

## all: default aggregate target (alias for build)
all: build

## build: type-check the module (a library emits no binary artifact)
build:
	go build ./...

## test: run the test suite with the race detector and write a coverage profile
test:
	go test -race -cover -coverprofile=$(COVERAGE_OUT) ./...

## cover: print a per-function coverage summary (runs test first)
cover: test
	go tool cover -func=$(COVERAGE_OUT)

## lint: run static analysis (golangci-lint, config in .golangci.yml)
lint:
	golangci-lint run

## fmt: format Go sources (golangci-lint runs gofumpt + goimports)
fmt:
	golangci-lint fmt

## hooks: install the Lefthook Git hooks (pre-commit, pre-push)
hooks:
	go tool lefthook install

## hooks-uninstall: remove the Lefthook Git hooks
hooks-uninstall:
	go tool lefthook uninstall

## tidy: prune and verify module dependencies
tidy:
	go mod tidy

## clean: remove build artifacts
clean:
	rm -f $(COVERAGE_OUT)

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
