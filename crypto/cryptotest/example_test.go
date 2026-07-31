// This file lives in package cryptotest_test (a black-box test package), not
// cryptotest itself. That is deliberate: it exercises cryptotest through its
// exported API alone, so a helper that quietly leaned on an unexported
// identifier of its own package would fail to compile here. It doubles as
// the package's godoc example. Note it cannot prove importability from
// outside the module: Go's internal rule admits any importer under
// internal/'s parent, so this file would still compile if the package were
// nested under internal/. Cross-module importability is proven by
// Nestorage, which imports this path from its own test suites.
package cryptotest_test

import (
	"fmt"

	"github.com/ericfisherdev/nestcore/crypto"
	"github.com/ericfisherdev/nestcore/crypto/cryptotest"
)

// Example demonstrates a consuming application's test suite hashing and
// verifying a password with cryptotest's cheap, non-production cost
// parameters, exercised entirely through crypto's public API.
func Example() {
	hasher := cryptotest.Hasher()

	encoded, err := hasher.Hash("correct-horse-battery-staple")
	if err != nil {
		fmt.Println("hash error:", err)
		return
	}

	ok, err := crypto.Verify("correct-horse-battery-staple", encoded)
	if err != nil {
		fmt.Println("verify error:", err)
		return
	}
	fmt.Println(ok)
	// Output: true
}
