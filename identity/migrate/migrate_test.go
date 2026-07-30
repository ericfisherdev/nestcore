// package migrate_test (an external test package, not package migrate) is
// deliberate: it needs nestcore/db/dbtest.Harness, and importing dbtest from
// inside package migrate itself would tangle this package's own import graph
// unnecessarily. The ungated check that needs the package's unexported
// migrationsFS lives separately in embed_test.go, as package migrate.
package migrate_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	ncdbtest "github.com/ericfisherdev/nestcore/db/dbtest"
	ncmigrate "github.com/ericfisherdev/nestcore/db/migrate"

	"github.com/ericfisherdev/nestcore/identity/migrate"
)

// harness derives isolated test databases from NESTCORE_TEST_DATABASE_URL,
// one per gated test (via harness.DSN), the same base nestcore/db/migrate's
// own gated tests use. Package-level and shared across this file's tests
// rather than reconstructed per test, mirroring how an application wires
// this harness once (see nestorage's internal/platform/db/dbtest).
var harness = newHarness()

func newHarness() *ncdbtest.Harness {
	runner, err := migrate.New()
	if err != nil {
		// The embedded migration set is fixed at build time, so a failure
		// here means the embed itself is broken — a programming error, not
		// a runtime condition any caller of harness could recover from.
		panic(fmt.Sprintf("identity/migrate: %v", err))
	}
	return ncdbtest.New("NESTCORE_TEST_DATABASE_URL", runnerMigrator{runner})
}

// runnerMigrator adapts *ncmigrate.Runner to nestcore/db/dbtest's Migrator
// interface. The adapter is load-bearing, not decorative: dbtest.Migrator
// declares Reset(ctx, dsn) error and Up(ctx, dsn) error, while the Runner's
// own methods take a trailing opts ...ncmigrate.Option, and Go requires an
// exact method signature to satisfy an interface — the variadic cannot be
// dropped by assignment.
type runnerMigrator struct {
	runner *ncmigrate.Runner
}

func (m runnerMigrator) Reset(ctx context.Context, dsn string) error {
	return m.runner.Reset(ctx, dsn)
}

func (m runnerMigrator) Up(ctx context.Context, dsn string) error {
	return m.runner.Up(ctx, dsn)
}

// newRunner returns a fresh Runner for one test's exclusive use — every test
// constructs its own rather than sharing package state, mirroring
// db/migrate's own gated tests.
func newRunner(t *testing.T) *ncmigrate.Runner {
	t.Helper()
	r, err := migrate.New()
	if err != nil {
		t.Fatalf("migrate.New(): %v", err)
	}
	return r
}

// resetAndUp resets dsn to empty and applies every identity migration,
// registering a cleanup Reset — the setup every constraint test below
// shares.
func resetAndUp(t *testing.T, dsn string) *ncmigrate.Runner {
	t.Helper()
	r := newRunner(t)
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
	return r
}

// TestUpDownUp proves a full up/down/up cycle against every identity
// migration: Up creates every table this package owns, Reset (DownTo(0))
// drops them all, and Up again succeeds from empty.
func TestUpDownUp(t *testing.T) {
	dsn := harness.DSN(t, "identity_updownup")
	ctx := context.Background()
	r := resetAndUp(t, dsn)

	tables := []string{
		"household", "member", "member_mfa", "member_recovery_code",
		"member_credential", "sessions",
	}
	for _, table := range tables {
		if !tableExists(t, dsn, table) {
			t.Errorf("after Up, identity.%s does not exist", table)
		}
	}

	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, table := range tables {
		if tableExists(t, dsn, table) {
			t.Errorf("after Reset, identity.%s still exists", table)
		}
	}

	if err := r.Up(ctx, dsn); err != nil {
		t.Fatalf("Up after Reset: %v", err)
	}
	for _, table := range tables {
		if !tableExists(t, dsn, table) {
			t.Errorf("after re-Up, identity.%s does not exist", table)
		}
	}
}

// TestUp_AdoptsNestorageShapedSchema proves this migration set can run
// against a database where Nestorage's own interim
// internal/platform/db/migrate/migrations/00017_identity_schema.sql already
// created identity.household/identity.member/identity.sessions directly
// (guarded with IF NOT EXISTS specifically so this set could adopt them) —
// rather than failing on a duplicate-object error, and rather than leaving
// Nestorage's shape (NOT NULL credentials, no uniqueness indexes) in place
// where 00002/00003's composite FKs need it reconciled.
func TestUp_AdoptsNestorageShapedSchema(t *testing.T) {
	dsn := harness.DSN(t, "identity_adopt")
	ctx := context.Background()
	db := openDB(t, dsn)

	// Pre-create Nestorage's interim shape directly, before this package's
	// Runner ever touches the database — simulating Nestorage having
	// deployed and migrated first.
	for _, stmt := range []string{
		`CREATE SCHEMA IF NOT EXISTS identity`,
		`CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public`,
		`CREATE EXTENSION IF NOT EXISTS citext SCHEMA public`,
		`CREATE TABLE IF NOT EXISTS identity.household (
			id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
			name       text        NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS identity.member (
			id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
			household_id  uuid        NOT NULL REFERENCES identity.household (id) ON DELETE CASCADE,
			display_name  text        NOT NULL,
			email         citext      NOT NULL,
			password_hash text        NOT NULL,
			role          text        NOT NULL CHECK (role IN ('owner', 'adult', 'child')),
			active        boolean     NOT NULL DEFAULT true,
			created_at    timestamptz NOT NULL DEFAULT now(),
			updated_at    timestamptz NOT NULL DEFAULT now(),
			CONSTRAINT member_email_unique UNIQUE (email)
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("pre-create Nestorage-shaped schema: %v\nstatement: %s", err, stmt)
		}
	}

	r := newRunner(t)
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	if err := r.Up(ctx, dsn); err != nil {
		t.Fatalf("Up against a Nestorage-shaped identity.member = %v, want success", err)
	}

	var emailNullable string
	if err := db.QueryRow(
		`SELECT is_nullable FROM information_schema.columns
		 WHERE table_schema = 'identity' AND table_name = 'member' AND column_name = 'email'`,
	).Scan(&emailNullable); err != nil {
		t.Fatalf("query email nullability: %v", err)
	}
	if emailNullable != "YES" {
		t.Error("identity.member.email is still NOT NULL after Up, want nullable")
	}

	household := insertHousehold(t, db, "Adopted Household")
	emailOnly := "adopted-email-only@example.com"
	if err := insertMember(db, household, "EmailOnly", "adult", &emailOnly, nil, nil); !isCheckViolation(err) {
		t.Errorf("insert member with email but no password_hash = %v, want a CHECK violation", err)
	}
	if err := insertMember(db, household, "Neither", "child", nil, nil, nil); err != nil {
		t.Errorf("insert member with neither email nor password_hash: %v", err)
	}

	for _, table := range []string{"member_mfa", "member_credential"} {
		if !tableExists(t, dsn, table) {
			t.Errorf("after Up on adopted schema, identity.%s does not exist", table)
		}
	}
}

// TestRoleCheck_RejectsAdmin proves the role CHECK admits exactly owner,
// adult, child and nothing else.
func TestRoleCheck_RejectsAdmin(t *testing.T) {
	dsn := harness.DSN(t, "identity_role")
	resetAndUp(t, dsn)
	db := openDB(t, dsn)
	household := insertHousehold(t, db, "Role Test Household")

	for _, role := range []string{"owner", "adult", "child"} {
		if err := insertMember(db, household, "Member "+role, role, nil, nil, nil); err != nil {
			t.Errorf("insert member with role %q: %v", role, err)
		}
	}

	err := insertMember(db, household, "Admin Member", "admin", nil, nil, nil)
	if !isCheckViolation(err) {
		t.Errorf("insert member with role %q = %v, want a CHECK violation", "admin", err)
	}
}

// TestCredentialsCompleteCheck proves member_credentials_complete enforces
// email and password_hash as both-or-neither.
func TestCredentialsCompleteCheck(t *testing.T) {
	dsn := harness.DSN(t, "identity_credentials")
	resetAndUp(t, dsn)
	db := openDB(t, dsn)
	household := insertHousehold(t, db, "Credentials Test Household")

	email := "both@example.com"
	hash := "hash"
	if err := insertMember(db, household, "Both", "adult", &email, &hash, nil); err != nil {
		t.Errorf("insert member with both email and password_hash: %v", err)
	}
	if err := insertMember(db, household, "Neither", "child", nil, nil, nil); err != nil {
		t.Errorf("insert member with neither email nor password_hash: %v", err)
	}

	emailOnly := "email-only@example.com"
	err := insertMember(db, household, "EmailOnly", "adult", &emailOnly, nil, nil)
	if !isCheckViolation(err) {
		t.Errorf("insert member with email but no password_hash = %v, want a CHECK violation", err)
	}

	hashOnly := "hash-only"
	err = insertMember(db, household, "HashOnly", "adult", nil, &hashOnly, nil)
	if !isCheckViolation(err) {
		t.Errorf("insert member with password_hash but no email = %v, want a CHECK violation", err)
	}
}

// TestDisplayNameUniqueness_CaseInsensitive proves member_household_name_uniq
// enforces case-insensitive uniqueness scoped to one household, and that the
// same display name is fine in a different household.
func TestDisplayNameUniqueness_CaseInsensitive(t *testing.T) {
	dsn := harness.DSN(t, "identity_name_uniq")
	resetAndUp(t, dsn)
	db := openDB(t, dsn)
	householdA := insertHousehold(t, db, "Household A")
	householdB := insertHousehold(t, db, "Household B")

	if err := insertMember(db, householdA, "Alice", "adult", nil, nil, nil); err != nil {
		t.Fatalf("insert Alice: %v", err)
	}

	err := insertMember(db, householdA, "ALICE", "adult", nil, nil, nil)
	if !isUniqueViolation(err) {
		t.Errorf("insert case-variant duplicate name in the same household = %v, want a unique violation", err)
	}

	if err := insertMember(db, householdB, "Alice", "adult", nil, nil, nil); err != nil {
		t.Errorf("insert same display name in a different household: %v", err)
	}
}

// TestActive_DefaultsTrueAndAcceptsFalse proves active defaults to true on
// insert and that an explicit false round-trips.
func TestActive_DefaultsTrueAndAcceptsFalse(t *testing.T) {
	dsn := harness.DSN(t, "identity_active")
	resetAndUp(t, dsn)
	db := openDB(t, dsn)
	household := insertHousehold(t, db, "Active Test Household")

	defaultTrue := true
	if err := insertMember(db, household, "Defaulted", "adult", nil, nil, nil); err != nil {
		t.Fatalf("insert without active: %v", err)
	}
	if got := memberActive(t, db, household, "Defaulted"); got != defaultTrue {
		t.Errorf("active after insert with no explicit value = %v, want %v", got, defaultTrue)
	}

	explicitFalse := false
	if err := insertMember(db, household, "Deactivated", "adult", nil, nil, &explicitFalse); err != nil {
		t.Fatalf("insert with active = false: %v", err)
	}
	if got := memberActive(t, db, household, "Deactivated"); got != explicitFalse {
		t.Errorf("active after explicit false = %v, want %v", got, explicitFalse)
	}
}

// TestConcurrentUp_SessionLock_AppliesExactlyOnce proves the session-level
// advisory lock New enables serializes two concurrent Up calls, which two
// apps booting at once against a fresh database would otherwise race:
// neither call errors, and every migration ends up applied exactly once.
func TestConcurrentUp_SessionLock_AppliesExactlyOnce(t *testing.T) {
	dsn := harness.DSN(t, "identity_concurrent")
	ctx := context.Background()
	setup := newRunner(t)
	if err := setup.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := setup.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	const concurrentRunners = 2
	errs := make([]error, concurrentRunners)
	var wg sync.WaitGroup
	for i := range concurrentRunners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine builds its own Runner, standing in for a
			// separate process/binary rather than a shared one.
			r, err := migrate.New()
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = r.Up(ctx, dsn)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Up %d: %v", i, err)
		}
	}

	statuses, err := setup.Status(ctx, dsn)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("Status() reported no migrations")
	}
	for _, s := range statuses {
		if !s.Applied {
			t.Errorf("migration %s not applied after concurrent Up", s.Source)
		}
	}
}

// TestRequireVersion_PropagatesStatusError proves RequireVersion surfaces a
// wrapped error when reading the schema's status fails, rather than only
// ever exercising the below-minimum path. Deterministic and hermetic: an
// unparsable DSN fails inside connect() before any network attempt, so this
// needs no database and runs in the default suite.
func TestRequireVersion_PropagatesStatusError(t *testing.T) {
	if err := migrate.RequireVersion(context.Background(), "://not-a-valid-dsn", 1); err == nil {
		t.Error("RequireVersion with an unparsable DSN = nil error, want error")
	}
}

// TestRequireVersion proves RequireVersion refuses a schema older than the
// requested minimum and accepts one at or above it.
func TestRequireVersion(t *testing.T) {
	dsn := harness.DSN(t, "identity_require_version")
	ctx := context.Background()
	r := newRunner(t)
	if err := r.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	if err := migrate.RequireVersion(ctx, dsn, 1); err == nil {
		t.Error("RequireVersion on an empty schema = nil error, want an error")
	}

	if err := r.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}
	statuses, err := r.Status(ctx, dsn)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	top := latestVersion(statuses)

	if err := migrate.RequireVersion(ctx, dsn, top); err != nil {
		t.Errorf("RequireVersion(min=%d) on a schema at exactly that version = %v, want nil", top, err)
	}
	if err := migrate.RequireVersion(ctx, dsn, top-1); err != nil {
		t.Errorf("RequireVersion(min=%d) on a newer-than-built schema = %v, want nil (newer is allowed)", top-1, err)
	}
	if err := migrate.RequireVersion(ctx, dsn, top+1); err == nil {
		t.Errorf("RequireVersion(min=%d) on a schema one version below it = nil error, want an error", top+1)
	}
}

func latestVersion(statuses []ncmigrate.MigrationStatus) int64 {
	var top int64
	for _, s := range statuses {
		if s.Applied && s.Version > top {
			top = s.Version
		}
	}
	return top
}

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertHousehold(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(
		`INSERT INTO identity.household (name) VALUES ($1) RETURNING id`, name,
	).Scan(&id); err != nil {
		t.Fatalf("insert household %q: %v", name, err)
	}
	return id
}

func insertMember(db *sql.DB, householdID, displayName, role string, email, passwordHash *string, active *bool) error {
	_, err := db.Exec(
		`INSERT INTO identity.member (household_id, display_name, role, email, password_hash, active)
		 VALUES ($1, $2, $3, $4, $5, COALESCE($6, true))`,
		householdID, displayName, role, email, passwordHash, active,
	)
	return err
}

func memberActive(t *testing.T, db *sql.DB, householdID, displayName string) bool {
	t.Helper()
	var active bool
	if err := db.QueryRow(
		`SELECT active FROM identity.member WHERE household_id = $1 AND display_name = $2`,
		householdID, displayName,
	).Scan(&active); err != nil {
		t.Fatalf("query active for %q: %v", displayName, err)
	}
	return active
}

func tableExists(t *testing.T, dsn, table string) bool {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var name *string
	if err := db.QueryRow(`SELECT to_regclass('identity.' || $1)`, table).Scan(&name); err != nil {
		t.Fatalf("query to_regclass(%q): %v", table, err)
	}
	return name != nil
}

// checkViolationCode is Postgres's SQLSTATE for a CHECK constraint failure.
const checkViolationCode = "23514"

// uniqueViolationCode is Postgres's SQLSTATE for a UNIQUE constraint or
// unique index failure.
const uniqueViolationCode = "23505"

func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == checkViolationCode
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}
