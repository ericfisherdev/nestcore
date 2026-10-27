package ratelimit

import (
	"container/list"
	"sync"
	"time"
)

// Policy tunes an [AttemptLimiter]. Every field must be positive;
// [NewAttemptLimiter] panics otherwise.
type Policy struct {
	// Threshold is the number of attempts admitted per key per window. The
	// Threshold-th admitted attempt engages the lockout, so a Threshold of 5
	// admits five attempts and denies the sixth.
	Threshold int
	// LockoutWindow is both how long a key stays locked once it uses up its
	// Threshold and how long attempts are remembered: a key idle for a full
	// window starts counting from zero again, so the budget means "N
	// attempts within a window", not "N attempts ever".
	LockoutWindow time.Duration
	// MaxTrackedKeys bounds the number of distinct keys held in memory. Keys
	// are attacker-supplied, so an unbounded map is a memory-exhaustion
	// vector. At the cap, a new key first displaces the least recently active
	// key that is NOT locked. A lockout is never evicted: when every tracked
	// key is locked, new keys are denied (fail closed) until one expires.
	MaxTrackedKeys int
}

// Outcome is the verdict of [AttemptLimiter.Attempt]. The zero value is
// [Denied], so an unset Outcome fails closed.
type Outcome int

const (
	// Denied means the attempt must not proceed: the key is locked out, or
	// the limiter is saturated with lockouts and cannot track a new key.
	Denied Outcome = iota
	// Allowed means the attempt may proceed.
	Allowed
	// AllowedLastAttempt means the attempt may proceed but it used up the
	// key's budget and engaged the lockout. It is returned exactly once per
	// lockout, so callers can log one line per lockout.
	AllowedLastAttempt
)

// Permitted reports whether the attempt may proceed.
func (o Outcome) Permitted() bool { return o != Denied }

// entry is one key's in-memory state. It sits in exactly one of the
// limiter's two recency lists, described on [AttemptLimiter].
type entry struct {
	key          string
	attempts     int
	locked       bool
	lockedUntil  time.Time
	lastActivity time.Time
	element      *list.Element
}

// AttemptLimiter admits a bounded number of attempts per key per window and
// locks a key out once it has used its budget. It is safe for concurrent use.
// Construct one with [NewAttemptLimiter]; the zero value is not usable.
//
// [AttemptLimiter.Attempt] checks the lockout and counts the attempt under one
// lock, so concurrent requests cannot all slip past a check made before any of
// them is counted. A caller that wants only failures to count reports a
// successful attempt back with [AttemptLimiter.Reset] or
// [AttemptLimiter.Refund].
//
// Entries are kept in two lists, each ordered by recency of activity: keys
// still collecting attempts, and keys that are locked. A lockout ends exactly
// one window after the attempt that engaged it, so each list expires from its
// back and every operation is amortised O(1) however many keys are tracked.
// Callers must pass non-decreasing times.
//
// # Threat model
//
// Keys are attacker-supplied, so the table is capped, and a full table forces
// a trade between two attacks: evicting lockouts would let a flood of distinct
// keys erase a victim's lockout, while refusing new keys would let the same
// flood deny every legitimate user. The settled design is: lockouts are never
// evicted; a slot is made by dropping the least recently active counting key
// (one that has not yet used its budget); and only when every slot holds a
// live lockout are new keys denied.
//
// The residual risk is that a flood can erase another source's in-progress
// strikes, or, if it fills every slot with lockouts, deny new keys until one
// expires. Both need on the order of MaxTrackedKeys distinct keys within one
// window. That is only affordable when the flood is not itself throttled, so
// callers must pair a per-key limiter (an account) with a per-client one (an
// IP) and consult the client limiter FIRST: one source then locks out after
// its own Threshold and cannot mint distinct account keys faster than that.
// Filling the table then takes roughly MaxTrackedKeys distinct client
// addresses. Size MaxTrackedKeys generously: an entry costs about 250 bytes
// (key string, entry, list element and map slot), so 50,000 keys is about
// 12 MB.
type AttemptLimiter struct {
	policy Policy

	mu       sync.Mutex
	entries  map[string]*entry
	counting *list.List // unlocked entries, most recently active first
	locks    *list.List // locked entries, most recently locked first
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
	return &AttemptLimiter{
		policy:   p,
		entries:  make(map[string]*entry),
		counting: list.New(),
		locks:    list.New(),
	}
}

// Locked reports, without counting anything, whether an attempt for key would
// be denied as of now. It is a cheap pre-check; [AttemptLimiter.Attempt] is the
// authoritative gate. A key the limiter has no room to track is reported
// locked, matching what Attempt would do.
func (l *AttemptLimiter) Locked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireLocked(now)
	e, tracked := l.entries[key]
	if !tracked {
		return l.saturatedLocked()
	}
	return e.locked && now.Before(e.lockedUntil)
}

// Attempt atomically checks key's lockout and, unless denied, counts one
// attempt against its budget.
//
// A denied attempt is not counted, so retrying a locked key does not extend
// its lockout. The Threshold-th admitted attempt returns
// [AllowedLastAttempt] and locks the key for Policy.LockoutWindow.
func (l *AttemptLimiter) Attempt(key string, now time.Time) Outcome {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireLocked(now)

	e, tracked := l.entries[key]
	switch {
	case !tracked:
		if !l.makeRoomLocked() {
			return Denied
		}
		e = &entry{key: key}
		e.element = l.counting.PushFront(e)
		l.entries[key] = e
	case e.locked && now.Before(e.lockedUntil):
		return Denied
	}

	e.attempts++
	e.lastActivity = now
	if e.attempts < l.policy.Threshold {
		l.counting.MoveToFront(e.element)
		return Allowed
	}
	l.lockLocked(e, now)
	return AllowedLastAttempt
}

// Refund gives back one attempt previously admitted for key, for an attempt
// that should not have counted (an internal error, a request rejected by a
// second limiter). It only ever lowers the count of a key that is still
// counting, forgetting it at zero. A locked key is left alone: an anonymous
// refund cannot tell whether it belongs to the attempt that engaged the
// lockout or to an unrelated earlier one, and lifting a lockout on the
// strength of the latter would let any in-flight success undo an attacker's
// strikes. The lockout stands until it expires or [AttemptLimiter.Reset].
func (l *AttemptLimiter) Refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, tracked := l.entries[key]
	if !tracked || e.locked || e.attempts == 0 {
		return
	}
	e.attempts--
	if e.attempts == 0 {
		l.removeLocked(e)
	}
}

// Reset forgets key entirely, lockout included, e.g. after a correct password.
func (l *AttemptLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, tracked := l.entries[key]; tracked {
		l.removeLocked(e)
	}
}

// idleForAWindow reports whether e has seen no activity for a full window, so
// whatever it counted has aged out. Callers must hold l.mu.
func (l *AttemptLimiter) idleForAWindow(e *entry, now time.Time) bool {
	return !e.lastActivity.After(now.Add(-l.policy.LockoutWindow))
}

// expireLocked drops entries whose window has fully elapsed, oldest first.
// Callers must hold l.mu.
func (l *AttemptLimiter) expireLocked(now time.Time) {
	for back := l.counting.Back(); back != nil; back = l.counting.Back() {
		e := back.Value.(*entry)
		if !l.idleForAWindow(e, now) {
			break
		}
		l.removeLocked(e)
	}
	for back := l.locks.Back(); back != nil; back = l.locks.Back() {
		e := back.Value.(*entry)
		if now.Before(e.lockedUntil) {
			break
		}
		l.removeLocked(e)
	}
}

// saturatedLocked reports whether the limiter is full of lockouts, leaving no
// entry that may be displaced for a new key. Callers must hold l.mu.
func (l *AttemptLimiter) saturatedLocked() bool {
	return len(l.entries) >= l.policy.MaxTrackedKeys && l.counting.Len() == 0
}

// makeRoomLocked guarantees space for one more key by displacing the least
// recently active unlocked key. It reports false, changing nothing, when every
// tracked key is locked. Callers must hold l.mu.
func (l *AttemptLimiter) makeRoomLocked() bool {
	if len(l.entries) < l.policy.MaxTrackedKeys {
		return true
	}
	back := l.counting.Back()
	if back == nil {
		return false
	}
	l.removeLocked(back.Value.(*entry))
	return true
}

// lockLocked moves e into the lockout list, locked until one window after now.
// Callers must hold l.mu.
func (l *AttemptLimiter) lockLocked(e *entry, now time.Time) {
	l.counting.Remove(e.element)
	e.locked = true
	e.lockedUntil = now.Add(l.policy.LockoutWindow)
	e.element = l.locks.PushFront(e)
}

// removeLocked deletes e from the map and whichever list holds it. Callers
// must hold l.mu.
func (l *AttemptLimiter) removeLocked(e *entry) {
	if e.locked {
		l.locks.Remove(e.element)
	} else {
		l.counting.Remove(e.element)
	}
	delete(l.entries, e.key)
}
