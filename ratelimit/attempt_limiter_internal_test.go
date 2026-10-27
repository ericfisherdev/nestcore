package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

// These tests inspect the tracked-key map directly: its size is not part of
// the public surface, but bounding it is the reason the cap exists.

const (
	boundedKeys   = 50
	boundedWindow = 15 * time.Minute
)

func newBoundedLimiter() *AttemptLimiter {
	return NewAttemptLimiter(Policy{Threshold: 2, LockoutWindow: boundedWindow, MaxTrackedKeys: boundedKeys})
}

func distinctKey(i int) string { return fmt.Sprintf("email:user-%d@example.com", i) }

func TestAttemptLimiter_FloodOfDistinctKeysNeverExceedsTheCap(t *testing.T) {
	l := newBoundedLimiter()
	now := time.Now()

	for i := range boundedKeys + 1 {
		// Distinct timestamps so every entry is live and the sweep frees
		// nothing: the cap must hold by eviction alone.
		l.RecordFailure(distinctKey(i), now.Add(time.Duration(i)*time.Second))
		if got := len(l.state); got > boundedKeys {
			t.Fatalf("tracked keys = %d after %d inserts, want at most %d", got, i+1, boundedKeys)
		}
	}
	if _, kept := l.state[distinctKey(boundedKeys)]; !kept {
		t.Error("the newest key must be tracked after the flood")
	}
	if _, kept := l.state[distinctKey(0)]; kept {
		t.Error("the least recently active key should have been evicted to make room")
	}
}

func TestAttemptLimiter_SweepFreesAgedOutKeys(t *testing.T) {
	l := newBoundedLimiter()
	start := time.Now()
	for i := range boundedKeys {
		l.RecordFailure(distinctKey(i), start)
	}

	l.RecordFailure("email:newcomer@example.com", start.Add(boundedWindow+time.Minute))

	if got := len(l.state); got != 1 {
		t.Errorf("tracked keys once every filler has aged out = %d, want 1 (only the newcomer)", got)
	}
}

// TestAttemptLimiter_SweepEvictsOnlyStaleEntries: the sweep at the cap removes
// the entry whose activity aged out and leaves fresher ones, including a live
// lockout, untouched.
func TestAttemptLimiter_SweepEvictsOnlyStaleEntries(t *testing.T) {
	l := newBoundedLimiter()
	now := time.Now()
	const stale, recent = "email:stale@example.com", "email:recent@example.com"

	l.RecordFailure(stale, now)
	later := now.Add(boundedWindow + time.Minute)
	for i := range boundedKeys - 1 {
		l.RecordFailure(distinctKey(i), later)
	}
	l.RecordFailure(recent, later)

	if _, kept := l.state[stale]; kept {
		t.Error("a stale (aged-out) entry survived the sweep")
	}
	if _, kept := l.state[recent]; !kept {
		t.Error("the freshly inserted entry that triggered the sweep was itself evicted")
	}
	if got := len(l.state); got != boundedKeys {
		t.Errorf("tracked keys after the sweep = %d, want %d", got, boundedKeys)
	}
}
