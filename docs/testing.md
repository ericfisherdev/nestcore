# Testing

Two tiers: the default suite, which is hermetic and needs nothing, and the
database-gated suite, which needs a real Postgres.

## The default suite

```sh
make test        # go test -race -cover ./...
```

No database, no network, no containers. The gated suite skips itself when
`NESTCORE_TEST_DATABASE_URL` is unset, which is what keeps this run
dependency-free.

## The database-gated suite

Set `NESTCORE_TEST_DATABASE_URL` and the gated tests run instead of
skipping:

```sh
docker run -d --rm --name nestcore-test-db \
  -e POSTGRES_PASSWORD=test -e POSTGRES_DB=nestcore_test \
  -p 127.0.0.1:55433:5432 postgres:16-alpine

# docker run -d returns before Postgres accepts connections; bound the wait
# so a broken container fails the setup instead of hanging forever.
ready=false
for _ in $(seq 1 30); do
  if docker exec nestcore-test-db pg_isready -U postgres -d nestcore_test >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [ "$ready" != true ]; then
  echo "nestcore-test-db did not become ready in time" >&2
  docker logs nestcore-test-db
  exit 1
fi

export NESTCORE_TEST_DATABASE_URL="postgres://postgres:test@localhost:55433/nestcore_test?sslmode=disable"
make test-gated
```

`sslmode=disable` is safe here: `applySupabasePooling`'s TLS rejection fires
only under `DB_PROVIDER=supabase`, which gated tests never set.

`make test-gated` names the gated packages explicitly. `go test ./...` with
the variable set works too and runs everything; the explicit target exists
so a gated run is deliberate and its package list is reviewable.

### Prerequisites

- **A Postgres reachable at that DSN, version 16 or 17.** nestcore supports
  both — Nestova and Nestorage need not sit on the same major, and the
  container recipe above deliberately pins the floor (16), not the ceiling,
  so the gated suite exercises the version a consumer is more likely to
  regress against unnoticed. Do not read "matches the deployment target" as
  the rationale for either number; nestcore has no deployment target of its
  own.
- **A database named `test` or ending in `_test`.** Enforced as a safety
  rail: the harness refuses to run otherwise, because it drops and
  recreates schemas. `nestcore_test` is the convention, and at 13 bytes
  leaves ample headroom under the 63-byte derived-name ceiling (see
  Isolation model below).
- **The `CREATEDB` privilege on that role.** The harness creates a database
  per package on demand. A superuser like the container's default
  `postgres` role already has it; a purpose-made role needs it granted:

  ```sql
  ALTER ROLE nestcore_test CREATEDB;
  ```

  Without it, gated tests fail with a `create database` error naming this
  document.

### Isolation model

Every gated package using `dbtest.Harness` gets **its own database**,
derived from the configured one by appending a package suffix —
`nestcore_test` becomes `nestcore_test_dbtest`, and so on for whatever
gated packages this module grows.

That per-package database is what makes a parallel run safe. Go runs
different packages' test binaries concurrently, so a single shared database
would race: one package's schema reset could drop the schema out from under
another package's in-flight test. (`go test -p 1` does not fix it — that
serializes *builds*, not test binaries.)

### Writing a gated test

`db/dbtest` is application-agnostic: it takes the environment variable name
and a `Migrator` (whatever resets and applies the caller's own schema) as
constructor arguments, rather than hard-coding either. nestcore owns no
migrations, so its own gated test constructs a `Harness` with a stub
migrator purely to exercise the pool-creation chain — see
`db/dbtest/dbtest_test.go`'s `TestHarness_NewIsolatedPool_Integration` for
the shape.

A consuming application instead wires one `Harness` against its real
migration runner, typically once, package-level, in its own test helper:

```go
var testHarness = dbtest.New("MYAPP_TEST_DATABASE_URL", myMigrator{})

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testHarness.NewIsolatedPool(t, "tasks")
}
```

- The **suffix must be unique per package** and stable. Two packages
  sharing one would reintroduce exactly the race this removes.
- Need the connection string rather than a pool — a second pool in the same
  test, or a CLI invocation — use `testHarness.DSN(t, "<same-suffix>")`. Do
  not read the environment variable directly: that names the *base*
  database, not the package's, so the two would silently diverge.
- A package whose rows can block a down-migration passes a hook:
  `testHarness.NewIsolatedPool(t, "media", dbtest.WithPreReset(preResetSweep))`.

Derived databases persist between runs; only their schemas are reset (on
both setup and cleanup), so repeat runs are fast. Drop them wholesale by
dropping the container, or, substituting your own base database name if
you're cleaning up a consuming application's derived databases instead of
nestcore's own:

```sql
-- inside psql, connected to the maintenance database. \gexec runs each
-- statement the SELECT generates; without it this only prints them.
SELECT format('DROP DATABASE %I;', datname)
  FROM pg_database
 WHERE datname LIKE 'nestcore\_test\_%' ESCAPE '\'
\gexec
```

### The one exception

`db/db_test.go` reads the variable directly rather than going through
`dbtest`: it only opens a connection and pings it, so it has nothing to
isolate and cannot corrupt another package's fixture.

## No CI database

CI runs `make test` only; the gated suite stays local and unwired from any
service container. That is a deliberate scope boundary, not an oversight —
see the module README for what nestcore's CI does and does not cover.
