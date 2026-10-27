// Package ratelimit provides a keyed strike counter with a hard lockout
// window, for guarding credential-checking endpoints against brute force.
//
// An [AttemptLimiter] counts failures per caller-supplied string key — an
// email, a client IP, any attacker-controlled value — and, once a key has
// failed more than its [Policy.Threshold] times within [Policy.LockoutWindow],
// reports it locked until the window passes. It is deliberately a strike
// counter, not a token bucket: there is no refill rate, a key is either
// within its budget or locked out. The key space is attacker-supplied, so
// the tracked-key map is bounded by [Policy.MaxTrackedKeys].
//
// # Process-lifetime state
//
// Strike state lives in memory and dies with the process. Two binaries that
// share one credential store each keep their own counters, so a shared
// credential gets at most Threshold attempts per window per application, and
// a restart clears every lockout. That is no worse than an attacker waiting
// out the window; persisting strikes in the shared identity schema is
// explicitly deferred.
//
// # Where it lives
//
// The package is generic on purpose. It knows nothing about HTTP or accounts,
// so the per-IP dimension (HTTP-shaped) and the per-account dimension
// (identity-shaped) are both expressed by the caller choosing keys, and the
// caller supplies the clock on every call.
package ratelimit
