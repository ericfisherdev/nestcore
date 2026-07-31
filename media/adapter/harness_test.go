// package adapter_test (an external test package, not package adapter) so
// these gated tests can depend on nestcore/db/dbtest and
// nestcore/identity/migrate without tangling the adapter package's own
// import graph — mirrors identity/adapter's own harness_test.go.
//
// media/adapter ships no migrations of its own (see the package doc: every
// consumer owns its OWN "photo" table). This harness therefore builds the
// canonical shape directly with raw SQL, in the derived test database's
// default "public" schema — deliberately NOT "nestova" or "nestorage" — so
// these tests double as the "second consumer, different schema" proof the
// package doc's additive-only contract is exercised against (see
// photo_postgres_test.go).
package adapter_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ncdbtest "github.com/ericfisherdev/nestcore/db/dbtest"
	identitymigrate "github.com/ericfisherdev/nestcore/identity/migrate"
)

// harness derives isolated test databases from NESTCORE_TEST_DATABASE_URL,
// the same base identity/adapter's own gated tests use.
var harness = ncdbtest.New("NESTCORE_TEST_DATABASE_URL", photoSchemaMigrator{})

// photoSchemaMigrator satisfies ncdbtest.Migrator: Up first applies the
// identity schema (the canonical "photo" table's household_id/uploaded_by
// FKs reference it), then creates a "photo" table matching the package
// doc's canonical shape, UNQUALIFIED, in whatever schema is on the fresh
// database's default search_path ("public") — never "nestova" or
// "nestorage". Reset tears down both, in dependency order.
type photoSchemaMigrator struct{}

const createPhotoTable = `
CREATE TABLE photo (
    id               uuid        PRIMARY KEY,
    household_id     uuid        NOT NULL REFERENCES identity.household(id),
    storage_ref      text        NOT NULL,
    storage_backend  text        NOT NULL,
    content_sha256   text,
    size_bytes       bigint      NOT NULL,
    content_type     text        NOT NULL,
    taken_at         timestamptz,
    uploaded_by      uuid        REFERENCES identity.member(id),
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX photo_household_content_hash_uniq
    ON photo (household_id, content_sha256)
    WHERE content_sha256 IS NOT NULL;`

const dropPhotoTable = `DROP TABLE IF EXISTS photo CASCADE;`

func (photoSchemaMigrator) Up(ctx context.Context, dsn string) error {
	runner, err := identitymigrate.New()
	if err != nil {
		return fmt.Errorf("identity migrate.New: %w", err)
	}
	if err := runner.Up(ctx, dsn); err != nil {
		return fmt.Errorf("apply identity schema: %w", err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, createPhotoTable); err != nil {
		return fmt.Errorf("create photo table: %w", err)
	}
	return nil
}

func (photoSchemaMigrator) Reset(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if _, err := conn.Exec(ctx, dropPhotoTable); err != nil {
		_ = conn.Close(ctx)
		return fmt.Errorf("drop photo table: %w", err)
	}
	_ = conn.Close(ctx)

	runner, err := identitymigrate.New()
	if err != nil {
		return fmt.Errorf("identity migrate.New: %w", err)
	}
	if err := runner.Reset(ctx, dsn); err != nil {
		return fmt.Errorf("reset identity schema: %w", err)
	}
	return nil
}

// newTestPool returns a pool against this package's own derived database,
// freshly reset and migrated onto the canonical photo table shape (plus
// identity, for the FKs).
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return harness.NewIsolatedPool(t, "media_adapter")
}

// testCtx returns a per-call context bounded so a slow/unresponsive
// database fails the test rather than hanging it.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
