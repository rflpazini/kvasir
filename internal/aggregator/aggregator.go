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
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rflpazini/kvasir/internal/adapter"
	"github.com/rflpazini/kvasir/internal/model"
)

// Aggregator fans out a query to every registered adapter and collapses the
// outcomes into a single SearchResponse.
type Aggregator struct {
	registry       *adapter.Registry
	adapterTimeout time.Duration
}

// New creates an Aggregator. adapterTimeout caps the time any single adapter
// has to return results; siblings keep running.
func New(registry *adapter.Registry, adapterTimeout time.Duration) *Aggregator {
	if adapterTimeout <= 0 {
		adapterTimeout = 8 * time.Second
	}
	return &Aggregator{
		registry:       registry,
		adapterTimeout: adapterTimeout,
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

			res, err := op(scrapeCtx, ad)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				stats[ad.Name()] = model.SourceStat{
					Status:   classifyStatus(err),
					ErrorMsg: err.Error(),
				}
				return nil // best-effort: never cancel siblings
			}

			stats[ad.Name()] = model.SourceStat{
				Count:  len(res),
				Status: model.StatusOK,
			}
			results = append(results, res...)
			return nil
		})
	}
	_ = g.Wait()

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
