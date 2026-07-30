package migrate

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed testdata/migrations/*.sql
var fixtureFS embed.FS

const fixtureDir = "testdata/migrations"

// TestNew verifies the fail-fast validation New performs before any Runner
// method needs a database: a real migration set is accepted and every .sql
// file found, a missing directory is rejected, and a directory with no .sql
// files is rejected — the property TestEmbeddedMigrations used to guarantee
// only for Nestova's own embed, now enforced for every caller.
func TestNew(t *testing.T) {
	t.Run("valid FS and dir finds every .sql migration", func(t *testing.T) {
		r, err := New(fixtureFS, fixtureDir)
		if err != nil {
			t.Fatalf("New() error: %v", err)
		}
		matches, err := fs.Glob(r.migrations, "*.sql")
		if err != nil {
			t.Fatalf("glob the runner's filesystem: %v", err)
		}
		if len(matches) != 3 {
			t.Errorf("found %d migrations, want 3 (every fixture .sql file)", len(matches))
		}
	})

	t.Run("nonexistent dir errors", func(t *testing.T) {
		if _, err := New(fixtureFS, "testdata/does-not-exist"); err == nil {
			t.Error("New() = nil error, want error for a nonexistent directory")
		}
	})

	t.Run("dir with no .sql files errors", func(t *testing.T) {
		if _, err := New(fixtureFS, "testdata"); err == nil {
			t.Error("New() = nil error, want error for a directory with no .sql migrations directly in it")
		}
	})
}

// TestNew_ConstructionOptions verifies WithVersionTable, WithEnsureSchema,
// and WithSessionLock each set their corresponding Runner field, and that a
// Runner built with none of them (every existing caller's call pattern)
// leaves all three at their zero value. Table-driven since all four cases
// share the same shape: apply some options, assert the three fields.
func TestNew_ConstructionOptions(t *testing.T) {
	tests := []struct {
		name        string
		opts        []NewOption
		wantVersion string
		wantSchema  string
		wantSession bool
	}{
		{name: "no options leaves every field at its zero value"},
		{
			name:        "WithVersionTable sets versionTable",
			opts:        []NewOption{WithVersionTable("identity.goose_db_version")},
			wantVersion: "identity.goose_db_version",
		},
		{
			name:       "WithEnsureSchema sets ensureSchema",
			opts:       []NewOption{WithEnsureSchema("identity")},
			wantSchema: "identity",
		},
		{
			name:        "WithSessionLock sets sessionLock",
			opts:        []NewOption{WithSessionLock()},
			wantSession: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := New(fixtureFS, fixtureDir, tt.opts...)
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			if r.versionTable != tt.wantVersion {
				t.Errorf("versionTable = %q, want %q", r.versionTable, tt.wantVersion)
			}
			if r.ensureSchema != tt.wantSchema {
				t.Errorf("ensureSchema = %q, want %q", r.ensureSchema, tt.wantSchema)
			}
			if r.sessionLock != tt.wantSession {
				t.Errorf("sessionLock = %v, want %v", r.sessionLock, tt.wantSession)
			}
		})
	}
}

// TestEnsureSchema_PropagatesExecError proves ensureSchema wraps and returns
// db.ExecContext's error rather than swallowing it. Deterministic and
// hermetic: sql.Open never dials (connections are lazy), so closing the
// handle immediately after is what makes the following ExecContext fail —
// no real database is needed to exercise this path.
func TestEnsureSchema_PropagatesExecError(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1/db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if err := ensureSchema(context.Background(), db, "identity"); err == nil {
		t.Error("ensureSchema on a closed db = nil error, want error")
	}
}

// TestPoolerSafeConnConfig verifies the pooler-safe path selects the simple
// query protocol (no named prepared statements) without needing a database.
func TestPoolerSafeConnConfig(t *testing.T) {
	t.Run("selects the simple protocol", func(t *testing.T) {
		cfg, err := poolerSafeConnConfig("postgres://u:p@pooler.supabase.com:6543/postgres?sslmode=require")
		if err != nil {
			t.Fatalf("poolerSafeConnConfig() error: %v", err)
		}
		if cfg.DefaultQueryExecMode != pgx.QueryExecModeSimpleProtocol {
			t.Errorf("DefaultQueryExecMode = %v, want QueryExecModeSimpleProtocol", cfg.DefaultQueryExecMode)
		}
	})

	t.Run("invalid DSN returns an error", func(t *testing.T) {
		if _, err := poolerSafeConnConfig("://nope"); err == nil {
			t.Error("poolerSafeConnConfig() = nil error, want error for invalid DSN")
		}
	})
}

// TestWriteStatus is a unit test — no database — proving the rendered table
// matches the legacy goose dispatcher's format byte-for-byte, now written to
// a caller-supplied writer instead of goose's package logger.
func TestWriteStatus(t *testing.T) {
	applied := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	statuses := []MigrationStatus{
		{Version: 1, Source: "00001_widget.sql", Applied: true, AppliedAt: applied},
		{Version: 2, Source: "00002_widget_color.sql", Applied: false},
	}

	var buf bytes.Buffer
	if err := WriteStatus(&buf, statuses); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}

	want := "    Applied At                  Migration\n" +
		"    =======================================\n" +
		fmt.Sprintf("    %-24s -- %v\n", applied.Format(time.ANSIC), "00001_widget.sql") +
		fmt.Sprintf("    %-24s -- %v\n", "Pending", "00002_widget_color.sql")
	if got := buf.String(); got != want {
		t.Errorf("WriteStatus output =\n%q\nwant\n%q", got, want)
	}
}

// newFixtureRunner returns a Runner over this package's own three-migration
// fixture set — never a product schema, per the gated tests below.
func newFixtureRunner(t *testing.T) *Runner {
	t.Helper()
	r, err := New(fixtureFS, fixtureDir)
	if err != nil {
		t.Fatalf("New(fixture): %v", err)
	}
	return r
}

// isolatedDSN derives this package's own gated test database, mirroring
// nestcore/db/dbtest.Harness without depending on it: dbtest is BUILT ON
// this package (a caller wires a *Runner in as its Migrator), so importing
// it here would be an import cycle — and these tests exercise the very
// primitives dbtest depends on, so they must not be layered over it. The
// duplicated logic is deliberately minimal: safety rail, CREATE DATABASE,
// rewritten DSN. Schema reset/teardown stays in each test, which is the
// point of this package's tests.
func isolatedDSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv("NESTCORE_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set NESTCORE_TEST_DATABASE_URL to run the gated migrate tests")
	}
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatalf("parse NESTCORE_TEST_DATABASE_URL: %v", err)
	}
	name := strings.ToLower(cfg.Database)
	if name != "test" && !strings.HasSuffix(name, "_test") {
		t.Fatalf("refusing to use database %q; name must be \"test\" or end with \"_test\"", name)
	}
	derived := name + "_migrate"

	adminCfg := cfg.Copy()
	adminCfg.Database = "postgres"
	// Bounded: CREATE DATABASE takes an exclusive lock on the template
	// database and can block on another session, which would otherwise hang
	// until the whole `go test` timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatalf("connect to maintenance database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{derived}.Sanitize()); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42P04" {
			t.Fatalf("create database %q (the test role needs CREATEDB; see docs/testing.md): %v", derived, err)
		}
	}

	// Swap only the database name on the ORIGINAL DSN — re-rendering from
	// the parsed config would drop options pgx folds into the connection
	// (sslrootcert, connect_timeout, ...) and force re-escaping values such
	// as a password containing spaces.
	if u, err := url.Parse(base); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		u.Path = "/" + derived
		return u.String()
	}
	// Conninfo form: splice over just the dbname value. Quote-aware for the
	// same reason as dbtest's scanner — a whitespace split would collapse
	// spaces inside a quoted password.
	if start, end, ok := dbnameValueSpan(base); ok {
		return base[:start] + derived + base[end:]
	}
	t.Fatalf("cannot derive a database name from NESTCORE_TEST_DATABASE_URL: no dbname= key and not a postgres:// URL")
	return ""
}

// dbnameValueSpan locates the dbname value in a libpq conninfo string,
// returning its half-open byte range; when dbname repeats, the LAST
// occurrence wins, matching libpq. Mirrors dbtest's scanner of the same
// name (see isolatedDSN for why this package cannot import dbtest), split
// into the same key/quoted-value/bare-value helpers dbtest uses, both to
// keep this function's cognitive complexity low and to keep the two
// scanners easy to compare when either changes.
func dbnameValueSpan(conninfo string) (start, end int, ok bool) {
	i := 0
	for i < len(conninfo) {
		i = skipConninfoSpace(conninfo, i)
		if i >= len(conninfo) {
			break
		}
		key, next := scanConninfoKey(conninfo, i)
		i = next
		if i >= len(conninfo) || conninfo[i] != '=' {
			continue // malformed fragment; let pgx report it
		}
		i++ // consume '='
		valStart := i
		i = scanConninfoValue(conninfo, i)
		if key == "dbname" {
			start, end, ok = valStart, i, true // keep scanning: last wins
		}
	}
	return start, end, ok
}

// skipConninfoSpace returns the index of the first non-whitespace byte in
// conninfo at or after i, skipping the separator between key=value pairs.
func skipConninfoSpace(conninfo string, i int) int {
	for i < len(conninfo) && isConninfoSpace(conninfo[i]) {
		i++
	}
	return i
}

// scanConninfoKey scans a bare key token starting at i, which must not be
// whitespace, stopping at the next '=' or whitespace. It returns the key
// text and the index immediately after it.
func scanConninfoKey(conninfo string, i int) (key string, next int) {
	keyStart := i
	for i < len(conninfo) && conninfo[i] != '=' && !isConninfoSpace(conninfo[i]) {
		i++
	}
	return conninfo[keyStart:i], i
}

// scanConninfoValue scans a value starting at i, immediately after the '=',
// dispatching to the quoted or bare scanner depending on the value's first
// byte. It returns the index immediately after the value.
func scanConninfoValue(conninfo string, i int) (end int) {
	if i < len(conninfo) && conninfo[i] == '\'' {
		return scanQuotedConninfoValue(conninfo, i)
	}
	return scanBareConninfoValue(conninfo, i)
}

// scanQuotedConninfoValue scans a single-quoted value starting at the
// opening quote, honoring backslash escapes, and returns the index
// immediately after the closing quote (or the end of the string, if the
// quote is never closed).
func scanQuotedConninfoValue(conninfo string, i int) int {
	i++ // opening quote
	for i < len(conninfo) {
		if conninfo[i] == '\\' && i+1 < len(conninfo) {
			i += 2
			continue
		}
		if conninfo[i] == '\'' {
			i++ // closing quote
			break
		}
		i++
	}
	return i
}

// scanBareConninfoValue scans an unquoted value starting at i, stopping at
// the next whitespace byte, and returns the index immediately after it.
func scanBareConninfoValue(conninfo string, i int) int {
	for i < len(conninfo) && !isConninfoSpace(conninfo[i]) {
		if conninfo[i] == '\\' && i+1 < len(conninfo) {
			i++
		}
		i++
	}
	return i
}

func isConninfoSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// The gated tests below deliberately use context.Background(), not
// t.Context(): t.Context() is canceled just before Cleanup-registered
// functions run, and every one of these tests registers a cleanup that
// itself calls r.Reset against ctx. A canceled ctx there does not fail the
// test — Reset returns an error that only reaches t.Logf — it just makes
// the cleanup Reset silently do nothing, discovered by running these tests
// against a real Postgres for the first time (see NSTR-6's CI wiring).

// TestSchemaExists_AbsentSchemaReportsFalse proves SchemaExists reports false
// — and creates nothing — for a schema that has never been created, unlike
// every other operation in this package (which would create it via
// WithEnsureSchema or Up).
func TestSchemaExists_AbsentSchemaReportsFalse(t *testing.T) {
	dsn := isolatedDSN(t)
	const schema = "migrate_probe_absent_test"
	ctx := context.Background()

	db := openTestDB(t, dsn)
	if _, err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
		t.Fatalf("ensure schema %q absent: %v", schema, err)
	}

	exists, err := SchemaExists(ctx, dsn, schema)
	if err != nil {
		t.Fatalf("SchemaExists: %v", err)
	}
	if exists {
		t.Errorf("SchemaExists(%q) = true, want false (never created)", schema)
	}
	if schemaExists(t, dsn, schema) {
		t.Errorf("SchemaExists as a side effect created %q", schema)
	}
}

// TestSchemaExists_PresentSchemaReportsTrue proves SchemaExists reports true
// for a schema created independently of this package's own machinery.
func TestSchemaExists_PresentSchemaReportsTrue(t *testing.T) {
	dsn := isolatedDSN(t)
	const schema = "migrate_probe_present_test"
	ctx := context.Background()

	db := openTestDB(t, dsn)
	if _, err := db.Exec("CREATE SCHEMA IF NOT EXISTS " + schema); err != nil {
		t.Fatalf("create schema %q: %v", schema, err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Logf("cleanup drop schema failed: %v", err)
		}
	})

	exists, err := SchemaExists(ctx, dsn, schema)
	if err != nil {
		t.Fatalf("SchemaExists: %v", err)
	}
	if !exists {
		t.Errorf("SchemaExists(%q) = false, want true", schema)
	}
}

// TestSchemaExists_InvalidDSNReturnsError confirms SchemaExists fails fast on
// a malformed DSN without needing a live database.
func TestSchemaExists_InvalidDSNReturnsError(t *testing.T) {
	if _, err := SchemaExists(context.Background(), "://nope", "identity"); err == nil {
		t.Error("SchemaExists() = nil error, want error for an invalid DSN")
	}
}

// TestAppliedVersion_PristineDatabaseReportsZero proves AppliedVersion reports
// 0 against a schema with no migrations applied yet — same "ensure the
// version table, report zero" behaviour Reset relies on, exercised through
// the new accessor instead.
func TestAppliedVersion_PristineDatabaseReportsZero(t *testing.T) {
	dsn := isolatedDSN(t)
	r := newFixtureRunner(t)
	ctx := context.Background()
	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	if got := appliedVersion(ctx, t, r, dsn); got != 0 {
		t.Errorf("AppliedVersion on a pristine schema = %d, want 0", got)
	}
}

// TestAppliedVersion_MatchesHighestAppliedStatus proves AppliedVersion agrees
// with the highest version Status reports as applied — the cross-check a
// caller relies on when comparing AppliedVersion against its own highest
// known migration version to detect a schema newer than it understands.
func TestAppliedVersion_MatchesHighestAppliedStatus(t *testing.T) {
	dsn := isolatedDSN(t)
	r := newFixtureRunner(t)
	ctx := context.Background()
	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})
	if err := r.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}

	statuses, err := r.Status(ctx, dsn)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	top := latestVersion(statuses)
	if top == 0 {
		t.Fatal("fixture Up applied no migrations")
	}

	if got := appliedVersion(ctx, t, r, dsn); got != top {
		t.Errorf("AppliedVersion = %d, want %d (Status's highest applied version)", got, top)
	}
}

// TestReset_OnPristineDatabase_IsANoOp proves the behaviour delta from the
// legacy dispatcher: Reset no longer needs a special case for a database
// with no goose_db_version table, because Provider.DownTo ensures that
// table (and its zero-version row) exists before reading applied versions.
func TestReset_OnPristineDatabase_IsANoOp(t *testing.T) {
	dsn := isolatedDSN(t)
	r := newFixtureRunner(t)
	ctx := context.Background()

	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}

	// The behaviour under test: Reset again against an already-empty
	// database must succeed, not error.
	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("Reset on pristine database: %v", err)
	}
	if v := appliedVersion(ctx, t, r, dsn); v != 0 {
		t.Errorf("applied version after Reset on pristine database = %d, want 0", v)
	}
}

// TestEnsureSchemaAndVersionTable proves the two construction options
// identity/migrate relies on actually take effect against a real database:
// WithEnsureSchema creates a schema goose itself never would, and
// WithVersionTable places goose's bookkeeping table inside it rather than
// the default public.goose_db_version — the pair that lets an
// independently-versioned migration set share a database with others
// without colliding on either the schema or the version table.
func TestEnsureSchemaAndVersionTable(t *testing.T) {
	dsn := isolatedDSN(t)
	const schema = "migrate_ensure_test"
	r, err := New(fixtureFS, fixtureDir,
		WithEnsureSchema(schema),
		WithVersionTable(schema+".goose_db_version"),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	ctx := context.Background()

	if err := r.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	if !schemaExists(t, dsn, schema) {
		t.Errorf("schema %q was not created by WithEnsureSchema", schema)
	}
	if !tableExistsInSchema(t, dsn, schema, "goose_db_version") {
		t.Errorf("version table %s.goose_db_version was not created", schema)
	}
}

// TestConcurrentEnsureSchema_NoDuplicateSchemaError proves ensureSchema
// tolerates losing a race to a concurrent creator: CREATE SCHEMA IF NOT
// EXISTS is not atomic, and ensureSchema runs before goose's own advisory
// lock (only acquired later, inside an operation like Up), so two callers
// racing against a schema that does not exist yet must both succeed rather
// than one failing on a duplicate-object error.
//
// This calls ensureSchema directly, over its own dedicated connections,
// rather than going through Runner.Up: racing full unlocked migration runs
// would additionally race the fixture migrations' own unqualified CREATE
// TABLE statements against each other (a different, unrelated failure mode
// that has nothing to do with schema creation, and would corrupt this
// package's shared fixture database for every other gated test in it). The
// schema is dropped first so the race window is guaranteed open when the
// concurrent calls start — TestConcurrentUp_SessionLock_AppliesExactlyOnce
// in identity/migrate does not cover this path at all, because its setup
// Reset already creates the schema before its concurrent Ups begin, and it
// races under WithSessionLock, which serializes the migrations themselves
// rather than exercising ensureSchema's own pre-lock race window.
func TestConcurrentEnsureSchema_NoDuplicateSchemaError(t *testing.T) {
	dsn := isolatedDSN(t)
	const schema = "migrate_ensure_race_test"
	ctx := context.Background()

	setup, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := setup.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		_ = setup.Close()
		t.Fatalf("drop schema %q to open the race window: %v", schema, err)
	}
	t.Cleanup(func() {
		defer func() { _ = setup.Close() }()
		if _, err := setup.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Logf("cleanup drop schema failed: %v", err)
		}
	})

	const concurrentCallers = 8
	errs := make([]error, concurrentCallers)
	var wg sync.WaitGroup
	for i := range concurrentCallers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each caller gets its own connection, standing in for a
			// separate process/binary rather than a shared one.
			conn, err := sql.Open("pgx", dsn)
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = conn.Close() }()
			errs[i] = ensureSchema(ctx, conn, schema)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent ensureSchema %d: %v", i, err)
		}
	}
	if !schemaExists(t, dsn, schema) {
		t.Errorf("schema %q was not created by any of the concurrent callers", schema)
	}
}

// schemaExists is a t.Fatal-on-error wrapper over the production SchemaExists,
// the assertion shape every caller below wants.
func schemaExists(t *testing.T, dsn, schema string) bool {
	t.Helper()
	exists, err := SchemaExists(context.Background(), dsn, schema)
	if err != nil {
		t.Fatalf("SchemaExists(%q): %v", schema, err)
	}
	return exists
}

func tableExistsInSchema(t *testing.T, dsn, schema, table string) bool {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var name *string
	if err := db.QueryRow(`SELECT to_regclass($1)`, schema+"."+table).Scan(&name); err != nil {
		t.Fatalf("query to_regclass(%q): %v", schema+"."+table, err)
	}
	return name != nil
}

// TestUpDownRoundTrip applies and rolls back the full fixture migration set
// against a real database. Skipped unless NESTCORE_TEST_DATABASE_URL is set,
// keeping the default test run hermetic.
func TestUpDownRoundTrip(t *testing.T) {
	dsn := isolatedDSN(t)
	r := newFixtureRunner(t)
	ctx := context.Background()

	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	if err := r.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, table := range []string{"widget", "gadget"} {
		if !tableExists(t, dsn, table) {
			t.Errorf("after Up, table %q does not exist", table)
		}
	}

	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, table := range []string{"widget", "gadget"} {
		if tableExists(t, dsn, table) {
			t.Errorf("after Reset, table %q still exists", table)
		}
	}
}

// TestDown_RollsBackExactlyOneVersion exercises single-migration rollback:
// Down must lower the applied version by exactly one, and the rollback must
// be reversible.
func TestDown_RollsBackExactlyOneVersion(t *testing.T) {
	dsn := isolatedDSN(t)
	r := newFixtureRunner(t)
	ctx := context.Background()

	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	if err := r.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}
	top := appliedVersion(ctx, t, r, dsn)

	if err := r.Down(ctx, dsn); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if got := appliedVersion(ctx, t, r, dsn); got != top-1 {
		t.Errorf("applied version after Down = %d, want %d", got, top-1)
	}

	if err := r.Up(ctx, dsn); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if got := appliedVersion(ctx, t, r, dsn); got != top {
		t.Errorf("applied version after re-Up = %d, want %d", got, top)
	}
}

// TestUpTo_LandsOnRequestedVersion proves UpTo stops exactly at the
// requested version rather than applying everything: 00003's table must be
// absent after UpTo(2).
func TestUpTo_LandsOnRequestedVersion(t *testing.T) {
	dsn := isolatedDSN(t)
	r := newFixtureRunner(t)
	ctx := context.Background()

	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	const target = 2
	if err := r.UpTo(ctx, dsn, target); err != nil {
		t.Fatalf("UpTo(%d): %v", target, err)
	}
	if got := appliedVersion(ctx, t, r, dsn); got != target {
		t.Errorf("applied version after UpTo(%d) = %d, want %d", target, got, target)
	}
	if !tableExists(t, dsn, "widget") {
		t.Error(`after UpTo(2), table "widget" does not exist`)
	}
	if tableExists(t, dsn, "gadget") {
		t.Error(`after UpTo(2), table "gadget" exists, want absent (migration 00003 not yet applied)`)
	}
}

// TestDownTo_LandsOnRequestedVersion mirrors TestUpTo_LandsOnRequestedVersion
// for the down direction: it must land on exactly the requested version.
func TestDownTo_LandsOnRequestedVersion(t *testing.T) {
	dsn := isolatedDSN(t)
	r := newFixtureRunner(t)
	ctx := context.Background()

	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	if err := r.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}
	const target = 1
	if err := r.DownTo(ctx, dsn, target); err != nil {
		t.Fatalf("DownTo(%d): %v", target, err)
	}
	if got := appliedVersion(ctx, t, r, dsn); got != target {
		t.Errorf("applied version after DownTo(%d) = %d, want %d", target, got, target)
	}
	if tableExists(t, dsn, "gadget") {
		t.Error(`after DownTo(1), table "gadget" exists, want absent`)
	}
	if !tableExists(t, dsn, "widget") {
		t.Error(`after DownTo(1), table "widget" does not exist`)
	}
}

// TestStatus_ReportsAppliedPendingSplit proves Status reports each
// migration's real applied/pending state rather than a fixed count.
func TestStatus_ReportsAppliedPendingSplit(t *testing.T) {
	dsn := isolatedDSN(t)
	r := newFixtureRunner(t)
	ctx := context.Background()

	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	if err := r.UpTo(ctx, dsn, 2); err != nil {
		t.Fatalf("UpTo(2): %v", err)
	}

	statuses, err := r.Status(ctx, dsn)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 3 {
		t.Fatalf("len(statuses) = %d, want 3", len(statuses))
	}

	want := []migrationStatusExpectation{
		{version: 1, applied: true},
		{version: 2, applied: true},
		{version: 3, applied: false},
	}
	for i, w := range want {
		assertMigrationStatus(t, i, statuses[i], w)
	}
}

// migrationStatusExpectation is one row of TestStatus_ReportsAppliedPendingSplit's
// expected table: the version's applied/pending state, checked against a
// live MigrationStatus by assertMigrationStatus.
type migrationStatusExpectation struct {
	version int64
	applied bool
}

// assertMigrationStatus checks a single MigrationStatus against its
// expectation, reporting index i on failure so a mismatch names the row
// that produced it.
func assertMigrationStatus(t *testing.T, i int, got MigrationStatus, want migrationStatusExpectation) {
	t.Helper()
	if got.Version != want.version {
		t.Errorf("statuses[%d].Version = %d, want %d", i, got.Version, want.version)
	}
	if got.Applied != want.applied {
		t.Errorf("statuses[%d].Applied = %v, want %v", i, got.Applied, want.applied)
	}
	if want.applied && got.AppliedAt.IsZero() {
		t.Errorf("statuses[%d].AppliedAt is zero, want a timestamp", i)
	}
	if !want.applied && !got.AppliedAt.IsZero() {
		t.Errorf("statuses[%d].AppliedAt = %v, want zero (pending)", i, got.AppliedAt)
	}
}

// appliedVersion is a t.Fatal-on-error wrapper over the production
// AppliedVersion, the assertion shape every caller below wants.
func appliedVersion(ctx context.Context, t *testing.T, r *Runner, dsn string) int64 {
	t.Helper()
	v, err := r.AppliedVersion(ctx, dsn)
	if err != nil {
		t.Fatalf("AppliedVersion: %v", err)
	}
	return v
}

func tableExists(t *testing.T, dsn, table string) bool {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var name *string
	if err := db.QueryRow(`SELECT to_regclass('public.' || $1)`, table).Scan(&name); err != nil {
		t.Fatalf("query to_regclass(%q): %v", table, err)
	}
	return name != nil
}

// openTestDB opens a raw *sql.DB against dsn, registering its own Close
// cleanup.
func openTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// latestVersion returns the highest version among statuses reported as
// applied, or 0 if none are.
func latestVersion(statuses []MigrationStatus) int64 {
	var top int64
	for _, s := range statuses {
		if s.Applied && s.Version > top {
			top = s.Version
		}
	}
	return top
}
