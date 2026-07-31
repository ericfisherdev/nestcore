// This file lives in package cryptotest_test (a black-box test package), not
// cryptotest itself. That is deliberate: it compiles by importing cryptotest
// via its full module path exactly as Nestova or Nestorage would, proving the
// package is usable from outside — not merely from within its own package,
// where an accidental internal/ nesting could still compile.
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
