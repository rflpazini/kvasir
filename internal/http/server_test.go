package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/rflpazini/kvasir/internal/adapter"
	"github.com/rflpazini/kvasir/internal/aggregator"
	"github.com/rflpazini/kvasir/internal/cache"
	apphttp "github.com/rflpazini/kvasir/internal/http"
	"github.com/rflpazini/kvasir/internal/model"
	"github.com/rflpazini/kvasir/internal/observability"
)

// fakeAdapter is reused for HTTP-layer tests.
type fakeAdapter struct {
	name    string
	results []model.Result
	err     error
	calls   atomic.Int32
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) Search(_ context.Context, _ string) ([]model.Result, error) {
	f.calls.Add(1)
	return f.results, f.err
}

func (f *fakeAdapter) Recent(_ context.Context) ([]model.Result, error) {
	f.calls.Add(1)
	return f.results, f.err
}

func (f *fakeAdapter) HealthCheck(_ context.Context) error { return f.err }

type harness struct {
	echo  *echo.Echo
	cache *cache.Client
	mr    *miniredis.Miniredis
	a     *fakeAdapter
}

func newHarness(t *testing.T, debug bool, results []model.Result, err error) *harness {
	t.Helper()
	mr := miniredis.RunT(t)
	c := cache.New(cache.Config{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })

	a := &fakeAdapter{name: "fake", results: results, err: err}
	reg := adapter.NewRegistry()
	reg.Register(a)
	agg := aggregator.New(reg, 500*time.Millisecond)

	promReg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(promReg)
	logger := observability.NewLogger("error")

	e := apphttp.NewServer(apphttp.Config{
		EnableDebugEndpoints: debug,
	}, apphttp.Deps{
		Logger:     logger,
		Metrics:    metrics,
		Registry:   reg,
		Aggregator: agg,
		Cache:      c,
		PromGather: promReg,
	})
	return &harness{echo: e, cache: c, mr: mr, a: a}
}

// do executes a request against the harness's echo instance and returns the recorded response.
func (h *harness) do(t *testing.T, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	h.echo.ServeHTTP(rec, req)

	if rec.Code >= 400 {
		return rec, nil
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		return rec, nil
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil && rec.Body.Len() > 0 {
		// Best-effort decode; some endpoints return non-map JSON.
		return rec, nil
	}
	return rec, body
}

func TestHandler_SearchEmptyQueryReturns400(t *testing.T) {
	h := newHarness(t, false, nil, nil)
	rec, _ := h.do(t, stdhttp.MethodGet, "/api/search")
	if rec.Code != stdhttp.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_SearchHappyPath(t *testing.T) {
	h := newHarness(t, false, []model.Result{
		{Title: "A", Source: "fake", DetailURL: "https://x/a"},
		{Title: "B", Source: "fake", DetailURL: "https://x/b"},
	}, nil)

	rec, body := h.do(t, stdhttp.MethodGet, "/api/search?q=interstellar")

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body["query"] != "interstellar" {
		t.Errorf("query roundtrip = %v", body["query"])
	}
	results, ok := body["results"].([]any)
	if !ok || len(results) != 2 {
		t.Errorf("results = %v", body["results"])
	}
	if body["cached"] != false {
		t.Errorf("cached = %v, want false on first call", body["cached"])
	}
}

func TestHandler_SearchSecondCallHitsCache(t *testing.T) {
	h := newHarness(t, false, []model.Result{{Title: "X", Source: "fake"}}, nil)

	if rec, _ := h.do(t, stdhttp.MethodGet, "/api/search?q=q1"); rec.Code != 200 {
		t.Fatalf("first call: %d", rec.Code)
	}
	rec, body := h.do(t, stdhttp.MethodGet, "/api/search?q=q1")
	if rec.Code != 200 {
		t.Fatalf("second call: %d", rec.Code)
	}
	if body["cached"] != true {
		t.Error("expected cached=true on second call")
	}
	if got := h.a.calls.Load(); got != 1 {
		t.Errorf("adapter called %d times, want 1 (cache hit on second)", got)
	}
}

func TestHandler_SearchLimitTruncatesAfterCacheLookup(t *testing.T) {
	results := make([]model.Result, 0, 10)
	for i := 0; i < 10; i++ {
		results = append(results, model.Result{Title: "x", Source: "fake"})
	}
	h := newHarness(t, false, results, nil)

	// First call: limit=10 (default 50, gets all)
	_, body1 := h.do(t, stdhttp.MethodGet, "/api/search?q=many")
	if got := len(body1["results"].([]any)); got != 10 {
		t.Errorf("first call returned %d, want 10", got)
	}

	// Second call with limit=3: cache hit, but truncated.
	_, body2 := h.do(t, stdhttp.MethodGet, "/api/search?q=many&limit=3")
	if got := len(body2["results"].([]any)); got != 3 {
		t.Errorf("limit=3 returned %d, want 3", got)
	}
	if body2["cached"] != true {
		t.Error("limit=3 should still be a cache hit")
	}

	// Cache must STILL hold the full set (verify by another call without limit).
	_, body3 := h.do(t, stdhttp.MethodGet, "/api/search?q=many")
	if got := len(body3["results"].([]any)); got != 10 {
		t.Errorf("after limited call, full set call returned %d, want 10 (cache must keep FULL set)", got)
	}
}

func TestHandler_SearchQualityFilter(t *testing.T) {
	results := []model.Result{
		{Title: "A 2160p", Source: "fake", Quality: model.Quality4K, DetailURL: "https://x/a"},
		{Title: "B 1080p", Source: "fake", Quality: model.Quality1080p, DetailURL: "https://x/b"},
		{Title: "C 720p", Source: "fake", Quality: model.QualityOther, DetailURL: "https://x/c"},
		{Title: "D 4K", Source: "fake", Quality: model.Quality4K, DetailURL: "https://x/d"},
	}

	t.Run("no filter returns all", func(t *testing.T) {
		h := newHarness(t, false, results, nil)
		_, body := h.do(t, stdhttp.MethodGet, "/api/search?q=movie")
		if got := len(body["results"].([]any)); got != 4 {
			t.Errorf("expected 4 results, got %d", got)
		}
	})

	t.Run("quality=4k returns only 4K", func(t *testing.T) {
		h := newHarness(t, false, results, nil)
		_, body := h.do(t, stdhttp.MethodGet, "/api/search?q=movie&quality=4k")
		got := body["results"].([]any)
		if len(got) != 2 {
			t.Errorf("expected 2 results, got %d", len(got))
		}
		for _, r := range got {
			if r.(map[string]any)["quality"] != "4K" {
				t.Errorf("got non-4K result: %+v", r)
			}
		}
	})

	t.Run("quality=1080p returns only 1080p", func(t *testing.T) {
		h := newHarness(t, false, results, nil)
		_, body := h.do(t, stdhttp.MethodGet, "/api/search?q=movie&quality=1080p")
		got := body["results"].([]any)
		if len(got) != 1 {
			t.Errorf("expected 1 result, got %d", len(got))
		}
	})

	t.Run("quality=4k,1080p returns both buckets", func(t *testing.T) {
		h := newHarness(t, false, results, nil)
		_, body := h.do(t, stdhttp.MethodGet, "/api/search?q=movie&quality=4k,1080p")
		got := body["results"].([]any)
		if len(got) != 3 {
			t.Errorf("expected 3 results (2x 4K + 1x 1080p), got %d", len(got))
		}
	})

	t.Run("unknown quality token is ignored, others honored", func(t *testing.T) {
		h := newHarness(t, false, results, nil)
		_, body := h.do(t, stdhttp.MethodGet, "/api/search?q=movie&quality=720p,4k")
		got := body["results"].([]any)
		if len(got) != 2 {
			t.Errorf("expected 2 results (only 4K honored), got %d", len(got))
		}
	})

	t.Run("cache stores full set, filter applied per call", func(t *testing.T) {
		h := newHarness(t, false, results, nil)

		// First call without filter: warms cache with full set.
		_, body1 := h.do(t, stdhttp.MethodGet, "/api/search?q=cachefilter")
		if got := len(body1["results"].([]any)); got != 4 {
			t.Fatalf("first call expected 4, got %d", got)
		}

		// Second call with filter: cache hit, but only filtered subset returned.
		_, body2 := h.do(t, stdhttp.MethodGet, "/api/search?q=cachefilter&quality=1080p")
		if body2["cached"] != true {
			t.Error("expected cache hit on filtered call")
		}
		if got := len(body2["results"].([]any)); got != 1 {
			t.Errorf("filtered call expected 1, got %d", got)
		}

		// Third call without filter: cache must still hold full set.
		_, body3 := h.do(t, stdhttp.MethodGet, "/api/search?q=cachefilter")
		if got := len(body3["results"].([]any)); got != 4 {
			t.Errorf("full-set call after filter expected 4, got %d (cache leaked filter)", got)
		}
	})

	t.Run("unknown and empty tokens bump dropped counter", func(t *testing.T) {
		h := newHarness(t, false, results, nil)

		// Mix of unknown ("720p") and empty (",,") tokens. 4k is recognized so
		// the request still succeeds with a valid filter applied.
		_, body := h.do(t, stdhttp.MethodGet, "/api/search?q=drift&quality=720p,,4k,")
		if rec := body["results"].([]any); len(rec) != 2 {
			t.Errorf("expected 2 (4K) results, got %d", len(rec))
		}

		// /metrics must expose both reasons with the right counts.
		rec, _ := h.do(t, stdhttp.MethodGet, "/metrics")
		out := rec.Body.String()
		if !strings.Contains(out, `kvasir_quality_filter_dropped_total{reason="unknown"} 1`) {
			t.Errorf("expected unknown=1 in metrics, got:\n%s", excerpt(out, "quality_filter_dropped"))
		}
		if !strings.Contains(out, `kvasir_quality_filter_dropped_total{reason="empty"} 2`) {
			t.Errorf("expected empty=2 in metrics, got:\n%s", excerpt(out, "quality_filter_dropped"))
		}
	})
}

// excerpt returns lines from out containing needle, for friendlier failure messages.
func excerpt(out, needle string) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func TestHandler_SearchAdapterErrorRecordedInSourceStats(t *testing.T) {
	h := newHarness(t, false, nil, errors.New("scrape boom"))

	_, body := h.do(t, stdhttp.MethodGet, "/api/search?q=anything")
	stats, ok := body["source_stats"].(map[string]any)
	if !ok {
		t.Fatalf("source_stats not present: %v", body)
	}
	fake := stats["fake"].(map[string]any)
	if fake["status"] != "error" {
		t.Errorf("status = %v, want error", fake["status"])
	}
	if fake["error"] == "" || fake["error"] == nil {
		t.Errorf("error message missing: %v", fake)
	}
}

func TestServer_SetsNoEdgeCacheHeader(t *testing.T) {
	// Every response must carry Cache-Control so CF (or any intermediary)
	// does not serve stale assets after a redeploy. healthz is convenient
	// because it does not return a body that masks the header.
	h := newHarness(t, false, nil, nil)
	rec, _ := h.do(t, stdhttp.MethodGet, "/healthz")
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

func TestHandler_RecentReturnsAggregate(t *testing.T) {
	results := []model.Result{
		{Title: "Latest 4K", Source: "fake", Quality: model.Quality4K, DetailURL: "https://x/a"},
		{Title: "Latest 1080p", Source: "fake", Quality: model.Quality1080p, DetailURL: "https://x/b"},
	}

	t.Run("happy path returns all", func(t *testing.T) {
		h := newHarness(t, false, results, nil)
		rec, body := h.do(t, stdhttp.MethodGet, "/api/recent")
		if rec.Code != stdhttp.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if got := len(body["results"].([]any)); got != 2 {
			t.Errorf("expected 2, got %d", got)
		}
		if body["query"] != "" {
			t.Errorf("Recent must not echo a query, got %v", body["query"])
		}
	})

	t.Run("quality filter applies", func(t *testing.T) {
		h := newHarness(t, false, results, nil)
		_, body := h.do(t, stdhttp.MethodGet, "/api/recent?quality=4k")
		got := body["results"].([]any)
		if len(got) != 1 || got[0].(map[string]any)["quality"] != "4K" {
			t.Errorf("expected 1x4K, got %+v", got)
		}
	})

	t.Run("second call uses cache", func(t *testing.T) {
		h := newHarness(t, false, results, nil)
		if rec, _ := h.do(t, stdhttp.MethodGet, "/api/recent"); rec.Code != 200 {
			t.Fatalf("first call: %d", rec.Code)
		}
		_, body2 := h.do(t, stdhttp.MethodGet, "/api/recent")
		if body2["cached"] != true {
			t.Error("expected cache hit on second call")
		}
		if got := h.a.calls.Load(); got != 1 {
			t.Errorf("adapter called %d times, want 1", got)
		}
	})

	t.Run("cache write failure does not break the response", func(t *testing.T) {
		// Close the miniredis backing store after harness construction so
		// the very next SetSearch fails. The handler must still return 200
		// with the freshly aggregated payload.
		h := newHarness(t, false, results, nil)
		h.mr.Close()

		rec, body := h.do(t, stdhttp.MethodGet, "/api/recent")
		if rec.Code != stdhttp.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := len(body["results"].([]any)); got != 2 {
			t.Errorf("expected 2 results despite cache failure, got %d", got)
		}
	})
}

// TestHandler_SearchLimitNeverPanicsAboveResultCount guards against a
// regression where applyLimit would blindly slice results[:limit]; with
// limit > len(results) that path panics. Default limit (50) hitting a
// 7-result fixture covers the hot path; explicit ?limit=100 covers the
// case where a thoughtful client over-asks.
func TestHandler_SearchLimitNeverPanicsAboveResultCount(t *testing.T) {
	results := make([]model.Result, 0, 7)
	for i := 0; i < 7; i++ {
		results = append(results, model.Result{Title: "x", Source: "fake", DetailURL: "https://x"})
	}
	h := newHarness(t, false, results, nil)

	rec, body := h.do(t, stdhttp.MethodGet, "/api/search?q=q&limit=100")
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := len(body["results"].([]any)); got != 7 {
		t.Errorf("limit=100 over 7 results returned %d, want 7", got)
	}
}

// TestHandler_SearchQualityFilterDedupes locks the dedup branch in
// parseQualityFilter — duplicate tokens must collapse, not produce
// double-filter behavior or duplicated results.
func TestHandler_SearchQualityFilterDedupes(t *testing.T) {
	results := []model.Result{
		{Title: "A 4K", Source: "fake", Quality: model.Quality4K, DetailURL: "https://x/a"},
		{Title: "B 1080p", Source: "fake", Quality: model.Quality1080p, DetailURL: "https://x/b"},
	}
	h := newHarness(t, false, results, nil)

	_, body := h.do(t, stdhttp.MethodGet, "/api/search?q=q&quality=4k,4K,4k,1080p,1080P")
	got := body["results"].([]any)
	if len(got) != 2 {
		t.Errorf("expected 2 results (4K + 1080p, deduped), got %d", len(got))
	}
}

func TestHandler_HealthzReportsAdapters(t *testing.T) {
	h := newHarness(t, false, nil, nil)
	rec, body := h.do(t, stdhttp.MethodGet, "/healthz")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if body["status"] != "ok" {
		t.Errorf("overall status = %v", body["status"])
	}
	adapters := body["adapters"].(map[string]any)
	if _, has := adapters["fake"]; !has {
		t.Error("fake adapter missing from /healthz")
	}
}

func TestHandler_DebugEndpointDisabledByDefault(t *testing.T) {
	h := newHarness(t, false, nil, nil)
	rec, _ := h.do(t, stdhttp.MethodPost, "/debug/force-failure/fake")
	// Without ENABLE_DEBUG_ENDPOINTS, the route is not registered → 404 or 405.
	if rec.Code < 400 {
		t.Errorf("debug endpoint reachable when disabled: %d", rec.Code)
	}
}

func TestHandler_DebugEndpointWhenEnabled(t *testing.T) {
	h := newHarness(t, true, nil, nil)
	rec, _ := h.do(t, stdhttp.MethodPost, "/debug/force-failure/fake")
	if rec.Code != stdhttp.StatusAccepted {
		t.Errorf("debug status = %d, want 202", rec.Code)
	}
}

func TestHandler_DebugEndpointRejectsUnknownAdapter(t *testing.T) {
	h := newHarness(t, true, nil, nil)
	rec, _ := h.do(t, stdhttp.MethodPost, "/debug/force-failure/does-not-exist")
	if rec.Code != stdhttp.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandler_MetricsExposesKvasirCollectors(t *testing.T) {
	h := newHarness(t, false, []model.Result{{Title: "x", Source: "fake"}}, nil)

	// Trigger one search to bump cache_misses_total at least.
	h.do(t, stdhttp.MethodGet, "/api/search?q=anything")

	rec, _ := h.do(t, stdhttp.MethodGet, "/metrics")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "kvasir_cache_misses_total") {
		t.Error("/metrics missing kvasir_cache_misses_total")
	}
}

func TestHandler_QueryNormalizationCachesAcrossWhitespace(t *testing.T) {
	h := newHarness(t, false, []model.Result{{Title: "z", Source: "fake"}}, nil)

	// First call populates cache.
	h.do(t, stdhttp.MethodGet, "/api/search?q=Matrix%201080p")

	// Second call with different casing + extra spaces should hit the same cache key.
	_, body := h.do(t, stdhttp.MethodGet, "/api/search?q=matrix%20%201080P")

	if body["cached"] != true {
		t.Errorf("expected cache hit across normalization, cached=%v", body["cached"])
	}
	if got := h.a.calls.Load(); got != 1 {
		t.Errorf("adapter called %d times, want 1 (normalize must collapse keys)", got)
	}
}
