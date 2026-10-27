package ratelimit_test

import (
	"sync"
	"sync/atomic"
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

// attempt makes n attempts for key at ts and returns the LAST outcome.
func attempt(l *ratelimit.AttemptLimiter, key string, ts time.Time, n int) (last ratelimit.Outcome) {
	for range n {
		last = l.Attempt(key, ts)
	}
	return last
}

// lockOut spends key's whole budget at now, failing the test if the lockout
// did not take.
func lockOut(t *testing.T, l *ratelimit.AttemptLimiter, key string, now time.Time) {
	t.Helper()
	attempt(l, key, now, testThreshold)
	if !l.Locked(key, now) {
		t.Fatal("setup: expected key to be locked after spending its budget")
	}
}

func TestAttemptLimiter_AdmitsUpToThresholdThenDenies(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	for i := 1; i < testThreshold; i++ {
		if got := l.Attempt(testKey, now); got != ratelimit.Allowed {
			t.Fatalf("attempt #%d = %v, want Allowed", i, got)
		}
	}
	if got := l.Attempt(testKey, now); got != ratelimit.AllowedLastAttempt {
		t.Fatalf("attempt #%d = %v, want AllowedLastAttempt", testThreshold, got)
	}
	if got := l.Attempt(testKey, now); got != ratelimit.Denied {
		t.Errorf("attempt #%d = %v, want Denied", testThreshold+1, got)
	}
}

func TestAttemptLimiter_ReportsLastAttemptExactlyOnce(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	var last int
	for range testThreshold + 3 {
		if l.Attempt(testKey, now) == ratelimit.AllowedLastAttempt {
			last++
		}
	}
	if last != 1 {
		t.Errorf("AllowedLastAttempt returned %d times, want exactly once per lockout", last)
	}
}

func TestOutcome_ZeroValueFailsClosed(t *testing.T) {
	var o ratelimit.Outcome
	if o.Permitted() {
		t.Error("the zero Outcome must not be permitted")
	}
	if !ratelimit.Allowed.Permitted() || !ratelimit.AllowedLastAttempt.Permitted() {
		t.Error("Allowed and AllowedLastAttempt must be permitted")
	}
}

func TestAttemptLimiter_LockedPeeksWithoutCounting(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	for range testThreshold * 3 {
		if l.Locked(testKey, now) {
			t.Fatal("Locked must not consume the key's budget")
		}
	}
	if got := attempt(l, testKey, now, testThreshold); got != ratelimit.AllowedLastAttempt {
		t.Errorf("full budget still expected after peeking, last outcome = %v", got)
	}
	if !l.Locked(testKey, now) {
		t.Error("Locked must report the lockout once the budget is spent")
	}
}

func TestAttemptLimiter_DeniedAttemptsDoNotExtendTheLockout(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	lockOut(t, l, testKey, now)

	l.Attempt(testKey, now.Add(testWindow-time.Second))

	if got := l.Attempt(testKey, now.Add(testWindow+time.Second)); got != ratelimit.Allowed {
		t.Errorf("attempt after the original lockout end = %v, want Allowed", got)
	}
}

func TestAttemptLimiter_UnlocksAfterWindowWithAFreshBudget(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	lockOut(t, l, testKey, now)

	afterWindow := now.Add(testWindow + time.Second)
	if l.Locked(testKey, afterWindow) {
		t.Fatal("key must not be locked once the lockout window has elapsed")
	}
	for i := 1; i < testThreshold; i++ {
		if got := l.Attempt(testKey, afterWindow); got != ratelimit.Allowed {
			t.Fatalf("post-expiry attempt #%d = %v, want Allowed (budget did not reset)", i, got)
		}
	}
	if got := l.Attempt(testKey, afterWindow); got != ratelimit.AllowedLastAttempt {
		t.Errorf("post-expiry attempt #%d = %v, want AllowedLastAttempt", testThreshold, got)
	}
}

func TestAttemptLimiter_ResetClearsStrikesAndLockouts(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	attempt(l, testKey, now, testThreshold-1)
	l.Reset(testKey)
	if got := attempt(l, testKey, now, testThreshold-1); got != ratelimit.Allowed {
		t.Errorf("after Reset the prior count must not carry over, last outcome = %v", got)
	}

	lockOut(t, l, testKey, now)
	l.Reset(testKey)
	if l.Locked(testKey, now) {
		t.Error("Reset must clear a lockout")
	}
	l.Reset("never-seen") // must not panic
}

func TestAttemptLimiter_RefundGivesBackOneAttempt(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	attempt(l, testKey, now, testThreshold-1)
	l.Refund(testKey)

	// Four attempts less one refunded leaves three used, so one more is
	// Allowed and the next spends the budget.
	if got := l.Attempt(testKey, now); got != ratelimit.Allowed {
		t.Errorf("outcome after Refund = %v, want Allowed", got)
	}
	if got := l.Attempt(testKey, now); got != ratelimit.AllowedLastAttempt {
		t.Errorf("outcome = %v, want AllowedLastAttempt", got)
	}
}

// TestAttemptLimiter_RefundNeverLiftsALockout: an attempt admitted before the
// lockout engaged may be refunded after it (e.g. it succeeded). That must not
// undo the strikes that locked the key.
func TestAttemptLimiter_RefundNeverLiftsALockout(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	l.Attempt(testKey, now) // the legitimate attempt, later refunded
	attempt(l, testKey, now, testThreshold-1)
	if !l.Locked(testKey, now) {
		t.Fatal("setup: expected the key to be locked")
	}

	l.Refund(testKey)

	if !l.Locked(testKey, now) {
		t.Error("a refund must not lift a lockout")
	}
	if got := l.Attempt(testKey, now); got != ratelimit.Denied {
		t.Errorf("attempt after the refund = %v, want Denied", got)
	}
	l.Refund("never-seen") // must not panic
}

func TestAttemptLimiter_RefundToZeroForgetsTheKey(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	l.Attempt(testKey, now)
	l.Refund(testKey)
	l.Refund(testKey) // refunding past zero is a no-op

	if got := attempt(l, testKey, now, testThreshold); got != ratelimit.AllowedLastAttempt {
		t.Errorf("full budget expected after refunding everything, last outcome = %v", got)
	}
}

// TestAttemptLimiter_StaleAttemptsDecayAfterWindow: the budget is "N attempts
// within a window". A shared kiosk IP must not turn a day of scattered typos
// into a lockout.
func TestAttemptLimiter_StaleAttemptsDecayAfterWindow(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	attempt(l, testKey, now, testThreshold-1)
	later := now.Add(testWindow + time.Second)

	if got := attempt(l, testKey, later, testThreshold-1); got != ratelimit.Allowed {
		t.Errorf("last outcome after the window = %v, want Allowed (stale attempts must decay)", got)
	}
}

func TestAttemptLimiter_AttemptsInsideTheWindowAccumulate(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	attempt(l, testKey, now, testThreshold-1)
	if got := l.Attempt(testKey, now.Add(testWindow-time.Second)); got != ratelimit.AllowedLastAttempt {
		t.Errorf("attempt inside the window = %v, want it to count against earlier ones", got)
	}
}

func TestAttemptLimiter_KeysAreIndependent(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	lockOut(t, l, testKey, now)

	if l.Locked("email:bob@example.com", now) {
		t.Error("an unrelated key must be unaffected by another key's lockout")
	}
	if got := l.Attempt("email:bob@example.com", now); got != ratelimit.Allowed {
		t.Errorf("unrelated key attempt = %v, want Allowed", got)
	}
}

// TestAttemptLimiter_ConcurrentAttemptsNeverExceedTheBudget guards the
// check-then-act race: with the lockout check and the count under one lock,
// no matter how many goroutines race, exactly Threshold get through.
func TestAttemptLimiter_ConcurrentAttemptsNeverExceedTheBudget(t *testing.T) {
	const goroutines = 200
	l := newLimiter()
	now := time.Now()

	var permitted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if l.Attempt(testKey, now).Permitted() {
				permitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := permitted.Load(); got != testThreshold {
		t.Errorf("%d of %d concurrent attempts were permitted, want exactly %d", got, goroutines, testThreshold)
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

func TestAttemptLimiter_CancelOfTheEngagingAttemptLiftsTheLockout(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	attempt(l, testKey, now, testThreshold-1)
	outcome, engaging := l.Reserve(testKey, now)
	if outcome != ratelimit.AllowedLastAttempt {
		t.Fatalf("setup: outcome = %v, want AllowedLastAttempt", outcome)
	}

	l.Cancel(engaging)

	if l.Locked(testKey, now) {
		t.Error("cancelling the attempt that engaged the lockout must lift it")
	}
	if got := l.Attempt(testKey, now); got != ratelimit.AllowedLastAttempt {
		t.Errorf("attempt after the cancel = %v, want AllowedLastAttempt (one attempt given back)", got)
	}
}

// TestAttemptLimiter_CancelOfAnEarlierAttemptNeverLiftsALockout: an attempt
// admitted before the lockout engaged may be cancelled after it. That must not
// undo the strikes that locked the key.
func TestAttemptLimiter_CancelOfAnEarlierAttemptNeverLiftsALockout(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	_, earlier := l.Reserve(testKey, now)
	attempt(l, testKey, now, testThreshold-1)
	if !l.Locked(testKey, now) {
		t.Fatal("setup: expected the key to be locked")
	}

	l.Cancel(earlier)

	if !l.Locked(testKey, now) {
		t.Error("cancelling an attempt that did not engage the lockout must leave it standing")
	}
}

func TestAttemptLimiter_CancelledInternalErrorsNeverLockAKey(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	for range testThreshold * 4 {
		outcome, res := l.Reserve(testKey, now)
		if !outcome.Permitted() {
			t.Fatal("a key whose every attempt is cancelled must never lock")
		}
		l.Cancel(res)
	}

	if got := attempt(l, testKey, now, testThreshold); got != ratelimit.AllowedLastAttempt {
		t.Errorf("full budget expected after cancelling everything, last outcome = %v", got)
	}
}

func TestAttemptLimiter_CancelIgnoresStaleAndEmptyReservations(t *testing.T) {
	l := newLimiter()
	now := time.Now()

	_, old := l.Reserve(testKey, now)
	l.Reset(testKey)
	attempt(l, testKey, now, testThreshold-1) // a new entry, one attempt from locking

	l.Cancel(old)                     // from a reset entry
	l.Cancel(ratelimit.Reservation{}) // a denied attempt's zero value

	if got := l.Attempt(testKey, now); got != ratelimit.AllowedLastAttempt {
		t.Errorf("outcome = %v, want AllowedLastAttempt (stale cancels must change nothing)", got)
	}
}

func TestAttemptLimiter_DeniedAttemptReturnsAnEmptyReservation(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	lockOut(t, l, testKey, now)

	outcome, res := l.Reserve(testKey, now)
	l.Cancel(res)

	if outcome != ratelimit.Denied || !l.Locked(testKey, now) {
		t.Errorf("outcome = %v, locked = %v, want Denied and still locked", outcome, l.Locked(testKey, now))
	}
}

// TestAttemptLimiter_CancelIgnoresAReservationFromAnExpiredEntry: the lockout
// the reservation engaged has expired and the key started a fresh run; the old
// reservation must not shave an attempt off the new one.
func TestAttemptLimiter_CancelIgnoresAReservationFromAnExpiredEntry(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	attempt(l, testKey, now, testThreshold-1)
	_, engaging := l.Reserve(testKey, now)

	afterWindow := now.Add(testWindow + time.Second)
	l.Attempt(testKey, afterWindow) // the replacement entry's first attempt
	l.Cancel(engaging)

	for i := 2; i < testThreshold; i++ {
		if got := l.Attempt(testKey, afterWindow); got != ratelimit.Allowed {
			t.Fatalf("attempt #%d on the replacement entry = %v, want Allowed", i, got)
		}
	}
	if got := l.Attempt(testKey, afterWindow); got != ratelimit.AllowedLastAttempt {
		t.Errorf("attempt #%d on the replacement entry = %v, want AllowedLastAttempt (the stale cancel altered its count)", testThreshold, got)
	}
}
