// package adapter_test (an external test package, not package adapter) so
// these gated tests can depend on nestcore/db/dbtest and
// nestcore/identity/migrate without tangling the adapter package's own
// import graph.
package adapter_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ncdbtest "github.com/ericfisherdev/nestcore/db/dbtest"
	ncmigrate "github.com/ericfisherdev/nestcore/db/migrate"
	"github.com/ericfisherdev/nestcore/identity/migrate"
)

// harness derives isolated test databases from NESTCORE_TEST_DATABASE_URL,
// the same base identity/migrate's own gated tests use.
var harness = newHarness()

func newHarness() *ncdbtest.Harness {
	runner, err := migrate.New()
	if err != nil {
		// The embedded migration set is fixed at build time, so a failure
		// here means the embed itself is broken — a programming error,
		// not a runtime condition any caller of harness could recover
		// from.
		panic(fmt.Sprintf("identity/migrate: %v", err))
	}
	return ncdbtest.New("NESTCORE_TEST_DATABASE_URL", runnerMigrator{runner})
}

// runnerMigrator adapts *ncmigrate.Runner to nestcore/db/dbtest's
// Migrator interface: the Runner's own Reset/Up take a trailing
// opts ...ncmigrate.Option, and Go requires an exact method signature to
// satisfy an interface, so the variadic cannot be dropped by assignment.
type runnerMigrator struct {
	runner *ncmigrate.Runner
}

func (m runnerMigrator) Reset(ctx context.Context, dsn string) error {
	return m.runner.Reset(ctx, dsn)
}

func (m runnerMigrator) Up(ctx context.Context, dsn string) error {
	return m.runner.Up(ctx, dsn)
}

// newTestPool returns a pool against this package's own derived database,
// freshly reset and migrated onto the identity schema.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return harness.NewIsolatedPool(t, "identity_adapter")
}

// testCtx returns a per-call context bounded so a slow/unresponsive
// database fails the test rather than hanging it.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
