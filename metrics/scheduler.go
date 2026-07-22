package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SchedulerName identifies a background scheduler in the tick metrics'
// scheduler label. It is a distinct type (not a bare string) so callers must
// deliberately mint a value rather than accidentally passing arbitrary text;
// combined with PromTickRecorder collapsing names outside its constructor's
// allowlist to "other", the label's cardinality stays bounded even against
// misuse.
type SchedulerName string

// schedulerOther is the collapsed scheduler label value for names outside
// PromTickRecorder's known allowlist. Prometheus label values become time
// series, so an unchecked caller-supplied name would let a bug (or a future
// careless caller) mint unbounded series; collapsing to one fixed value caps
// the damage while still making the misuse visible on the dashboard.
const schedulerOther = "other"

// TickRecorder is the minimal port (ISP) a background scheduler records one
// completed poll cycle through: how long the cycle took and whether it
// failed. name should be one of the caller's own canonical scheduler names
// so the label set stays bounded; implementations collapse anything outside
// their known allowlist to a fixed fallback value. Implementations must be
// safe for concurrent use — a caller may run each scheduler in its own
// goroutine.
type TickRecorder interface {
	ObserveTick(name SchedulerName, d time.Duration, err error)
}

// Values for the tick counter's result label.
const (
	tickResultSuccess = "success"
	tickResultError   = "error"
)

// PromTickRecorder is the Prometheus-backed TickRecorder. The fields are
// exported so tests can assert on them with prometheus/testutil, but
// construction always goes through NewPromTickRecorder so every instance is
// registered.
type PromTickRecorder struct {
	// TicksTotal counts completed scheduler cycles, labelled by scheduler name
	// and result ("success" or "error").
	TicksTotal *prometheus.CounterVec
	// TickDuration observes cycle duration in seconds, labelled by scheduler
	// name. Result is intentionally omitted to bound the histogram's series
	// count (each series carries a full bucket set).
	TickDuration *prometheus.HistogramVec
	// LastSuccess gauges the Unix timestamp of each scheduler's most recent
	// successful cycle; a failing cycle leaves it untouched, so a stale value
	// signals a scheduler that has stopped succeeding.
	LastSuccess *prometheus.GaugeVec

	// known is the constructor-supplied allowlist ObserveTick consults to
	// decide whether a name keeps its own label or collapses to "other".
	known map[SchedulerName]struct{}
}

// Compile-time check that the Prometheus recorder satisfies the port.
var _ TickRecorder = (*PromTickRecorder)(nil)

// NewPromTickRecorder constructs the scheduler tick metrics and registers
// them on reg, with every metric name prefixed by namespace (see
// NewHTTPMetrics for the namespacing mechanics) so more than one application
// can share a scrape target without their scheduler metrics colliding.
//
// known is the caller's allowlist of canonical scheduler names; ObserveTick
// collapses any name outside it to the fixed "other" label so a misbehaving
// caller cannot mint unbounded series. An empty or nil known means every
// name collapses to "other" — a caller who forgets this argument sees a
// blank-looking scheduler dashboard rather than an unbounded label, which is
// the point.
//
// It panics when reg is nil or namespace is empty (matching the platform
// convention of failing loudly at construction for required dependencies),
// when known contains the reserved SchedulerName("other") — that would let
// a legitimate scheduler share the collapsed-fallback series with every
// truly unknown name, defeating the point of the allowlist — and when a
// metric with the same name is already registered, so a double-wired
// registry surfaces at boot rather than as silently shared counters.
func NewPromTickRecorder(reg prometheus.Registerer, namespace string, known []SchedulerName) *PromTickRecorder {
	if reg == nil {
		panic("metrics: NewPromTickRecorder requires a non-nil registerer")
	}
	if namespace == "" {
		panic("metrics: NewPromTickRecorder requires a non-empty namespace")
	}
	knownSet := make(map[SchedulerName]struct{}, len(known))
	for _, name := range known {
		if name == SchedulerName(schedulerOther) {
			panic(`metrics: NewPromTickRecorder's known allowlist must not include the reserved "other" scheduler name`)
		}
		knownSet[name] = struct{}{}
	}

	factory := promauto.With(reg)
	return &PromTickRecorder{
		TicksTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scheduler_ticks_total",
			Help:      "Total number of completed background scheduler cycles, by scheduler and result.",
		}, []string{"scheduler", "result"}),
		TickDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "scheduler_tick_duration_seconds",
			Help:      "Background scheduler cycle duration in seconds, by scheduler.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"scheduler"}),
		LastSuccess: factory.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "scheduler_last_success_timestamp_seconds",
			Help:      "Unix timestamp of the most recent successful cycle, by scheduler.",
		}, []string{"scheduler"}),
		known: knownSet,
	}
}

// ObserveTick records one completed cycle: it increments the tick counter
// with the outcome derived from err, observes the cycle duration, and — on
// success only — moves the scheduler's last-success timestamp to now, so a
// failing scheduler's staleness is visible. A name outside r's known
// allowlist is collapsed to the fixed "other" label value so a misbehaving
// caller cannot mint unbounded series.
func (r *PromTickRecorder) ObserveTick(name SchedulerName, d time.Duration, err error) {
	scheduler := string(name)
	if _, ok := r.known[name]; !ok {
		scheduler = schedulerOther
	}
	result := tickResultSuccess
	if err != nil {
		result = tickResultError
	}
	r.TicksTotal.WithLabelValues(scheduler, result).Inc()
	r.TickDuration.WithLabelValues(scheduler).Observe(d.Seconds())
	if err == nil {
		r.LastSuccess.WithLabelValues(scheduler).SetToCurrentTime()
	}
}

// NopTickRecorder is a no-op TickRecorder for tests and optional wiring where
// tick instrumentation is irrelevant.
type NopTickRecorder struct{}

// Compile-time check that the no-op recorder satisfies the port.
var _ TickRecorder = NopTickRecorder{}

// ObserveTick discards the observation.
func (NopTickRecorder) ObserveTick(SchedulerName, time.Duration, error) {}
