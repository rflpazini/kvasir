// Package aggregator orchestrates parallel scrapes across registered adapters
// and produces a single normalized SearchResponse.
//
// Failures in one adapter never derail the others: errgroup returns nil from
// each scrape goroutine and per-source outcomes flow through SourceStats
// instead of the aggregate error.
package aggregator

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rflpazini/kvasir/internal/adapter"
	"github.com/rflpazini/kvasir/internal/health"
	"github.com/rflpazini/kvasir/internal/model"
	"github.com/rflpazini/kvasir/internal/observability"
)

// Aggregator fans out a query to every registered adapter and collapses the
// outcomes into a single SearchResponse.
type Aggregator struct {
	registry       *adapter.Registry
	adapterTimeout time.Duration
	metrics        *observability.Metrics
	tracker        *health.Tracker
}

// New creates an Aggregator. adapterTimeout caps the time any single adapter
// has to return results; siblings keep running. Metrics and tracker are
// optional — pass nil in tests that do not exercise observability.
func New(registry *adapter.Registry, adapterTimeout time.Duration, metrics *observability.Metrics, tracker *health.Tracker) *Aggregator {
	if adapterTimeout <= 0 {
		adapterTimeout = 8 * time.Second
	}
	return &Aggregator{
		registry:       registry,
		adapterTimeout: adapterTimeout,
		metrics:        metrics,
		tracker:        tracker,
	}
}

// Search runs the query across all adapters in parallel and returns the
// normalized aggregate. Cached is left to the caller (the HTTP handler).
func (a *Aggregator) Search(ctx context.Context, query string) model.SearchResponse {
	resp := a.fanOut(ctx, func(ctx context.Context, ad adapter.Adapter) ([]model.Result, error) {
		return ad.Search(ctx, query)
	})
	resp.Query = query
	return resp
}

// Recent fans out to every adapter's Recent() to build the "Lançamentos"
// view. Same per-source resilience as Search: failures and timeouts are
// recorded in SourceStats without aborting siblings.
func (a *Aggregator) Recent(ctx context.Context) model.SearchResponse {
	return a.fanOut(ctx, func(ctx context.Context, ad adapter.Adapter) ([]model.Result, error) {
		return ad.Recent(ctx)
	})
}

// fanOut runs `op` against every registered adapter in parallel, capturing
// per-source stats and merging the results. Used by both Search and Recent.
func (a *Aggregator) fanOut(ctx context.Context, op func(context.Context, adapter.Adapter) ([]model.Result, error)) model.SearchResponse {
	start := time.Now()
	adapters := a.registry.All()

	var (
		mu      sync.Mutex
		results = make([]model.Result, 0, 32)
		stats   = make(map[string]model.SourceStat, len(adapters))
	)

	g, gctx := errgroup.WithContext(ctx)
	for _, ad := range adapters {
		ad := ad
		g.Go(func() error {
			scrapeCtx, cancel := context.WithTimeout(gctx, a.adapterTimeout)
			defer cancel()

			scrapeStart := time.Now()
			res, err := op(scrapeCtx, ad)
			elapsed := time.Since(scrapeStart).Seconds()

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				status := classifyStatus(err)
				stats[ad.Name()] = model.SourceStat{
					Status:   status,
					ErrorMsg: err.Error(),
				}
				a.observeError(ad.Name(), status, elapsed)
				return nil // best-effort: never cancel siblings
			}

			stats[ad.Name()] = model.SourceStat{
				Count:  len(res),
				Status: model.StatusOK,
			}
			a.observeSuccess(ad.Name(), elapsed, len(res))
			results = append(results, res...)
			return nil
		})
	}
	_ = g.Wait()

	// Stable order — adapters race to completion, so the goroutine append
	// order would otherwise scramble the response. Primary key is Source;
	// secondary key DetailURL guards future adapters that might fan out
	// internally (eg parallel detail fetches) and lose their own ordering.
	// Two identical requests therefore return byte-equal payloads (cache
	// observability + reproducible test fixtures).
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Source != results[j].Source {
			return results[i].Source < results[j].Source
		}
		return results[i].DetailURL < results[j].DetailURL
	})

	return model.SearchResponse{
		Results:     results,
		SourceStats: stats,
		DurationMs:  time.Since(start).Milliseconds(),
	}
}

func classifyStatus(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return model.StatusTimeout
	}
	return model.StatusError
}

// observeSuccess emits the per-source metrics for a healthy scrape, resets
// the consecutive-failures gauge, and stamps the health tracker.
//
// Order matters: metrics first (lock-free atomic increments via
// client_golang) and tracker last (RWMutex). A future observer should
// be slotted at the end of the chain to keep the cheapest writes ahead
// of the locking ones.
func (a *Aggregator) observeSuccess(adapter string, elapsed float64, count int) {
	if a.metrics != nil {
		a.metrics.ScrapeDuration.WithLabelValues(adapter, model.StatusOK).Observe(elapsed)
		a.metrics.ResultsReturned.WithLabelValues(adapter).Observe(float64(count))
		a.metrics.ConsecutiveFailures.WithLabelValues(adapter).Set(0)
	}
	if a.tracker != nil {
		a.tracker.RecordSuccess(adapter)
	}
}

// observeError emits the per-source metrics for a failed scrape, bumps the
// consecutive-failures gauge, and tells the health tracker. Same
// metrics-first-tracker-last ordering as observeSuccess.
func (a *Aggregator) observeError(adapter, status string, elapsed float64) {
	if a.metrics != nil {
		a.metrics.ScrapeDuration.WithLabelValues(adapter, status).Observe(elapsed)
		a.metrics.ScrapeErrors.WithLabelValues(adapter, status).Inc()
		a.metrics.ConsecutiveFailures.WithLabelValues(adapter).Inc()
	}
	if a.tracker != nil {
		a.tracker.RecordFailure(adapter, status)
	}
}
