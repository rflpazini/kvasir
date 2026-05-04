package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/rflpazini/kvasir/internal/health"
	"github.com/rflpazini/kvasir/internal/model"
)

const (
	cacheTTL            = 15 * time.Minute
	recentCacheTTL      = 5 * time.Minute
	defaultLimit        = 50
	maxLimit            = 100
	cacheKeyPrefix      = "search:v1:"
	recentCacheKey      = "recent:v1"
	cacheLookupBudget   = 200 * time.Millisecond
)

type handlers struct {
	deps Deps
}

func newHandlers(d Deps) *handlers {
	return &handlers{deps: d}
}

// search resolves a query through the cache (lookup), falling back to the
// aggregator on a miss. The cache stores the FULL set; the limit query
// parameter is applied in memory after lookup so we never serve a truncated
// payload to a wider request, see plan critical C2.
func (h *handlers) search(c echo.Context) error {
	q := strings.TrimSpace(c.QueryParam("q"))
	if q == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "query parameter 'q' is required")
	}

	limit := parseLimit(c.QueryParam("limit"))
	qualities, droppedUnknown, droppedEmpty := parseQualityFilter(c.QueryParam("quality"))
	if droppedUnknown > 0 {
		h.deps.Logger.Warn("quality filter dropped unknown tokens", "raw", c.QueryParam("quality"), "count", droppedUnknown)
		h.deps.Metrics.QualityFilterDropped.WithLabelValues("unknown").Add(float64(droppedUnknown))
	}
	if droppedEmpty > 0 {
		h.deps.Metrics.QualityFilterDropped.WithLabelValues("empty").Add(float64(droppedEmpty))
	}

	ctx := c.Request().Context()
	key := cacheKey(q)

	if h.deps.Cache != nil {
		if cached, ok := h.lookupCache(ctx, key); ok {
			h.deps.Metrics.CacheHits.Inc()
			cached.Cached = true
			cached.Results = applyLimit(model.FilterByQuality(cached.Results, qualities), limit)
			return c.JSON(http.StatusOK, cached)
		}
	}
	if h.deps.Cache != nil {
		h.deps.Metrics.CacheMisses.Inc()
	}

	timer := time.Now()
	resp := h.deps.Aggregator.Search(ctx, normalizeQuery(q))
	h.deps.Metrics.RequestDuration.Observe(time.Since(timer).Seconds())
	resp.Query = q

	// Cache the FULL set (pre-filter), so subsequent calls with different
	// filters all hit the cache. Filtering is a per-request concern.
	if h.deps.Cache != nil {
		h.storeCache(ctx, key, resp)
	}

	resp.Results = applyLimit(model.FilterByQuality(resp.Results, qualities), limit)
	return c.JSON(http.StatusOK, resp)
}

// recent returns the latest releases from every adapter. Same response
// envelope as /api/search; the cache uses a fixed key (no query) and a
// shorter TTL so stale releases do not linger. Quality filter still works.
func (h *handlers) recent(c echo.Context) error {
	limit := parseLimit(c.QueryParam("limit"))
	qualities, droppedUnknown, droppedEmpty := parseQualityFilter(c.QueryParam("quality"))

	ctx := c.Request().Context()

	if h.deps.Cache != nil {
		if cached, ok := h.lookupCache(ctx, recentCacheKey); ok {
			h.deps.Metrics.CacheHits.Inc()
			cached.Cached = true
			cached.Results = applyLimit(model.FilterByQuality(cached.Results, qualities), limit)
			return c.JSON(http.StatusOK, cached)
		}
		h.deps.Metrics.CacheMisses.Inc()
	}

	// Log + bump the dropped-token counter only on miss so the homepage
	// refresh button (same querystring hammered repeatedly) does not spam
	// the warn channel on every cache hit.
	if droppedUnknown > 0 {
		h.deps.Logger.Warn("quality filter dropped unknown tokens", "raw", c.QueryParam("quality"), "count", droppedUnknown)
		h.deps.Metrics.QualityFilterDropped.WithLabelValues("unknown").Add(float64(droppedUnknown))
	}
	if droppedEmpty > 0 {
		h.deps.Metrics.QualityFilterDropped.WithLabelValues("empty").Add(float64(droppedEmpty))
	}

	timer := time.Now()
	resp := h.deps.Aggregator.Recent(ctx)
	h.deps.Metrics.RequestDuration.Observe(time.Since(timer).Seconds())

	if h.deps.Cache != nil {
		h.storeRecentCache(ctx, resp)
	}

	resp.Results = applyLimit(model.FilterByQuality(resp.Results, qualities), limit)
	return c.JSON(http.StatusOK, resp)
}

// storeRecentCache persists the full Recent payload with a shorter TTL.
func (h *handlers) storeRecentCache(ctx context.Context, resp model.SearchResponse) {
	resp.Cached = false
	payload, err := json.Marshal(resp)
	if err != nil {
		h.deps.Logger.Warn("recent cache marshal failed", "err", err.Error())
		return
	}
	storeCtx, cancel := context.WithTimeout(ctx, cacheLookupBudget)
	defer cancel()
	if err := h.deps.Cache.SetSearch(storeCtx, recentCacheKey, payload, recentCacheTTL); err != nil {
		h.deps.Logger.Warn("recent cache store failed", "err", err.Error())
	}
}

// lookupCache returns a fresh SearchResponse on every hit because the payload
// is JSON-decoded into a new struct per call. Callers may safely mutate the
// returned Results slice (apply filter, truncate to limit) without affecting
// other in-flight requests. If this layer is ever swapped for an in-memory
// cache that returns shared backing arrays, callers must defensively copy.
func (h *handlers) lookupCache(ctx context.Context, key string) (model.SearchResponse, bool) {
	lookupCtx, cancel := context.WithTimeout(ctx, cacheLookupBudget)
	defer cancel()

	raw, err := h.deps.Cache.GetSearch(lookupCtx, key)
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			h.deps.Logger.Warn("cache lookup failed", "key", key, "err", err.Error())
		}
		return model.SearchResponse{}, false
	}
	var resp model.SearchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		h.deps.Logger.Warn("cache payload decode failed", "key", key, "err", err.Error())
		return model.SearchResponse{}, false
	}
	return resp, true
}

func (h *handlers) storeCache(ctx context.Context, key string, resp model.SearchResponse) {
	// Persist the FULL set (no limit applied) so subsequent wider queries
	// hit the cache without underfetching.
	resp.Cached = false
	payload, err := json.Marshal(resp)
	if err != nil {
		h.deps.Logger.Warn("cache marshal failed", "err", err.Error())
		return
	}

	storeCtx, cancel := context.WithTimeout(ctx, cacheLookupBudget)
	defer cancel()
	if err := h.deps.Cache.SetSearch(storeCtx, key, payload, cacheTTL); err != nil {
		h.deps.Logger.Warn("cache store failed", "key", key, "err", err.Error())
	}
}

// health reports per-adapter availability + the rolling status the
// tracker has observed (last successful scrape, current failure streak,
// degraded flag). The frontend uses `degraded` to fade chips when an
// adapter has been failing repeatedly, so a single-user homelab can
// tell upstream-dead from kvasir-broken at a glance.
//
// Idle adapters (registered but never called, or only called
// successfully a long time ago) are NOT marked degraded. Idle is not
// failing — see Source.Degraded for the rationale.
func (h *handlers) health(c echo.Context) error {
	type adapterHealth struct {
		Name                string `json:"name"`
		Status              string `json:"status"`
		LastSuccessAt       string `json:"last_success_at,omitempty"`
		ConsecutiveFailures int    `json:"consecutive_failures"`
		Degraded            bool   `json:"degraded"`
	}

	registered := h.deps.Registry.All()
	adapters := make([]adapterHealth, 0, len(registered))
	for _, a := range registered {
		entry := adapterHealth{Name: a.Name(), Status: "unknown"}
		if h.deps.Health != nil {
			src, ok := h.deps.Health.Get(a.Name())
			if !ok {
				src = health.Source{Name: a.Name()}
			}
			entry.Status = src.LastStatus
			if entry.Status == "" {
				entry.Status = "unknown"
			}
			entry.ConsecutiveFailures = src.ConsecutiveFailures
			entry.Degraded = src.Degraded(health.DefaultSuccessWindow, health.DefaultStreakThreshold)
			if !src.LastSuccessAt.IsZero() {
				entry.LastSuccessAt = src.LastSuccessAt.UTC().Format(time.RFC3339)
			}
		} else {
			entry.Status = "ok"
		}
		adapters = append(adapters, entry)
	}

	status := "ok"
	if len(adapters) == 0 {
		status = "no-adapters"
	}
	return c.JSON(http.StatusOK, echo.Map{
		"status":   status,
		"adapters": adapters,
	})
}

// forceFailure is the deterministic alert rehearsal hook. Disabled in prod.
func (h *handlers) forceFailure(c echo.Context) error {
	name := c.Param("adapter")
	if _, ok := h.deps.Registry.Get(name); !ok {
		return echo.NewHTTPError(http.StatusNotFound, "adapter not registered: "+name)
	}
	if h.deps.Metrics != nil {
		h.deps.Metrics.ConsecutiveFailures.WithLabelValues(name).Inc()
	}
	h.deps.Logger.Warn("force-failure triggered", "adapter", name)
	return c.JSON(http.StatusAccepted, echo.Map{"adapter": name, "action": "force-failure"})
}

// helpers

func parseLimit(raw string) int {
	if raw == "" {
		return defaultLimit
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return defaultLimit
	}
	if v > maxLimit {
		return maxLimit
	}
	return v
}

// parseQualityFilter parses a comma-separated quality list (e.g. "4k,1080p")
// into the canonical Quality slice. Unknown tokens are silently dropped (kept
// silent so adding a new bucket like Quality720p later does not 400 older
// clients), but the counts surface to the caller for observable drift.
//
// Returns:
//   - qualities: deduped slice in input order, nil if no recognized tokens
//   - droppedUnknown: count of non-empty tokens that did not match any bucket
//   - droppedEmpty: count of empty parts (",,4k" or trailing comma)
func parseQualityFilter(raw string) (qualities []model.Quality, droppedUnknown, droppedEmpty int) {
	if raw == "" {
		return nil, 0, 0
	}
	parts := strings.Split(raw, ",")
	out := make([]model.Quality, 0, len(parts))
	seen := make(map[model.Quality]struct{}, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			droppedEmpty++
			continue
		}
		q, ok := model.QualityFromString(p)
		if !ok {
			droppedUnknown++
			continue
		}
		if _, dup := seen[q]; dup {
			continue
		}
		seen[q] = struct{}{}
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil, droppedUnknown, droppedEmpty
	}
	return out, droppedUnknown, droppedEmpty
}

func applyLimit(results []model.Result, limit int) []model.Result {
	if limit <= 0 || limit >= len(results) {
		return results
	}
	return results[:limit]
}

func cacheKey(query string) string {
	sum := sha256.Sum256([]byte(normalizeQuery(query)))
	return cacheKeyPrefix + hex.EncodeToString(sum[:])
}

func normalizeQuery(query string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	return strings.Join(strings.Fields(q), " ")
}
