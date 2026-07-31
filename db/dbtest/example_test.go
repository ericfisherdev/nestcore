// This file lives in package dbtest_test (a black-box test package), not
// dbtest itself. That is deliberate: it exercises dbtest through its
// exported API alone, so a helper that quietly leaned on an unexported
// identifier of its own package would fail to compile here. It doubles as
// the package's godoc example. Note it cannot prove importability from
// outside the module: Go's internal rule admits any importer under
// internal/'s parent, so this file would still compile if the package were
// nested under internal/. Cross-module importability is proven by
// Nestorage, which imports this path from its own test suites. This example
// needs no live database: New only wires an env var name and a Migrator
// together, and the migrator here is a no-op stand-in.
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
// application's environment variable and migrator. Each gated test package
// then calls harness.NewIsolatedPool(t, "<suffix>") from its own tests —
// which a runnable example cannot show, having no *testing.T.
func Example() {
	harness := dbtest.New("NESTOVA_TEST_DATABASE_URL", noopMigrator{})
	fmt.Printf("%T\n", harness)
	// Output: *dbtest.Harness
}
