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

// appliedVersion returns the current goose migration version recorded in the
// database, via r's own Provider rather than any global goose state. Takes
// ctx from the caller rather than manufacturing its own, matching every
// other helper here.
func appliedVersion(ctx context.Context, t *testing.T, r *Runner, dsn string) int64 {
	t.Helper()
	p, err := r.newProvider(ctx, dsn, nil)
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	v, err := p.GetDBVersion(ctx)
	if err != nil {
		t.Fatalf("GetDBVersion: %v", err)
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
