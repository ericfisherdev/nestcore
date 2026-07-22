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

# Coverage profile written by `make test-gated`. It is a SEPARATE file because
# the two runs cover the same packages to very different depths: without a
# database the gated tests self-skip, so COVERAGE_OUT reports db/... as barely
# covered. Both profiles are handed to Sonar, which unions them; keeping them
# apart is what stops the skipped run from masking the real one. The
# "coverage.*" name matters too — .gitignore already ignores that prefix.
GATED_COVERAGE_OUT := coverage.gated.out

# Packages containing database-gated tests. `make test` already runs these
# packages too — their DB-dependent tests self-skip without the env var —
# test-gated just runs them explicitly, with the env var required.
GATED_TEST_PACKAGES := \
	./db/...

.PHONY: all build test test-gated cover lint fmt hooks hooks-uninstall tidy clean help

## all: default aggregate target (alias for build)
all: build

## build: type-check the module (a library emits no binary artifact)
build:
	go build ./...

## test: run the test suite with the race detector and write a coverage profile
test:
	go test -race -cover -coverprofile=$(COVERAGE_OUT) ./...

## test-gated: run the database-gated suites (needs NESTCORE_TEST_DATABASE_URL)
test-gated:
	@test -n "$(NESTCORE_TEST_DATABASE_URL)" || \
		{ echo "NESTCORE_TEST_DATABASE_URL is not set; see docs/testing.md"; exit 1; }
	# -v: gated tests are the only automated check on the db pool and the
	# migration runner, so their names must show up in a CI log as PASS/SKIP
	# per test, not just one "ok" line per package that can't tell "ran and
	# passed" apart from "every test skipped itself".
	go test -race -v -count=1 -coverprofile=$(GATED_COVERAGE_OUT) $(GATED_TEST_PACKAGES)

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
	rm -f $(COVERAGE_OUT) $(GATED_COVERAGE_OUT)

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
