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
	epoch        uint64
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
// them is counted. A caller that wants a success to clear a key calls
// [AttemptLimiter.Reset]; a key a success must not clear keeps the attempt
// counted. An attempt that should never have counted (an internal error, a
// request rejected by a second limiter) is taken back with
// [AttemptLimiter.Cancel] using the [Reservation] from [AttemptLimiter.Reserve].
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

	mu        sync.Mutex
	entries   map[string]*entry
	nextEpoch uint64
	// latest is the newest time any call has presented. Cancel has no clock of
	// its own, so it uses this to expire entries and to stamp a key it returns
	// to the counting list, keeping that list ordered by activity.
	latest   time.Time
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
	l.observeLocked(now)
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
// [AllowedLastAttempt] and locks the key for Policy.LockoutWindow. Use
// [AttemptLimiter.Reserve] instead when the attempt may have to be cancelled.
func (l *AttemptLimiter) Attempt(key string, now time.Time) Outcome {
	outcome, _ := l.Reserve(key, now)
	return outcome
}

// Reservation identifies one admitted attempt so it can be taken back with
// [AttemptLimiter.Cancel]. The zero value (what a denied attempt returns)
// cancels nothing.
type Reservation struct {
	key     string
	epoch   uint64
	engaged bool
}

// Reserve is [AttemptLimiter.Attempt] that also returns a [Reservation] for
// the attempt it admitted.
func (l *AttemptLimiter) Reserve(key string, now time.Time) (Outcome, Reservation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.observeLocked(now)

	e, tracked := l.entries[key]
	switch {
	case !tracked:
		if !l.makeRoomLocked() {
			return Denied, Reservation{}
		}
		l.nextEpoch++
		e = &entry{key: key, epoch: l.nextEpoch}
		e.element = l.counting.PushFront(e)
		l.entries[key] = e
	case e.locked && now.Before(e.lockedUntil):
		return Denied, Reservation{}
	}

	e.attempts++
	e.lastActivity = now
	if e.attempts < l.policy.Threshold {
		l.counting.MoveToFront(e.element)
		return Allowed, Reservation{key: key, epoch: e.epoch}
	}
	l.lockLocked(e, now)
	return AllowedLastAttempt, Reservation{key: key, epoch: e.epoch, engaged: true}
}

// Cancel takes back the attempt behind r, for an attempt that should never
// have counted (an internal error, a request rejected by a second limiter).
// Call it at most once per reservation.
//
// It is exact about which attempt it cancels. Every cancelled attempt is given
// back, so concurrent cancels never leave strikes behind. But a lockout is
// lifted only when r is the attempt that engaged it; if a different attempt
// engaged it, it stands, so an unrelated in-flight attempt can never undo an
// attacker's strikes. A reservation that outlived its entry (reset, expired or
// evicted since) cancels nothing.
func (l *AttemptLimiter) Cancel(r Reservation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireLocked(l.latest)
	e, tracked := l.entries[r.key]
	if !tracked || r.epoch == 0 || e.epoch != r.epoch {
		return
	}
	e.attempts--
	switch {
	case e.locked && !r.engaged:
		// The engaging attempt is still counted, so the lockout stands.
	case e.attempts == 0:
		l.removeLocked(e)
	case e.locked:
		l.unlockLocked(e)
	}
}

// Refund gives back one attempt previously admitted for key, for an attempt
// that should not have counted (an internal error, a request rejected by a
// second limiter). It only ever lowers the count of a key that is still
// counting, forgetting it at zero. A locked key is left alone: an anonymous
// refund cannot tell whether it belongs to the attempt that engaged the
// lockout or to an unrelated earlier one, and lifting a lockout on the
// strength of the latter would let any in-flight success undo an attacker's
// strikes. The lockout stands until it expires or [AttemptLimiter.Reset].
//
// Deprecated: Refund cannot say which attempt it returns, so it cannot undo an
// attempt that engaged a lockout. Use [AttemptLimiter.Reserve] and
// [AttemptLimiter.Cancel].
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

// unlockLocked returns e to the counting list, lifting its lockout. It is
// stamped with the newest time seen so that pushing it to the front keeps the
// list ordered by activity. Callers must hold l.mu.
func (l *AttemptLimiter) unlockLocked(e *entry) {
	l.locks.Remove(e.element)
	e.locked = false
	e.lockedUntil = time.Time{}
	e.lastActivity = l.latest
	e.element = l.counting.PushFront(e)
}

// observeLocked records now as the newest time seen and expires what it has
// aged out. Callers must hold l.mu.
func (l *AttemptLimiter) observeLocked(now time.Time) {
	if now.After(l.latest) {
		l.latest = now
	}
	l.expireLocked(now)
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
