package ratelimit

import (
	"sync"
	"time"
)

// Policy tunes an [AttemptLimiter]. Every field must be positive;
// [NewAttemptLimiter] panics otherwise.
type Policy struct {
	// Threshold is the number of failures tolerated per key. The failure
	// after that (failure number Threshold+1) engages the lockout, so a
	// Threshold of 5 lets five wrong attempts through and locks on the
	// sixth.
	Threshold int
	// LockoutWindow is both how long a key stays locked once it crosses the
	// Threshold and how long strikes are remembered: a key idle for longer
	// than the window starts counting from zero again, so strikes mean "N
	// failures within a window", not "N failures ever".
	LockoutWindow time.Duration
	// MaxTrackedKeys bounds the number of distinct keys held in memory. Keys
	// are attacker-supplied, so an unbounded map is a memory-exhaustion
	// vector. When the cap is reached, inserting a new key first sweeps
	// entries idle for longer than LockoutWindow and, if the map is still
	// full, evicts the least recently active entry. A flood of MaxTrackedKeys
	// distinct live keys can therefore push out even a live lockout; pair a
	// per-key limiter with a per-client one so such a flood is itself capped.
	MaxTrackedKeys int
}

// attemptState is one key's in-memory strike state.
type attemptState struct {
	failures     int
	lockedUntil  time.Time
	lastActivity time.Time
}

// AttemptLimiter counts failures per key and locks a key out for
// Policy.LockoutWindow once it exceeds Policy.Threshold. It is safe for
// concurrent use. Construct one with [NewAttemptLimiter]; the zero value is
// not usable.
//
// Callers check [AttemptLimiter.Locked] BEFORE doing the guarded work, record
// the outcome with [AttemptLimiter.RecordFailure] or
// [AttemptLimiter.RecordSuccess], and pass the current time on every call.
type AttemptLimiter struct {
	policy Policy

	mu    sync.Mutex
	state map[string]*attemptState
}

// NewAttemptLimiter constructs an empty limiter enforcing p.
//
// It panics if p.Threshold < 1, p.LockoutWindow <= 0 or p.MaxTrackedKeys < 1:
// a limiter that silently never limits is worse than one that refuses to
// start.
func NewAttemptLimiter(p Policy) *AttemptLimiter {
	switch {
	case p.Threshold < 1:
		panic("ratelimit: Policy.Threshold must be at least 1")
	case p.LockoutWindow <= 0:
		panic("ratelimit: Policy.LockoutWindow must be positive")
	case p.MaxTrackedKeys < 1:
		panic("ratelimit: Policy.MaxTrackedKeys must be at least 1")
	}
	return &AttemptLimiter{policy: p, state: make(map[string]*attemptState)}
}

// Locked reports whether key is inside a lockout window as of now.
func (l *AttemptLimiter) Locked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.state[key]
	return ok && now.Before(st.lockedUntil)
}

// RecordFailure records one failure for key as of now and returns
// lockedOut=true exactly once per lockout: on the failure that crosses
// Policy.Threshold. Callers use it to log one line per lockout rather than
// one per rejected attempt.
//
// Strikes older than Policy.LockoutWindow, and strikes left over from a
// lockout that has since expired, are discarded before this failure is
// counted. A failure recorded while the key is already locked changes
// nothing: the lockout end is fixed when it engages.
func (l *AttemptLimiter) RecordFailure(key string, now time.Time) (lockedOut bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateForFailureLocked(key, now)
	if now.Before(st.lockedUntil) {
		return false
	}
	st.failures++
	st.lastActivity = now
	if st.failures != l.policy.Threshold+1 {
		return false
	}
	st.lockedUntil = now.Add(l.policy.LockoutWindow)
	return true
}

// RecordSuccess forgets key's strikes, e.g. after a correct password.
func (l *AttemptLimiter) RecordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.state, key)
}

// stateForFailureLocked returns key's state ready to count a failure at now:
// freshly inserted for a new key, reset when its strikes or lockout have aged
// out. Callers must hold l.mu.
func (l *AttemptLimiter) stateForFailureLocked(key string, now time.Time) *attemptState {
	st, ok := l.state[key]
	if !ok {
		l.makeRoomLocked(now)
		st = &attemptState{}
		l.state[key] = st
		return st
	}
	if l.expiredLocked(st, now) {
		*st = attemptState{}
	}
	return st
}

// expiredLocked reports whether st holds nothing worth keeping as of now:
// its lockout (if any) has ended and it has been idle for a full window.
func (l *AttemptLimiter) expiredLocked(st *attemptState, now time.Time) bool {
	if now.Before(st.lockedUntil) {
		return false
	}
	return !st.lastActivity.After(now.Add(-l.policy.LockoutWindow))
}

// makeRoomLocked guarantees space for one more key. It sweeps expired
// entries first so live lockouts survive, and only evicts the least recently
// active entry when every tracked key is still live. Callers must hold l.mu.
func (l *AttemptLimiter) makeRoomLocked(now time.Time) {
	if len(l.state) < l.policy.MaxTrackedKeys {
		return
	}
	for key, st := range l.state {
		if l.expiredLocked(st, now) {
			delete(l.state, key)
		}
	}
	if len(l.state) < l.policy.MaxTrackedKeys {
		return
	}
	l.evictLeastRecentlyActiveLocked()
}

// evictLeastRecentlyActiveLocked drops the entry with the oldest activity.
// Callers must hold l.mu.
func (l *AttemptLimiter) evictLeastRecentlyActiveLocked() {
	var oldestKey string
	var oldest time.Time
	found := false
	for key, st := range l.state {
		if !found || st.lastActivity.Before(oldest) {
			oldestKey, oldest, found = key, st.lastActivity, true
		}
	}
	if found {
		delete(l.state, oldestKey)
	}
}
