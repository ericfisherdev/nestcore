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

`make test` runs the default suite with the race detector — hermetic, no
database required. `db/...` additionally has database-gated tests behind the
`NESTCORE_TEST_DATABASE_URL` environment variable; run `make test-gated` to
include that coverage. See [`docs/testing.md`](docs/testing.md) for the
container recipe, the isolation model, and how a consuming application wires
its own `dbtest.Harness`.

## Configuration

`config` loads and validates runtime configuration from environment
variables, one sub-config at a time (see the package doc for the loader /
validator pattern). Every variable below is read exclusively from the
environment; an optional `.env` file is honored in development only, via
`LoadDotenv`, and never overrides a variable the real environment already
set.

An application composes its own root configuration from these sub-configs
and adds whatever is specific to its own domain — see `config.go`'s
`AppEnv`, `ValidateAppEnv`, and `LoadDotenv` for the pieces a composition
needs beyond the sub-configs themselves.

| Variable | Sub-config | Default |
|---|---|---|
| `APP_ENV` | — | `dev` (`dev`\|`test`\|`prod`) |
| `PORT` | Server | `8080` |
| `TRUSTED_PROXIES` | Server | `127.0.0.0/8,::1/128` |
| `SERVER_REQUEST_TIMEOUT` | Server | `120s` (floor `15s`) |
| `PUBLIC_BASE_URL` | Server | empty (derive from the request) |
| `DATABASE_URL` | DB | none — required, no development fallback |
| `DB_MAX_CONNS` | DB | `0` (let the pool decide; `10` when `DB_PROVIDER=supabase` and unset) |
| `DB_CONNECT_TIMEOUT` | DB | `5s` |
| `DB_PROVIDER` | DB | `postgres` (`postgres`\|`supabase`) |
| `DB_POOL_MODE` | DB | `session` (`session`\|`transaction`) |
| `DB_SSL_ROOT_CERT` | DB | empty |
| `MIGRATE_DATABASE_URL` | DB | empty (reuse `DATABASE_URL`) |
| `SESSION_SECRET` | Session | `config.DevSessionSecret` (dev-only; rejected in prod) |
| `SESSION_LIFETIME` | Session | `12h` |
| `SESSION_COOKIE_SECURE` | Session | `auto` (`auto`\|`true`\|`false`) |
| `ENCRYPTION_KEY` | Crypto | `config.DevEncryptionKey` (dev-only; rejected in prod) |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | TLS | empty — both or neither |
| `HSTS_ENABLED` | HSTS | `false` |
| `HSTS_MAX_AGE` | HSTS | `config.DefaultHSTSMaxAge` (~180d) when enabled and unset |
| `HSTS_INCLUDE_SUBDOMAINS` | HSTS | `false` |
| `HSTS_PRELOAD` | HSTS | `false` (requires includeSubDomains + max-age >= 1y) |
| `S3_ENDPOINT` | S3 | empty (real AWS S3) |
| `S3_REGION` | S3 | empty |
| `S3_BUCKET` | S3 | empty |
| `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` | S3 | empty — both or neither (else the default AWS credential chain) |
| `S3_USE_PATH_STYLE` | S3 | `false` |
| `S3_PRESIGN_TTL` | S3 | `15m` |
| `NOTIFY_SMS_ENABLED` | SMS | `false` |
| `SMS_ORIGINATION_IDENTITY` | SMS | empty — required when enabled |
| `SMS_REGION` | SMS | empty — required when enabled |
| `SMS_ACCESS_KEY_ID` / `SMS_SECRET_ACCESS_KEY` | SMS | empty — both or neither |
| `SMS_RETRY_MAX_ATTEMPTS` | SMS | `3` |
| `NOTIFY_EMAIL_ENABLED` | Email | `false` |
| `SES_FROM_ADDRESS` | Email | empty — required when enabled |
| `SES_REGION` | Email | empty — required when enabled |
| `SES_ACCESS_KEY_ID` / `SES_SECRET_ACCESS_KEY` | Email | empty — both or neither |
| `CACHE_DIR` | Cache | `./.localdata/cache` |

`S3Config.Validate` is caller-gated rather than self-gating on an `Enabled`
field: nestcore has no storage-backend selector of its own, so a consuming
application calls `LoadS3`/`S3Config.Validate` only when its own selector
opted into S3. `SMSConfig` and `EmailConfig` self-gate on their own
`Enabled` field instead, since that field travels with the type.

## License

[AGPL-3.0](LICENSE), matching both consumers (Nestova, Nestorage).
