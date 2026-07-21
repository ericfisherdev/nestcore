# nestcore

Shared infrastructure module for the Nest household-appliance apps
(Nestova, Nestorage): configuration, database access, HTTP serving,
rendering, caching, crypto, metrics — platform concerns only. **No domain
code lives here** — anything that references a consumer's domain model
(household, item, bin, etc.) belongs in that app's own repo, not nestcore.

The package layout is flat and non-internal (`config/`, `db/`,
`httpserver/`, `render/`, `qrcode/`, ...) rather than nested under
`internal/`. Go forbids importing another module's `internal/` packages, so
anything nestcore needs to expose to Nestova or Nestorage has to live outside
`internal/` from the start.

## Development

### Prerequisites

- Go (see the `go` directive in [`go.mod`](go.mod))
- [golangci-lint](https://golangci-lint.run) **v2.11.4** (see
  [Linting](#linting-golangci-lint))

Everything else (lefthook, conform) is pinned in `go.mod` via Go tool
directives, so no global install is needed — invoke it with `go tool <name>`.
golangci-lint is the exception: its maintainers advise against `go install`,
so it is installed as a pinned binary instead.

### Common tasks

```sh
make build      # type-check the module (a library emits no binary artifact)
make test       # run tests with the race detector + coverage profile
make cover      # print a per-function coverage summary
make lint       # run static analysis (golangci-lint)
make fmt        # format Go sources (gofumpt + goimports via golangci-lint)
make hooks      # install the Lefthook Git hooks
make tidy       # prune and verify module dependencies
make help       # list all targets
```

### Linting (golangci-lint)

Install the pinned `v2.11.4` release from the
[golangci-lint releases page](https://github.com/golangci/golangci-lint/releases)
rather than `go install` (the project's own recommendation — it risks
building against an untested Go version or dependency set). CI installs the
same version via `golangci/golangci-lint-action`; keep both in sync with
`GOLANGCI_LINT_VERSION` in the [`Makefile`](Makefile).

### Testing

`make test` runs the full suite with the race detector. Some packages (added
starting with the database extraction) additionally need a real Postgres
instance and are gated behind the `NESTCORE_TEST_DATABASE_URL` environment
variable — reserved here, wired up when that package lands.

## License

[AGPL-3.0](LICENSE), matching both consumers (Nestova, Nestorage).
