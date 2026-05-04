package http

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/rflpazini/kvasir/internal/model"
)

type handlers struct {
	deps Deps
}

func newHandlers(d Deps) *handlers {
	return &handlers{deps: d}
}

// search is a stub. Phase 1 wires aggregator + cache here.
func (h *handlers) search(c echo.Context) error {
	q := c.QueryParam("q")
	if q == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "query parameter 'q' is required")
	}

	resp := model.SearchResponse{
		Query:       q,
		Results:     []model.Result{},
		SourceStats: map[string]model.SourceStat{},
		DurationMs:  0,
		Cached:      false,
	}
	return c.JSON(http.StatusOK, resp)
}

// health reports per-adapter status. Phase 1 expands this with real probes.
func (h *handlers) health(c echo.Context) error {
	adapters := map[string]string{}
	for _, a := range h.deps.Registry.All() {
		adapters[a.Name()] = "unknown"
	}
	return c.JSON(http.StatusOK, echo.Map{
		"status":   "ok",
		"adapters": adapters,
	})
}

// forceFailure is the deterministic alert rehearsal hook. Disabled in prod.
func (h *handlers) forceFailure(c echo.Context) error {
	name := c.Param("adapter")
	if _, ok := h.deps.Registry.Get(name); !ok {
		return echo.NewHTTPError(http.StatusNotFound, "adapter not registered: "+name)
	}
	// Phase 3 wires this to the consecutive_failures gauge.
	h.deps.Logger.Warn("force-failure triggered (stub)", "adapter", name)
	return c.JSON(http.StatusAccepted, echo.Map{"adapter": name, "action": "force-failure"})
}
