// Package ratelimit provides a keyed strike counter with a hard lockout
// window, for guarding credential-checking endpoints against brute force.
//
// An [AttemptLimiter] admits a bounded number of attempts per caller-supplied
// string key — an email, a client IP, any attacker-controlled value — and
// locks a key out for [Policy.LockoutWindow] once it has used its
// [Policy.Threshold]. It is a strike counter, not a token bucket: there is no
// refill rate, a key is either within its budget or locked out.
//
// # Atomic admission
//
// [AttemptLimiter.Attempt] checks the lockout and counts the attempt under one
// lock. Checking first and recording afterwards would let any number of
// concurrent requests pass the check before the first is counted. A caller
// that wants only failures to count gives a successful attempt back with
// [AttemptLimiter.Reset] (forget the key) or [AttemptLimiter.Refund] (return
// that one attempt, for keys a success must not clear).
//
// # Bounded memory
//
// The key space is attacker-supplied, so at most [Policy.MaxTrackedKeys] keys
// are held. A lockout is never evicted to make room: if every slot holds a
// lockout, new keys are denied until one expires. Bookkeeping is amortised
// O(1) per call, so a flood of distinct keys cannot make callers queue behind
// a scan of the whole table.
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
