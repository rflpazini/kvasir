package http

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/rflpazini/kvasir/internal/adapter"
	"github.com/rflpazini/kvasir/internal/aggregator"
	"github.com/rflpazini/kvasir/internal/cache"
	"github.com/rflpazini/kvasir/internal/observability"
)

// Config carries the HTTP layer configuration.
type Config struct {
	Address              string
	StaticDir            string
	EnableDebugEndpoints bool
}

// Deps groups the runtime dependencies the HTTP layer needs.
type Deps struct {
	Logger     *slog.Logger
	Metrics    *observability.Metrics
	Registry   *adapter.Registry
	Aggregator *aggregator.Aggregator
	Cache      *cache.Client
	PromGather prometheus.Gatherer
}

// NewServer wires Echo with middleware, routes and handlers and returns the
// configured server ready to start.
func NewServer(cfg Config, deps Deps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(middleware.RequestID())
	e.Use(middleware.Recover())
	e.Use(noEdgeCacheMiddleware())
	e.Use(slogRequestLogger(deps.Logger))

	h := newHandlers(deps)

	e.GET("/healthz", h.health)
	e.GET("/api/search", h.search)
	e.GET("/metrics", echo.WrapHandler(promhttp.HandlerFor(deps.PromGather, promhttp.HandlerOpts{})))

	if cfg.StaticDir != "" {
		e.Static("/", cfg.StaticDir)
	}

	if cfg.EnableDebugEndpoints {
		e.POST("/debug/force-failure/:adapter", h.forceFailure)
		deps.Logger.Warn("debug endpoints enabled", "endpoint", "/debug/force-failure/:adapter")
	}

	return e
}

// noEdgeCacheMiddleware tells Cloudflare (and any other intermediary cache)
// not to serve stale responses. Without an explicit Cache-Control header,
// CF applies its own 4h default to static assets like /app.js — fine in
// theory, painful in practice when a homelab redeploys faster than that
// and users see a stale frontend bundle until the TTL elapses.
//
// `no-cache, must-revalidate` lets the browser keep the asset locally but
// forces a revalidation roundtrip on every load. Single-user homelab,
// edge cache savings here are worth less than predictable deploys.
func noEdgeCacheMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Before(func() {
				if c.Response().Header().Get("Cache-Control") == "" {
					c.Response().Header().Set("Cache-Control", "no-cache, must-revalidate")
				}
			})
			return next(c)
		}
	}
}

// slogRequestLogger emits a structured log line per HTTP request.
func slogRequestLogger(log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)

			req := c.Request()
			res := c.Response()
			status := res.Status
			if err != nil {
				var he *echo.HTTPError
				if errors.As(err, &he) {
					status = he.Code
				} else {
					status = http.StatusInternalServerError
				}
			}

			log.Info("http request",
				"request_id", res.Header().Get(echo.HeaderXRequestID),
				"method", req.Method,
				"path", req.URL.Path,
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
			)

			return err
		}
	}
}

// EnableDebugFromEnv reports whether the operator opted into debug endpoints.
func EnableDebugFromEnv() bool {
	v := os.Getenv("ENABLE_DEBUG_ENDPOINTS")
	return v == "true" || v == "1"
}
