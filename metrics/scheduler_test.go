package metrics_test

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ericfisherdev/nestcore/metrics"
)

// schedulerOne and schedulerTwo are the fixed test allowlist this file's
// tests build every PromTickRecorder with, replacing the old
// nestova-specific Scheduler* constants: those constants stayed in Nestova
// (its own scheduler names are domain-specific), so this package's own
// tests use their own throwaway names instead.
const (
	schedulerOne = metrics.SchedulerName("scheduler_one")
	schedulerTwo = metrics.SchedulerName("scheduler_two")
)

var testKnownSchedulers = []metrics.SchedulerName{schedulerOne, schedulerTwo}

func TestNewPromTickRecorderRegistersOnRegistry(t *testing.T) {
	reg := metrics.NewRegistry()
	r := metrics.NewPromTickRecorder(reg, testNamespace, testKnownSchedulers)
	r.ObserveTick(schedulerOne, time.Millisecond, nil)

	names := familyNames(t, reg)
	for _, want := range []string{
		testNamespace + "_scheduler_ticks_total",
		testNamespace + "_scheduler_tick_duration_seconds",
		testNamespace + "_scheduler_last_success_timestamp_seconds",
	} {
		if !names[want] {
			t.Errorf("%s not registered on the provided registry", want)
		}
	}
}

// TestNewPromTickRecorderNilRegistererPanics pins the platform convention of
// failing loudly at construction when a required dependency is missing.
func TestNewPromTickRecorderNilRegistererPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewPromTickRecorder(nil) did not panic")
		}
	}()
	metrics.NewPromTickRecorder(nil, testNamespace, testKnownSchedulers)
}

// TestNewPromTickRecorderEmptyNamespacePanics mirrors NewHTTPMetrics's own
// empty-namespace guard: an unprefixed scheduler metric would collide across
// applications sharing a scrape target.
func TestNewPromTickRecorderEmptyNamespacePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewPromTickRecorder with an empty namespace did not panic")
		}
	}()
	metrics.NewPromTickRecorder(metrics.NewRegistry(), "", testKnownSchedulers)
}

// TestObserveTickErrorCountsErrorAndKeepsLastSuccess is the canonical
// failing-tick behaviour check: an error increments
// ticks_total{result="error"}, observes the duration, and does NOT move the
// last-success timestamp — neither on a first-ever failure (no series is
// created) nor on a failure that follows a success (the recorded timestamp
// stays put).
func TestObserveTickErrorCountsErrorAndKeepsLastSuccess(t *testing.T) {
	r := metrics.NewPromTickRecorder(metrics.NewRegistry(), testNamespace, testKnownSchedulers)

	r.ObserveTick(schedulerOne, 50*time.Millisecond, errors.New("db down"))

	if got := testutil.ToFloat64(r.TicksTotal.WithLabelValues(string(schedulerOne), "error")); got != 1 {
		t.Errorf(`ticks_total{result="error"} = %v, want 1`, got)
	}
	if got := testutil.ToFloat64(r.TicksTotal.WithLabelValues(string(schedulerOne), "success")); got != 0 {
		t.Errorf(`ticks_total{result="success"} = %v, want 0`, got)
	}
	if got := testutil.CollectAndCount(r.TickDuration); got != 1 {
		t.Errorf("tick_duration series count = %d, want 1", got)
	}
	// A first-ever failing tick must not create a last-success series.
	if got := testutil.CollectAndCount(r.LastSuccess); got != 0 {
		t.Errorf("last_success series count after a failing tick = %d, want 0", got)
	}

	// A failure AFTER a prior success must leave the recorded timestamp
	// untouched — this catches an implementation that overwrites the gauge on
	// every tick regardless of outcome. Pin the gauge to a known value first so
	// the comparison cannot be fooled by two SetToCurrentTime calls landing on
	// the same clock reading.
	r.ObserveTick(schedulerOne, 50*time.Millisecond, nil)
	const pinnedLastSuccess = 12345.0
	r.LastSuccess.WithLabelValues(string(schedulerOne)).Set(pinnedLastSuccess)

	r.ObserveTick(schedulerOne, 50*time.Millisecond, errors.New("db down again"))

	if got := testutil.ToFloat64(r.LastSuccess.WithLabelValues(string(schedulerOne))); got != pinnedLastSuccess {
		t.Errorf("last_success after a failure following a success = %v, want unchanged %v", got, pinnedLastSuccess)
	}
	if got := testutil.ToFloat64(r.TicksTotal.WithLabelValues(string(schedulerOne), "error")); got != 2 {
		t.Errorf(`ticks_total{result="error"} after second failure = %v, want 2`, got)
	}
}

// TestObserveTickUnknownSchedulerCollapsesToOther verifies the cardinality
// guard: a SchedulerName outside the constructor's known allowlist must land
// in the fixed "other" series rather than minting a new label value.
func TestObserveTickUnknownSchedulerCollapsesToOther(t *testing.T) {
	r := metrics.NewPromTickRecorder(metrics.NewRegistry(), testNamespace, testKnownSchedulers)

	r.ObserveTick(metrics.SchedulerName("rogue"), time.Millisecond, nil)

	// Exactly one counter series must exist — the "other" one. Asserting the
	// series count (rather than reading a "rogue" child, which would itself
	// instantiate that series) proves no rogue label value was minted.
	if got := testutil.CollectAndCount(r.TicksTotal); got != 1 {
		t.Errorf("ticks_total series count = %d, want 1 (only the collapsed 'other' series)", got)
	}
	if got := testutil.ToFloat64(r.TicksTotal.WithLabelValues("other", "success")); got != 1 {
		t.Errorf(`ticks_total{scheduler="other",result="success"} = %v, want 1`, got)
	}
	if got := testutil.ToFloat64(r.LastSuccess.WithLabelValues("other")); got <= 0 {
		t.Errorf(`last_success{scheduler="other"} = %v, want a positive Unix timestamp`, got)
	}
}

// TestObserveTickKnownSchedulerDoesNotCollapse is the other half of the
// cardinality guard: a name IN the constructor's allowlist must keep its own
// label, not fall into "other".
func TestObserveTickKnownSchedulerDoesNotCollapse(t *testing.T) {
	r := metrics.NewPromTickRecorder(metrics.NewRegistry(), testNamespace, testKnownSchedulers)

	r.ObserveTick(schedulerTwo, time.Millisecond, nil)

	if got := testutil.ToFloat64(r.TicksTotal.WithLabelValues(string(schedulerTwo), "success")); got != 1 {
		t.Errorf(`ticks_total{scheduler=%q,result="success"} = %v, want 1`, schedulerTwo, got)
	}
	if got := testutil.ToFloat64(r.TicksTotal.WithLabelValues("other", "success")); got != 0 {
		t.Errorf(`ticks_total{scheduler="other",result="success"} = %v, want 0 (a known name must not collapse)`, got)
	}
}

// TestObserveTickSuccessCountsSuccessAndMovesLastSuccess verifies the success
// path: ticks_total{result="success"} increments and the last-success gauge is
// set to (approximately) now.
func TestObserveTickSuccessCountsSuccessAndMovesLastSuccess(t *testing.T) {
	r := metrics.NewPromTickRecorder(metrics.NewRegistry(), testNamespace, testKnownSchedulers)

	before := float64(time.Now().Add(-time.Second).Unix())
	r.ObserveTick(schedulerTwo, 50*time.Millisecond, nil)

	if got := testutil.ToFloat64(r.TicksTotal.WithLabelValues(string(schedulerTwo), "success")); got != 1 {
		t.Errorf(`ticks_total{result="success"} = %v, want 1`, got)
	}
	if got := testutil.ToFloat64(r.LastSuccess.WithLabelValues(string(schedulerTwo))); got < before {
		t.Errorf("last_success = %v, want a recent Unix timestamp (>= %v)", got, before)
	}
}
