package metrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ericfisherdev/nestcore/metrics"
)

// testNamespace is the fixed namespace used across this package's tests
// (shared with scheduler_test.go); the metric names it derives are asserted
// directly rather than via the old nestova_-prefixed literals, since the
// namespace is now caller-supplied.
const testNamespace = "testapp"

// familyNames gathers reg and returns the set of registered metric family
// names, factored out since every test in this file needs the same
// gather-then-index-by-name step.
func familyNames(t *testing.T, reg *prometheus.Registry) map[string]bool {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	names := make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}
	return names
}

// TestNewRegistryIncludesStandardCollectors verifies the registry gathers the
// runtime, process, and build-info collector families out of the box.
func TestNewRegistryIncludesStandardCollectors(t *testing.T) {
	names := familyNames(t, metrics.NewRegistry())
	for _, want := range []string{"go_goroutines", "go_build_info"} {
		if !names[want] {
			t.Errorf("registry is missing standard collector family %q", want)
		}
	}
}

// TestNewHTTPMetricsRegistersOnRegistry verifies the constructor registers all
// three metrics on the given registerer, prefixed by the supplied namespace.
// The vectors are exercised with a zero-value observation first: vector
// families are lazy and appear in a gather only once at least one child
// series exists.
func TestNewHTTPMetricsRegistersOnRegistry(t *testing.T) {
	reg := metrics.NewRegistry()
	m := metrics.NewHTTPMetrics(reg, testNamespace)
	m.RequestsTotal.WithLabelValues("GET", "GET /widgets", "200").Add(0)
	m.RequestDuration.WithLabelValues("GET", "GET /widgets").Observe(0)

	names := familyNames(t, reg)
	for _, want := range []string{
		testNamespace + "_http_requests_in_flight",
		testNamespace + "_http_requests_total",
		testNamespace + "_http_request_duration_seconds",
	} {
		if !names[want] {
			t.Errorf("%s not registered on the provided registry", want)
		}
	}
}

// TestNewHTTPMetricsNilRegistererPanics pins the platform convention of failing
// loudly at construction when a required dependency is missing.
func TestNewHTTPMetricsNilRegistererPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewHTTPMetrics(nil) did not panic")
		}
	}()
	metrics.NewHTTPMetrics(nil, testNamespace)
}

// TestNewHTTPMetricsEmptyNamespacePanics pins the same fail-loudly convention
// for an empty namespace: silently emitting unprefixed metric names would
// let two applications collide on a shared scrape target.
func TestNewHTTPMetricsEmptyNamespacePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewHTTPMetrics with an empty namespace did not panic")
		}
	}()
	metrics.NewHTTPMetrics(metrics.NewRegistry(), "")
}

// TestNewHTTPMetrics_DifferentNamespacesProduceDifferentNames is the
// property this whole change exists to guarantee: two applications sharing
// this package on separate registries must produce distinct, non-colliding
// metric names.
func TestNewHTTPMetrics_DifferentNamespacesProduceDifferentNames(t *testing.T) {
	regA := metrics.NewRegistry()
	regB := metrics.NewRegistry()
	metrics.NewHTTPMetrics(regA, "nestova").RequestsInFlight.Set(0)
	metrics.NewHTTPMetrics(regB, "nestorage").RequestsInFlight.Set(0)

	namesA := familyNames(t, regA)
	namesB := familyNames(t, regB)

	if !namesA["nestova_http_requests_in_flight"] {
		t.Error(`registry A is missing "nestova_http_requests_in_flight"`)
	}
	if namesA["nestorage_http_requests_in_flight"] {
		t.Error("registry A must not contain registry B's namespaced metric")
	}
	if !namesB["nestorage_http_requests_in_flight"] {
		t.Error(`registry B is missing "nestorage_http_requests_in_flight"`)
	}
	if namesB["nestova_http_requests_in_flight"] {
		t.Error("registry B must not contain registry A's namespaced metric")
	}
}

// TestHandlerServesRegistryFamilies verifies the scrape handler exposes the
// families registered on the provided registry, so the composition root can
// mount it without touching promhttp directly.
func TestHandlerServesRegistryFamilies(t *testing.T) {
	reg := metrics.NewRegistry()
	metrics.NewHTTPMetrics(reg, testNamespace).RequestsInFlight.Set(0)

	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	metrics.Handler(reg).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{"go_goroutines", testNamespace + "_http_requests_in_flight"} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape body is missing family %q", want)
		}
	}
}

// TestHandlerNilRegistryPanics pins the platform convention of failing loudly
// at construction when a required dependency is missing.
func TestHandlerNilRegistryPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Handler(nil) did not panic")
		}
	}()
	metrics.Handler(nil)
}
