package aggregator_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/rflpazini/kvasir/internal/adapter"
	"github.com/rflpazini/kvasir/internal/aggregator"
	"github.com/rflpazini/kvasir/internal/model"
	"github.com/rflpazini/kvasir/internal/observability"
)

// fakeAdapter is a controllable adapter.Adapter for unit tests.
type fakeAdapter struct {
	name    string
	delay   time.Duration
	results []model.Result
	err     error
	calls   atomic.Int32
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) Search(ctx context.Context, _ string) ([]model.Result, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func (f *fakeAdapter) Recent(ctx context.Context) ([]model.Result, error) {
	// Reuse Search semantics in tests; "recent" is just another fan-out path.
	return f.Search(ctx, "")
}

func (f *fakeAdapter) HealthCheck(_ context.Context) error { return nil }

func registry(t *testing.T, adapters ...adapter.Adapter) *adapter.Registry {
	t.Helper()
	r := adapter.NewRegistry()
	for _, a := range adapters {
		r.Register(a)
	}
	return r
}

// TestAggregator_FanOutResultsAreDeterministic verifies merged results
// always come back in the same order regardless of which adapter happens
// to finish first. Primary sort key is Source; secondary is DetailURL,
// which doubles as a future-proof guard for adapters that themselves
// fan out internally and would otherwise return in non-deterministic
// order. The B adapter intentionally returns `B2` before `B1` to prove
// the secondary key takes precedence over goroutine append order.
func TestAggregator_FanOutResultsAreDeterministic(t *testing.T) {
	bSlow := &fakeAdapter{name: "b", delay: 80 * time.Millisecond, results: []model.Result{
		{Title: "B2", Source: "b", DetailURL: "https://b/2"},
		{Title: "B1", Source: "b", DetailURL: "https://b/1"},
	}}
	aFast := &fakeAdapter{name: "a", results: []model.Result{
		{Title: "A1", Source: "a", DetailURL: "https://a/1"},
		{Title: "A2", Source: "a", DetailURL: "https://a/2"},
	}}
	cMid := &fakeAdapter{name: "c", delay: 30 * time.Millisecond, results: []model.Result{
		{Title: "C1", Source: "c", DetailURL: "https://c/1"},
	}}

	agg := aggregator.New(registry(t, bSlow, aFast, cMid), 1*time.Second, nil, nil)

	// Within `b` the adapter returned [B2, B1] (non-alphabetical), but
	// the response sorts by DetailURL → B1 first, B2 second.
	want := []string{"A1", "A2", "B1", "B2", "C1"}
	for i := 0; i < 5; i++ {
		resp := agg.Search(context.Background(), "x")
		got := make([]string, 0, len(resp.Results))
		for _, r := range resp.Results {
			got = append(got, r.Title)
		}
		if !equalStrings(got, want) {
			t.Errorf("run %d order = %v, want %v", i, got, want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAggregator_EmitsPerSourceMetrics verifies the aggregator wires the
// declared collectors. After a single Search: each adapter has a sample
// recorded against ScrapeDuration, the bad adapter bumps ScrapeErrors,
// the good adapter bumps ResultsReturned, and ConsecutiveFailures gauge
// reflects per-source streak (zeroed on success, incremented on error).
func TestAggregator_EmitsPerSourceMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observability.NewMetrics(reg)

	good := &fakeAdapter{name: "good", results: []model.Result{
		{Title: "G1", Source: "good", DetailURL: "https://g/1"},
		{Title: "G2", Source: "good", DetailURL: "https://g/2"},
	}}
	bad := &fakeAdapter{name: "bad", err: errors.New("scrape boom")}

	agg := aggregator.New(registry(t, good, bad), 1*time.Second, m, nil)
	agg.Search(context.Background(), "x")

	if got := histogramSampleCount(t, reg, "kvasir_scrape_duration_seconds", map[string]string{"adapter": "good", "status": model.StatusOK}); got != 1 {
		t.Errorf("good ScrapeDuration sample_count = %d, want 1", got)
	}
	if got := histogramSampleCount(t, reg, "kvasir_scrape_duration_seconds", map[string]string{"adapter": "bad", "status": model.StatusError}); got != 1 {
		t.Errorf("bad ScrapeDuration{error} sample_count = %d, want 1", got)
	}
	if got := promtestutil.ToFloat64(m.ScrapeErrors.WithLabelValues("bad", model.StatusError)); got != 1 {
		t.Errorf("ScrapeErrors{bad, error} = %v, want 1", got)
	}
	if got := histogramSampleCount(t, reg, "kvasir_results_returned", map[string]string{"adapter": "good"}); got != 1 {
		t.Errorf("ResultsReturned{good} sample_count = %d, want 1", got)
	}
	if got := promtestutil.ToFloat64(m.ConsecutiveFailures.WithLabelValues("good")); got != 0 {
		t.Errorf("ConsecutiveFailures{good} = %v, want 0", got)
	}
	if got := promtestutil.ToFloat64(m.ConsecutiveFailures.WithLabelValues("bad")); got != 1 {
		t.Errorf("ConsecutiveFailures{bad} = %v, want 1", got)
	}

	// Second run: good keeps succeeding (gauge stays 0), bad accumulates.
	agg.Search(context.Background(), "x")
	if got := promtestutil.ToFloat64(m.ConsecutiveFailures.WithLabelValues("bad")); got != 2 {
		t.Errorf("ConsecutiveFailures{bad} after 2 errors = %v, want 2", got)
	}
}

// TestAggregator_NilMetricsIsSafe must NOT panic when the operator passes
// nil metrics — the test suite leans on this widely.
func TestAggregator_NilMetricsIsSafe(t *testing.T) {
	a := &fakeAdapter{name: "a", results: []model.Result{{Title: "x", Source: "a", DetailURL: "https://a"}}}
	agg := aggregator.New(registry(t, a), 1*time.Second, nil, nil)
	resp := agg.Search(context.Background(), "x")
	if len(resp.Results) != 1 {
		t.Errorf("expected 1 result, got %d", len(resp.Results))
	}
}

// histogramSampleCount returns the sample_count for a histogram metric
// matching the given name + label set.
func histogramSampleCount(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) uint64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if histogramMatchesLabels(m, labels) {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

func histogramMatchesLabels(m *dto.Metric, want map[string]string) bool {
	if len(m.GetLabel()) != len(want) {
		return false
	}
	for _, lp := range m.GetLabel() {
		if v, ok := want[lp.GetName()]; !ok || v != lp.GetValue() {
			return false
		}
	}
	return true
}

func TestAggregator_Recent_FanOut(t *testing.T) {
	a := &fakeAdapter{name: "a", results: []model.Result{{Title: "A1", Source: "a"}}}
	b := &fakeAdapter{name: "b", results: []model.Result{{Title: "B1", Source: "b"}, {Title: "B2", Source: "b"}}}
	bad := &fakeAdapter{name: "bad", err: errors.New("recent failed")}

	agg := aggregator.New(registry(t, a, b, bad), 1*time.Second, nil, nil)
	resp := agg.Recent(context.Background())

	if got := len(resp.Results); got != 3 {
		t.Errorf("expected 3 results from healthy adapters, got %d", got)
	}
	if resp.SourceStats["a"].Status != model.StatusOK {
		t.Errorf("a status = %q", resp.SourceStats["a"].Status)
	}
	if resp.SourceStats["b"].Count != 2 {
		t.Errorf("b count = %d, want 2", resp.SourceStats["b"].Count)
	}
	if resp.SourceStats["bad"].Status != model.StatusError {
		t.Errorf("bad status = %q", resp.SourceStats["bad"].Status)
	}
	if resp.Query != "" {
		t.Errorf("Recent must not set Query, got %q", resp.Query)
	}
}

func TestAggregator_RunsAdaptersInParallel(t *testing.T) {
	a := &fakeAdapter{name: "a", delay: 100 * time.Millisecond, results: []model.Result{{Title: "A1", Source: "a"}}}
	b := &fakeAdapter{name: "b", delay: 100 * time.Millisecond, results: []model.Result{{Title: "B1", Source: "b"}}}
	c := &fakeAdapter{name: "c", delay: 100 * time.Millisecond, results: []model.Result{{Title: "C1", Source: "c"}}}

	agg := aggregator.New(registry(t, a, b, c), 1*time.Second, nil, nil)

	start := time.Now()
	resp := agg.Search(context.Background(), "x")
	elapsed := time.Since(start)

	// 3 adapters * 100ms each. If sequential: 300ms+. If parallel: ~100ms.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("adapters did not run in parallel: total=%v", elapsed)
	}
	if got := len(resp.Results); got != 3 {
		t.Errorf("expected 3 results, got %d", got)
	}
	for _, name := range []string{"a", "b", "c"} {
		if resp.SourceStats[name].Status != model.StatusOK {
			t.Errorf("source %s status = %q, want ok", name, resp.SourceStats[name].Status)
		}
	}
}

func TestAggregator_OneFailureDoesNotDerailOthers(t *testing.T) {
	good := &fakeAdapter{name: "good", results: []model.Result{{Title: "ok", Source: "good"}}}
	bad := &fakeAdapter{name: "bad", err: errors.New("scrape boom")}
	other := &fakeAdapter{name: "other", results: []model.Result{{Title: "ok2", Source: "other"}}}

	agg := aggregator.New(registry(t, good, bad, other), 1*time.Second, nil, nil)

	resp := agg.Search(context.Background(), "x")

	if len(resp.Results) != 2 {
		t.Errorf("expected 2 results from healthy adapters, got %d", len(resp.Results))
	}
	if resp.SourceStats["bad"].Status != model.StatusError {
		t.Errorf("bad source status = %q, want %q", resp.SourceStats["bad"].Status, model.StatusError)
	}
	if resp.SourceStats["bad"].ErrorMsg == "" {
		t.Error("bad source must carry an error message")
	}
	if resp.SourceStats["good"].Status != model.StatusOK {
		t.Errorf("good source status = %q", resp.SourceStats["good"].Status)
	}
}

func TestAggregator_TimeoutClassifiedAsTimeoutNotError(t *testing.T) {
	slow := &fakeAdapter{name: "slow", delay: 200 * time.Millisecond, results: []model.Result{{Title: "x", Source: "slow"}}}
	fast := &fakeAdapter{name: "fast", results: []model.Result{{Title: "y", Source: "fast"}}}

	// per-adapter timeout 50ms, slow will trip it
	agg := aggregator.New(registry(t, slow, fast), 50*time.Millisecond, nil, nil)

	resp := agg.Search(context.Background(), "x")

	if resp.SourceStats["slow"].Status != model.StatusTimeout {
		t.Errorf("slow source status = %q, want %q", resp.SourceStats["slow"].Status, model.StatusTimeout)
	}
	if resp.SourceStats["fast"].Status != model.StatusOK {
		t.Errorf("fast source status = %q", resp.SourceStats["fast"].Status)
	}
	if len(resp.Results) != 1 {
		t.Errorf("expected 1 result from fast adapter, got %d", len(resp.Results))
	}
}

func TestAggregator_DefaultTimeoutWhenZeroProvided(t *testing.T) {
	a := &fakeAdapter{name: "a", results: []model.Result{}}
	agg := aggregator.New(registry(t, a), 0, nil, nil) // 0 should fall back to internal default

	// Just verify it runs without blocking forever; actual default value is implementation detail.
	done := make(chan struct{})
	go func() {
		agg.Search(context.Background(), "x")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("aggregator hung; default timeout did not apply")
	}
}

func TestAggregator_EmptyRegistryReturnsEmptyResponse(t *testing.T) {
	agg := aggregator.New(adapter.NewRegistry(), 1*time.Second, nil, nil)
	resp := agg.Search(context.Background(), "anything")

	if len(resp.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(resp.Results))
	}
	if len(resp.SourceStats) != 0 {
		t.Errorf("expected 0 source stats, got %d", len(resp.SourceStats))
	}
	if resp.Query != "anything" {
		t.Errorf("query roundtrip = %q, want anything", resp.Query)
	}
}

func TestAggregator_DurationReported(t *testing.T) {
	a := &fakeAdapter{name: "a", delay: 50 * time.Millisecond, results: []model.Result{}}
	agg := aggregator.New(registry(t, a), 1*time.Second, nil, nil)

	resp := agg.Search(context.Background(), "x")

	if resp.DurationMs < 40 || resp.DurationMs > 500 {
		t.Errorf("DurationMs = %d, expected ~50ms", resp.DurationMs)
	}
}

func TestAggregator_ParentContextCancelPropagates(t *testing.T) {
	slow := &fakeAdapter{name: "slow", delay: 500 * time.Millisecond, results: []model.Result{}}
	agg := aggregator.New(registry(t, slow), 1*time.Second, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	start := time.Now()
	resp := agg.Search(ctx, "x")
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("parent cancel did not propagate; took %v", elapsed)
	}
	if resp.SourceStats["slow"].Status != model.StatusTimeout {
		t.Errorf("expected timeout status on cancelled context, got %q", resp.SourceStats["slow"].Status)
	}
}
