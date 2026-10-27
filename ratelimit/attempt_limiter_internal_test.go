package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

// These tests inspect the tracked-key bookkeeping directly: its size is not
// part of the public surface, but bounding it is the reason the cap exists.

const (
	boundedKeys   = 50
	boundedWindow = 15 * time.Minute
)

// newBoundedLimiter admits two attempts per key, so the second locks it.
func newBoundedLimiter() *AttemptLimiter {
	return NewAttemptLimiter(Policy{Threshold: 2, LockoutWindow: boundedWindow, MaxTrackedKeys: boundedKeys})
}

func distinctKey(i int) string { return fmt.Sprintf("email:user-%d@example.com", i) }

func lockKey(l *AttemptLimiter, key string, now time.Time) {
	l.Attempt(key, now)
	l.Attempt(key, now)
}

func assertConsistent(t *testing.T, l *AttemptLimiter) {
	t.Helper()
	if got := l.counting.Len() + l.locks.Len(); got != len(l.entries) {
		t.Fatalf("lists hold %d entries but the map holds %d", got, len(l.entries))
	}
	if len(l.entries) > boundedKeys {
		t.Fatalf("tracked keys = %d, want at most %d", len(l.entries), boundedKeys)
	}
}

func TestAttemptLimiter_FloodOfDistinctKeysNeverExceedsTheCap(t *testing.T) {
	l := newBoundedLimiter()
	now := time.Now()

	for i := range boundedKeys * 3 {
		// Distinct timestamps keep every entry live, so the cap must hold by
		// eviction alone.
		l.Attempt(distinctKey(i), now.Add(time.Duration(i)*time.Millisecond))
		assertConsistent(t, l)
	}
	if _, kept := l.entries[distinctKey(boundedKeys*3-1)]; !kept {
		t.Error("the newest key must be tracked after the flood")
	}
	if _, kept := l.entries[distinctKey(0)]; kept {
		t.Error("the least recently active key should have been evicted to make room")
	}
}

// TestAttemptLimiter_FloodCannotEraseALockout is the eviction-bypass guard: an
// attacker spraying distinct keys must not be able to push a victim's lockout
// out of the map.
func TestAttemptLimiter_FloodCannotEraseALockout(t *testing.T) {
	l := newBoundedLimiter()
	now := time.Now()
	const victim = "email:victim@example.com"
	lockKey(l, victim, now)

	for i := range boundedKeys * 3 {
		l.Attempt(distinctKey(i), now.Add(time.Duration(i+1)*time.Millisecond))
	}

	if !l.Locked(victim, now.Add(time.Second)) {
		t.Error("a flood of distinct keys erased the victim's lockout")
	}
	if got := l.Attempt(victim, now.Add(time.Second)); got != Denied {
		t.Errorf("victim attempt after the flood = %v, want Denied", got)
	}
	assertConsistent(t, l)
}

func TestAttemptLimiter_SaturatedWithLockoutsFailsClosed(t *testing.T) {
	l := newBoundedLimiter()
	now := time.Now()
	for i := range boundedKeys {
		lockKey(l, distinctKey(i), now)
	}

	const newcomer = "email:newcomer@example.com"
	if !l.Locked(newcomer, now) {
		t.Error("an untrackable key must be reported locked when every slot holds a lockout")
	}
	if got := l.Attempt(newcomer, now); got != Denied {
		t.Errorf("newcomer attempt = %v, want Denied", got)
	}
	for i := range boundedKeys {
		if !l.Locked(distinctKey(i), now) {
			t.Fatalf("lockout %d was dropped to admit a newcomer", i)
		}
	}
	assertConsistent(t, l)

	if got := l.Attempt(newcomer, now.Add(boundedWindow+time.Second)); got != Allowed {
		t.Errorf("newcomer after the lockouts expired = %v, want Allowed", got)
	}
}

func TestAttemptLimiter_ExpiredKeysAreSwept(t *testing.T) {
	l := newBoundedLimiter()
	start := time.Now()
	for i := range boundedKeys / 2 {
		l.Attempt(distinctKey(i), start)
		lockKey(l, distinctKey(i+boundedKeys), start)
	}

	l.Attempt("email:newcomer@example.com", start.Add(boundedWindow+time.Minute))

	if got := len(l.entries); got != 1 {
		t.Errorf("tracked keys once everything has aged out = %d, want 1 (only the newcomer)", got)
	}
	assertConsistent(t, l)
}

// TestAttemptLimiter_EvictionSparesLiveLockoutsAndTakesTheOldestCounter: at the
// cap, the displaced key is the least recently active one that is not locked.
func TestAttemptLimiter_EvictionSparesLiveLockoutsAndTakesTheOldestCounter(t *testing.T) {
	l := newBoundedLimiter()
	now := time.Now()
	const locked, oldest, newer = "email:locked@example.com", "email:oldest@example.com", "email:newer@example.com"

	lockKey(l, locked, now)
	l.Attempt(oldest, now.Add(time.Millisecond))
	l.Attempt(newer, now.Add(2*time.Millisecond))
	for i := range boundedKeys - 3 {
		l.Attempt(distinctKey(i), now.Add(time.Duration(i+3)*time.Millisecond))
	}
	l.Attempt("email:trigger@example.com", now.Add(time.Second))

	if _, kept := l.entries[locked]; !kept {
		t.Error("the locked key was evicted")
	}
	if _, kept := l.entries[oldest]; kept {
		t.Error("the least recently active unlocked key should have been displaced")
	}
	if _, kept := l.entries[newer]; !kept {
		t.Error("a more recently active key was displaced instead of the oldest")
	}
	assertConsistent(t, l)
}

func TestAttemptLimiter_RefundToZeroLeavesNoEntry(t *testing.T) {
	l := newBoundedLimiter()
	l.Attempt("email:a@example.com", time.Now())
	l.Refund("email:a@example.com")

	if len(l.entries) != 0 || l.counting.Len() != 0 {
		t.Errorf("entries = %d, counting = %d after refunding the only attempt, want 0", len(l.entries), l.counting.Len())
	}
}

// TestAttemptLimiter_CancelIgnoresAReservationFromAnEvictedEntry: the key was
// pushed out of the table and later re-admitted as a new entry; the old
// reservation must leave the replacement alone.
func TestAttemptLimiter_CancelIgnoresAReservationFromAnEvictedEntry(t *testing.T) {
	l := newBoundedLimiter()
	now := time.Now()
	const key = "email:evicted@example.com"
	_, old := l.Reserve(key, now)

	for i := range boundedKeys {
		l.Attempt(distinctKey(i), now.Add(time.Duration(i+1)*time.Millisecond))
	}
	if _, kept := l.entries[key]; kept {
		t.Fatal("setup: the key should have been evicted")
	}
	l.Attempt(key, now.Add(time.Second))

	l.Cancel(old)

	if got := l.entries[key].attempts; got != 1 {
		t.Errorf("replacement entry attempts = %d, want 1 (the stale cancel must change nothing)", got)
	}
}
