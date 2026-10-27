package ratelimit_test

import (
	"testing"
	"time"

	"github.com/ericfisherdev/nestcore/ratelimit"
)

const (
	testThreshold = 5
	testWindow    = 15 * time.Minute
	testKey       = "email:alice@example.com"
)

func testPolicy() ratelimit.Policy {
	return ratelimit.Policy{Threshold: testThreshold, LockoutWindow: testWindow, MaxTrackedKeys: 100}
}

func newLimiter() *ratelimit.AttemptLimiter {
	return ratelimit.NewAttemptLimiter(testPolicy())
}

// recordFailures records n failures for key at ts and returns the LAST call's
// lockedOut result.
func recordFailures(l *ratelimit.AttemptLimiter, key string, ts time.Time, n int) (lastLockedOut bool) {
	for range n {
		lastLockedOut = l.RecordFailure(key, ts)
	}
	return lastLockedOut
}

// lockOut drives key into its first lockout at now, failing the test if the
// lockout did not take.
func lockOut(t *testing.T, l *ratelimit.AttemptLimiter, key string, now time.Time) {
	t.Helper()
	recordFailures(l, key, now, testThreshold+1)
	if !l.Locked(key, now) {
		t.Fatal("setup: expected key to be locked after crossing the threshold")
	}
}

func TestAttemptLimiter_AllowsUpToThreshold(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	for i := range testThreshold {
		if l.Locked(testKey, now) {
			t.Fatalf("locked after %d failures, want not locked until the (threshold+1)th", i)
		}
		if l.RecordFailure(testKey, now) {
			t.Fatalf("RecordFailure #%d reported lockedOut=true, want false (threshold not yet crossed)", i+1)
		}
	}
	if l.Locked(testKey, now) {
		t.Error("locked after exactly threshold failures, want still not locked")
	}
}

func TestAttemptLimiter_LocksOnThresholdPlusOne(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	recordFailures(l, testKey, now, testThreshold)
	if !l.RecordFailure(testKey, now) {
		t.Fatal("RecordFailure on the (threshold+1)th attempt reported lockedOut=false, want true")
	}
	if !l.Locked(testKey, now) {
		t.Error("key must be locked immediately after crossing the threshold")
	}
}

func TestAttemptLimiter_ReportsLockedOutExactlyOnce(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	recordFailures(l, testKey, now, testThreshold)
	if !l.RecordFailure(testKey, now) {
		t.Fatal("the crossing attempt must report lockedOut=true")
	}
	if l.RecordFailure(testKey, now) {
		t.Error("a SUBSEQUENT failure while already locked must not report lockedOut=true again")
	}
}

func TestAttemptLimiter_FailuresWhileLockedDoNotExtendTheLockout(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	lockOut(t, l, testKey, now)

	l.RecordFailure(testKey, now.Add(testWindow-time.Second))

	if l.Locked(testKey, now.Add(testWindow+time.Second)) {
		t.Error("a failure recorded during the lockout must not push the lockout end out")
	}
}

func TestAttemptLimiter_UnlocksAfterWindow(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	lockOut(t, l, testKey, now)

	if l.Locked(testKey, now.Add(testWindow+time.Second)) {
		t.Error("key must not be locked once the lockout window has elapsed")
	}
}

func TestAttemptLimiter_RecordSuccessClearsState(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	recordFailures(l, testKey, now, testThreshold-1)
	l.RecordSuccess(testKey)

	// A reset means a FULL fresh run: the prior near-threshold count must
	// not carry over.
	for i := range testThreshold {
		if l.RecordFailure(testKey, now) {
			t.Fatalf("RecordFailure #%d after a reset reported lockedOut=true too early", i+1)
		}
	}
}

func TestAttemptLimiter_RecordSuccessUnlocksAKey(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	lockOut(t, l, testKey, now)

	l.RecordSuccess(testKey)

	if l.Locked(testKey, now) {
		t.Error("RecordSuccess must clear a lockout")
	}
}

// TestAttemptLimiter_ExpiredLockoutResetsStrikeCount: a key that waits out a
// lockout must get a FULL fresh run of Threshold attempts before locking out
// again, neither relocking on the very next failure nor becoming permanently
// unlockable because its counter climbed past the exact crossing value.
func TestAttemptLimiter_ExpiredLockoutResetsStrikeCount(t *testing.T) {
	tests := []struct {
		name            string
		freshFailures   int
		wantFinalLocked bool
	}{
		{name: "a single fresh failure after expiry does not relock", freshFailures: 1, wantFinalLocked: false},
		{name: "exactly threshold fresh failures after expiry do not relock", freshFailures: testThreshold, wantFinalLocked: false},
		{name: "threshold+1 fresh failures after expiry relocks", freshFailures: testThreshold + 1, wantFinalLocked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newLimiter()
			now := time.Now()
			lockOut(t, l, testKey, now)

			afterWindow := now.Add(testWindow + time.Second)
			lastLockedOut := recordFailures(l, testKey, afterWindow, tt.freshFailures)

			if got := l.Locked(testKey, afterWindow); got != tt.wantFinalLocked {
				t.Errorf("Locked after %d fresh failures post-expiry = %v, want %v (strike count did not reset)", tt.freshFailures, got, tt.wantFinalLocked)
			}
			if tt.wantFinalLocked && !lastLockedOut {
				t.Error("expected the fresh failure that crossed the threshold to report lockedOut=true")
			}
		})
	}
}

// TestAttemptLimiter_StaleStrikesDecayAfterWindow: strikes are "N failures
// within a window". A shared kiosk IP must not turn a day of scattered typos
// into a lockout.
func TestAttemptLimiter_StaleStrikesDecayAfterWindow(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	recordFailures(l, testKey, now, testThreshold)
	later := now.Add(testWindow + time.Second)

	for i := range testThreshold {
		if l.RecordFailure(testKey, later) {
			t.Fatalf("failure #%d after the window reported lockedOut=true; stale strikes must have decayed", i+1)
		}
	}
	if l.Locked(testKey, later) {
		t.Error("key locked by strikes spread across two windows")
	}
}

func TestAttemptLimiter_StrikesInsideTheWindowAccumulate(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	recordFailures(l, testKey, now, testThreshold)
	if !l.RecordFailure(testKey, now.Add(testWindow-time.Second)) {
		t.Error("a failure inside the window must still count against earlier strikes")
	}
}

func TestAttemptLimiter_KeysAreIndependent(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	lockOut(t, l, testKey, now)

	if l.Locked("email:bob@example.com", now) {
		t.Error("an unrelated key must be unaffected by another key's lockout")
	}
}

func TestNewAttemptLimiter_InvalidPolicyPanics(t *testing.T) {
	valid := testPolicy()
	tests := []struct {
		name   string
		mutate func(*ratelimit.Policy)
	}{
		{name: "zero threshold", mutate: func(p *ratelimit.Policy) { p.Threshold = 0 }},
		{name: "negative threshold", mutate: func(p *ratelimit.Policy) { p.Threshold = -1 }},
		{name: "zero window", mutate: func(p *ratelimit.Policy) { p.LockoutWindow = 0 }},
		{name: "negative window", mutate: func(p *ratelimit.Policy) { p.LockoutWindow = -time.Second }},
		{name: "zero tracked keys", mutate: func(p *ratelimit.Policy) { p.MaxTrackedKeys = 0 }},
		{name: "negative tracked keys", mutate: func(p *ratelimit.Policy) { p.MaxTrackedKeys = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			tt.mutate(&p)
			defer func() {
				if recover() == nil {
					t.Error("NewAttemptLimiter did not panic on an invalid policy")
				}
			}()
			ratelimit.NewAttemptLimiter(p)
		})
	}
}
