package main

import (
	"context"
	"errors"
	stdhttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/rflpazini/kvasir/internal/adapter"
	"github.com/rflpazini/kvasir/internal/aggregator"
	"github.com/rflpazini/kvasir/internal/cache"
	"github.com/rflpazini/kvasir/internal/flaresolverr"
	apphttp "github.com/rflpazini/kvasir/internal/http"
	"github.com/rflpazini/kvasir/internal/observability"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}

	if err := run(); err != nil {
		// Logger may not be ready in early boot; fall back to stderr.
		os.Stderr.WriteString("fatal: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	logger := observability.NewLogger(getenv("LOG_LEVEL", "info"))

	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	registry := adapter.NewRegistry()
	registry.Register(adapter.NewBoitorrent(nil))
	registry.Register(adapter.NewTorrentDosFilmes(nil))

	if fsURL := os.Getenv("FLARESOLVERR_URL"); fsURL != "" {
		solver := flaresolverr.New(fsURL, nil)
		probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := solver.Health(probeCtx); err != nil {
			logger.Warn("flaresolverr unreachable, comando adapter disabled",
				"url", fsURL, "err", err.Error())
		} else {
			registry.Register(adapter.NewComando(solver))
			logger.Info("flaresolverr ok, comando adapter registered", "url", fsURL)
		}
		cancel()
	} else {
		logger.Warn("FLARESOLVERR_URL not set, comando adapter disabled")
	}

	var cacheClient *cache.Client
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		cacheClient = cache.New(cache.Config{Addr: addr})
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := cacheClient.Ping(pingCtx); err != nil {
			logger.Warn("redis unavailable, running without cache", "addr", addr, "err", err.Error())
			cacheClient = nil
		}
		cancel()
	} else {
		logger.Warn("REDIS_ADDR not set, running without cache")
	}

	// Per-adapter timeout: bumped to 30s because FlareSolverr cold-solves of
	// Cloudflare-fronted sites (eg comando.la) routinely take 15–25s on the
	// first request after a session expiry. Direct-HTTP adapters return well
	// within 2s, so the higher ceiling does not affect the warm path.
	agg := aggregator.New(registry, 30*time.Second, metrics)

	srv := apphttp.NewServer(apphttp.Config{
		StaticDir:            getenv("STATIC_DIR", "web/static"),
		EnableDebugEndpoints: apphttp.EnableDebugFromEnv(),
	}, apphttp.Deps{
		Logger:     logger,
		Metrics:    metrics,
		Registry:   registry,
		Aggregator: agg,
		Cache:      cacheClient,
		PromGather: reg,
	})

	addr := getenv("LISTEN_ADDR", ":8080")
	logger.Info("kvasir starting", "addr", addr)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Start(addr); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		logger.Info("shutdown signal received", "signal", sig.String())
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// runHealthcheck pings the local /healthz and returns a process exit code
// suitable for Docker HEALTHCHECK directives. Designed to ship in scratch images.
func runHealthcheck() int {
	addr := getenv("HEALTHCHECK_URL", "http://localhost:8080/healthz")
	client := &stdhttp.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(addr)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		return 1
	}
	return 0
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
