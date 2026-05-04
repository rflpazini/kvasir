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

	"github.com/rflpazini/kvasir/internal/model"
)

const (
	cacheTTL          = 15 * time.Minute
	defaultLimit      = 50
	maxLimit          = 100
	cacheKeyPrefix    = "search:v1:"
	cacheLookupBudget = 200 * time.Millisecond
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
	qualities := parseQualityFilter(c.QueryParam("quality"))

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

// health reports per-adapter availability.
func (h *handlers) health(c echo.Context) error {
	adapters := map[string]string{}
	for _, a := range h.deps.Registry.All() {
		adapters[a.Name()] = "ok"
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
// into the canonical Quality slice. Unknown tokens are ignored. An empty or
// missing parameter yields nil (no filtering).
func parseQualityFilter(raw string) []model.Quality {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]model.Quality, 0, len(parts))
	seen := make(map[model.Quality]struct{}, len(parts))
	for _, p := range parts {
		q, ok := model.QualityFromString(p)
		if !ok {
			continue
		}
		if _, dup := seen[q]; dup {
			continue
		}
		seen[q] = struct{}{}
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
