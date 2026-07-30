// Package migrate runs database schema migrations with goose over the pgx
// stdlib driver (a database/sql handle, distinct from an application's
// pgxpool). The migration set is supplied by the caller — an fs.FS and a
// directory within it, via New — rather than embedded in this package, so
// each application embeds and owns its own migrations and this package only
// supplies the up/down/status/reset/up-to/down-to plumbing plus the
// pooler-safe connection handling.
//
// It is built on goose's Provider API (goose.NewProvider), not the legacy
// package-level dispatcher (goose.RunContext). The legacy API configures
// goose through process-global state — one base fs.FS and one dialect, both
// set once via init() — so a library built on it could only ever serve one
// migration set per process. Provider takes its filesystem per instance and
// holds no global state, so a Runner has none either: two Runners over
// different migration sets coexist safely in one process.
//
// goose records applied migrations in its goose_db_version table (created
// automatically on first run); the up/down/status/reset/up-to/down-to
// operations consult it to compute the delta to apply or revert.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver + OpenDB
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

const dialect = goose.DialectPostgres

// Runner applies migrations from one caller-supplied filesystem. Construct
// one with New; it holds no process-global state, so multiple Runners over
// different migration sets are safe to use concurrently in one process.
type Runner struct {
	migrations   fs.FS
	versionTable string
	ensureSchema string
	sessionLock  bool
}

// newOptions configures a Runner's construction-time behavior: which goose
// version table it bookkeeps against, an optional schema to guarantee
// exists first, and whether its operations serialize against each other via
// a session-level advisory lock. Distinct from Option (below), which
// configures a single Up/Down/etc. call rather than the Runner itself.
type newOptions struct {
	versionTable string
	ensureSchema string
	sessionLock  bool
}

// NewOption customizes New.
type NewOption func(*newOptions)

// WithVersionTable sets a non-default goose version table name — optionally
// schema-qualified as "schema.table" — for a migration set that shares a
// database with other independently versioned migration sets and so cannot
// use goose's default goose_db_version.
func WithVersionTable(name string) NewOption {
	return func(o *newOptions) { o.versionTable = name }
}

// WithEnsureSchema creates schema, via CREATE SCHEMA IF NOT EXISTS, on
// connect — before goose's Provider first touches its version table. goose
// itself never creates a schema, so a caller whose version table (see
// WithVersionTable) lives outside the database's default schema needs one
// guaranteed to exist first, or Provider construction fails trying to
// create that table against a schema that is not there yet.
func WithEnsureSchema(schema string) NewOption {
	return func(o *newOptions) { o.ensureSchema = schema }
}

// WithSessionLock enables a Postgres session-level advisory lock (goose's
// WithSessionLocker over lock.NewPostgresSessionLocker), held for the
// duration of each operation, so two processes racing an Up serialize
// instead of both applying migrations at once.
//
// The lock is goose's lock.DefaultLockID, and Postgres advisory locks are
// scoped to the DATABASE rather than to a migration set — so this
// serializes against EVERY goose session-locked migration set in the same
// database, not only against another Runner over this one. That is safe but
// coarser than it sounds; if a set ever needs its own lock, this option has
// to grow a lock ID (goose's lock.WithLockID).
func WithSessionLock() NewOption {
	return func(o *newOptions) { o.sessionLock = true }
}

// New returns a Runner over the .sql migrations rooted at dir within fsys.
// dir may be "" or "." for a filesystem that is already rooted at its
// migrations (e.g. a //go:embed of just the migration files); any other
// value names a subdirectory of fsys.
//
// New fails fast when dir holds no .sql migrations, rather than deferring
// that discovery to the first Up/Down/etc. call: goose's Provider globs the
// ROOT of the fs.FS it is given for "*.sql" — it does not walk into
// subdirectories — so this check mirrors exactly what every Runner method
// requires of the filesystem it was built from. fs.Sub does not itself fail
// on a missing directory, so without this check a bad dir would surface
// only much later, as an opaque "no migrations found" from inside goose.
func New(fsys fs.FS, dir string, opts ...NewOption) (*Runner, error) {
	if dir == "" {
		dir = "."
	}
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: sub filesystem %q: %w", dir, err)
	}
	matches, err := fs.Glob(sub, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("migrate: glob %q for *.sql: %w", dir, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("migrate: no .sql migrations found in %q", dir)
	}

	var o newOptions
	for _, opt := range opts {
		opt(&o)
	}
	return &Runner{
		migrations:   sub,
		versionTable: o.versionTable,
		ensureSchema: o.ensureSchema,
		sessionLock:  o.sessionLock,
	}, nil
}

// options configures how a migration command connects.
type options struct {
	poolerSafe bool
}

// Option customizes a migration run.
type Option func(*options)

// PoolerSafe configures the migration connection to use the simple query
// protocol so goose's version-bookkeeping queries do not rely on named
// server-side prepared statements, which a transaction pooler (PgBouncer /
// Supabase Supavisor) cannot keep across multiplexed transactions. Prefer
// pointing the DSN at a direct/session connection over enabling this.
func PoolerSafe() Option { return func(o *options) { o.poolerSafe = true } }

// Up applies all pending migrations.
func (r *Runner) Up(ctx context.Context, dsn string, opts ...Option) error {
	p, err := r.newProvider(ctx, dsn, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// Down rolls back the most recently applied migration. If nothing is
// applied, it returns a wrapped goose.ErrNoNextVersion — unlike Reset and
// DownTo, Down does not treat an empty database as a no-op.
func (r *Runner) Down(ctx context.Context, dsn string, opts ...Option) error {
	p, err := r.newProvider(ctx, dsn, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	if _, err := p.Down(ctx); err != nil {
		return fmt.Errorf("goose down: %w", err)
	}
	return nil
}

// Status returns the applied/pending state of every migration in the
// Runner's filesystem, ordered by version ascending. It performs no
// output itself; pass the result to WriteStatus to render it.
func (r *Runner) Status(ctx context.Context, dsn string, opts ...Option) ([]MigrationStatus, error) {
	p, err := r.newProvider(ctx, dsn, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = p.Close() }()

	gooseStatuses, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("goose status: %w", err)
	}

	statuses := make([]MigrationStatus, len(gooseStatuses))
	for i, s := range gooseStatuses {
		statuses[i] = MigrationStatus{
			Version:   s.Source.Version,
			Source:    filepath.Base(s.Source.Path),
			Applied:   s.State == goose.StateApplied,
			AppliedAt: s.AppliedAt,
		}
	}
	return statuses, nil
}

// Reset rolls back every applied migration, via DownTo(0). Intended for
// tests and local resets.
//
// Unlike the legacy dispatcher this package used to sit on, Reset needs no
// special case for a pristine database: goose's Provider ensures the
// version table (and its zero-version row) exists before reading applied
// versions, so DownTo(0) against an already-empty schema is a clean no-op
// rather than an error.
//
// Reset is also STRICTER than the legacy behaviour about orphan versions: a
// database row recorded for a version with no corresponding migration file
// — for example, one migrated from a different branch — now fails loudly
// instead of being silently skipped. That is deliberate; do not read it as
// a bug.
func (r *Runner) Reset(ctx context.Context, dsn string, opts ...Option) error {
	p, err := r.newProvider(ctx, dsn, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	if _, err := p.DownTo(ctx, 0); err != nil {
		return fmt.Errorf("goose reset: %w", err)
	}
	return nil
}

// UpTo applies migrations up to and including the given goose version — the
// migration file's numeric filename prefix, e.g. 24 for
// 00024_reward_catalog_admin.sql.
//
// This exists for gated tests that need to seed data against an
// intermediate schema (i.e. stop applying migrations partway through) and
// then apply one specific migration on top of it, to prove that migration
// handles pre-existing rows correctly — coverage a plain Reset+Up cannot
// provide, since that always starts from an empty database where a backfill
// UPDATE trivially matches zero rows.
func (r *Runner) UpTo(ctx context.Context, dsn string, version int64, opts ...Option) error {
	p, err := r.newProvider(ctx, dsn, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	if _, err := p.UpTo(ctx, version); err != nil {
		return fmt.Errorf("goose up-to %d: %w", version, err)
	}
	return nil
}

// DownTo rolls back migrations until only those up to and including the
// given goose version remain applied — the mirror of UpTo. DownTo(ctx, dsn,
// 24) leaves 00024 applied and rolls back everything above it.
//
// This is the pinned-version-boundary pattern for migration tests: a test
// that means "roll back exactly migration N" must use DownTo(N-1), never
// Down. Down rolls back whatever is LATEST, so a test written against it
// silently starts exercising a different migration the moment another one
// lands on top. Pinning both boundaries keeps such a test meaningful at any
// future highest version.
func (r *Runner) DownTo(ctx context.Context, dsn string, version int64, opts ...Option) error {
	p, err := r.newProvider(ctx, dsn, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	if _, err := p.DownTo(ctx, version); err != nil {
		return fmt.Errorf("goose down-to %d: %w", version, err)
	}
	return nil
}

// AppliedVersion returns the highest migration version currently recorded as
// applied in the database — via goose's own Provider.GetDBVersion, so it
// reflects what the DATABASE itself has recorded rather than r's filesystem.
// Unlike Status (which reports one entry per migration r's filesystem knows
// about), a version applied by a newer binary sharing this migration set,
// with no corresponding source file here, still surfaces correctly: this is
// what a caller compares against its own highest known version to detect
// that case.
//
// AppliedVersion is NOT side-effect-free, despite reading like a probe.
// goose's GetDBVersion goes through Provider.initialize(ctx, true), so it
// acquires r's session lock when r was built WithSessionLock — blocking up
// to goose's five-minute lock retry against a concurrent migration, then
// failing with an opaque lock error — and it ensures the version table
// exists (CREATE TABLE plus the zero-version INSERT). r's own
// WithEnsureSchema runs first and creates the schema. A caller probing a
// shared schema it must neither create nor contend for should call
// SchemaExists first and build its Runner WITHOUT WithSessionLock, exactly
// as identity/migrate.RequireVersion does and for the same reason.
func (r *Runner) AppliedVersion(ctx context.Context, dsn string, opts ...Option) (int64, error) {
	p, err := r.newProvider(ctx, dsn, opts)
	if err != nil {
		return 0, err
	}
	defer func() { _ = p.Close() }()

	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("goose get db version: %w", err)
	}
	return v, nil
}

// SchemaExists reports whether schema exists as a Postgres schema at dsn, via
// a direct catalog query. Unlike every other operation in this package, it
// neither creates the schema nor touches goose's version-table machinery —
// callers use it to check what is there BEFORE deciding whether the
// schema-creating path (Up, or anything built with WithEnsureSchema) is
// appropriate. Accepts the same Option set as every other operation here —
// notably PoolerSafe, for a caller whose only DSN is a transaction pooler.
func SchemaExists(ctx context.Context, dsn, schema string, opts ...Option) (bool, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	db, err := connect(ctx, dsn, o.poolerSafe)
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()

	var exists bool
	if err := db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check schema %q exists: %w", schema, err)
	}
	return exists, nil
}

// MigrationStatus is one migration's applied/pending state, independent of
// goose's own type so goose stays out of this package's public API surface.
type MigrationStatus struct {
	Version   int64
	Source    string // filepath.Base of the migration file
	Applied   bool
	AppliedAt time.Time // zero value if not applied
}

// WriteStatus renders statuses to w in the same table format the legacy
// goose dispatcher printed — through its own logger, which, despite this
// package's former doc comment claiming stdout, actually wrote to stderr.
// Callers now choose the destination explicitly; Nestova passes os.Stdout,
// which finally makes that documented behaviour true.
func WriteStatus(w io.Writer, statuses []MigrationStatus) error {
	if _, err := fmt.Fprintln(w, "    Applied At                  Migration"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "    ======================================="); err != nil {
		return err
	}
	for _, s := range statuses {
		appliedAt := "Pending"
		if s.Applied {
			appliedAt = s.AppliedAt.Format(time.ANSIC)
		}
		if _, err := fmt.Fprintf(w, "    %-24s -- %v\n", appliedAt, s.Source); err != nil {
			return err
		}
	}
	return nil
}

// newProvider resolves opts and returns a goose Provider bound to dsn and
// r's migrations. The caller must Close the returned Provider, which also
// closes the *sql.DB opened here — do not additionally close it yourself.
func (r *Runner) newProvider(ctx context.Context, dsn string, opts []Option) (*goose.Provider, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	db, err := connect(ctx, dsn, o.poolerSafe)
	if err != nil {
		return nil, err
	}

	if r.ensureSchema != "" {
		if err := ensureSchema(ctx, db, r.ensureSchema); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	providerOpts, err := r.providerOptions()
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	p, err := goose.NewProvider(dialect, db, r.migrations, providerOpts...)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("build goose provider: %w", err)
	}
	return p, nil
}

// providerOptions translates r's construction-time configuration into goose
// ProviderOptions. Kept separate from newProvider so the session locker's
// own construction error — the only one of the three that can fail — is
// handled in one place rather than inline in an already-branchy function.
func (r *Runner) providerOptions() ([]goose.ProviderOption, error) {
	var opts []goose.ProviderOption
	if r.versionTable != "" {
		opts = append(opts, goose.WithTableName(r.versionTable))
	}
	if r.sessionLock {
		locker, err := lock.NewPostgresSessionLocker()
		if err != nil {
			return nil, fmt.Errorf("build session locker: %w", err)
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	return opts, nil
}

// duplicateSchemaCode and uniqueViolationCode are the SQLSTATEs a
// CREATE SCHEMA IF NOT EXISTS losing a race to a concurrent creator can
// surface: Postgres either reports the schema as a duplicate object, or
// fails inserting into pg_namespace's unique index.
const (
	duplicateSchemaCode = "42P06"
	uniqueViolationCode = "23505"
)

// ensureSchema creates schema via CREATE SCHEMA IF NOT EXISTS over db.
// schema is always a compile-time literal supplied by the caller of New
// (never end-user input), but it is quoted as a Postgres identifier anyway
// since CREATE SCHEMA takes no bind parameters.
//
// IF NOT EXISTS is not atomic against a concurrent creator, and this runs
// before goose's own advisory lock (which goose acquires per operation, on
// its own connection, only once Provider.Up/Status/etc. is called) — so a
// duplicate-object failure here means another process created the schema
// first, which is exactly the postcondition this function promises.
func ensureSchema(ctx context.Context, db *sql.DB, schema string) error {
	stmt := "CREATE SCHEMA IF NOT EXISTS " + (pgx.Identifier{schema}).Sanitize()
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) &&
			(pgErr.Code == duplicateSchemaCode || pgErr.Code == uniqueViolationCode) {
			return nil
		}
		return fmt.Errorf("ensure schema %q: %w", schema, err)
	}
	return nil
}

// connect opens a database/sql handle via openDB and verifies connectivity up
// front so an invalid DSN or unreachable database fails with a clear error
// before goose starts. The caller owns closing the returned handle.
func connect(ctx context.Context, dsn string, poolerSafe bool) (*sql.DB, error) {
	db, err := openDB(dsn, poolerSafe)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return db, nil
}

// openDB returns a database/sql handle over the pgx driver. The default path
// uses the registered "pgx" driver unchanged. The pooler-safe path opens a
// connection configured for the simple protocol so it works through a
// transaction pooler.
func openDB(dsn string, poolerSafe bool) (*sql.DB, error) {
	if !poolerSafe {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, fmt.Errorf("open database: %w", err)
		}
		return db, nil
	}
	connCfg, err := poolerSafeConnConfig(dsn)
	if err != nil {
		return nil, err
	}
	return stdlib.OpenDB(*connCfg), nil
}

// poolerSafeConnConfig parses dsn and selects the simple query protocol, which
// carries no named server-side prepared statements and so survives a
// transaction pooler's per-transaction connection multiplexing.
func poolerSafeConnConfig(dsn string) (*pgx.ConnConfig, error) {
	connCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}
	connCfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	return connCfg, nil
}
