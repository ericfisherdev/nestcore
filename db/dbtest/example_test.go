// This file lives in package dbtest_test (a black-box test package), not
// dbtest itself. That is deliberate: it compiles by importing dbtest via its
// full module path exactly as Nestova or Nestorage's own gated test helpers
// would, proving the package is usable from outside — not merely from
// within its own package, where an accidental internal/ nesting could still
// compile. It needs no live database: New only wires an env var name and a
// Migrator together, and the migrator here is a no-op stand-in.
package dbtest_test

import (
	"context"
	"fmt"

	"github.com/ericfisherdev/nestcore/db/dbtest"
)

// noopMigrator satisfies dbtest.Migrator without touching a real database,
// standing in for an application's embedded-goose migrator in this example.
type noopMigrator struct{}

func (noopMigrator) Reset(context.Context, string) error { return nil }
func (noopMigrator) Up(context.Context, string) error    { return nil }

// Example demonstrates a consuming application wiring its own gated-test
// harness from dbtest's public API: one Harness, built once with the
// application's environment variable and migrator, then shared across that
// application's gated test packages via NewIsolatedPool.
func Example() {
	harness := dbtest.New("NESTOVA_TEST_DATABASE_URL", noopMigrator{})
	fmt.Println(harness != nil)
	// Output: true
}
