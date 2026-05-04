package observability

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds every Prometheus collector kvasir exports.
// All collectors are registered against the provided Registerer in NewMetrics.
type Metrics struct {
	ScrapeDuration       *prometheus.HistogramVec
	ScrapeErrors         *prometheus.CounterVec
	ResultsReturned      *prometheus.HistogramVec
	CacheHits            prometheus.Counter
	CacheMisses          prometheus.Counter
	RequestDuration      prometheus.Histogram
	ConsecutiveFailures  *prometheus.GaugeVec
	RetryAttempts        *prometheus.CounterVec
	QualityFilterDropped *prometheus.CounterVec
}

// NewMetrics constructs and registers every collector in the kvasir namespace.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		ScrapeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "kvasir",
			Name:      "scrape_duration_seconds",
			Help:      "Time spent scraping a single adapter for a query.",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 4, 8, 16},
		}, []string{"adapter", "status"}),

		ScrapeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "kvasir",
			Name:      "scrape_errors_total",
			Help:      "Total scrape errors broken down by adapter and category.",
		}, []string{"adapter", "error_type"}),

		ResultsReturned: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "kvasir",
			Name:      "results_returned",
			Help:      "Number of normalized results returned by an adapter per query.",
			Buckets:   []float64{0, 1, 5, 10, 25, 50, 100},
		}, []string{"adapter"}),

		CacheHits: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "kvasir",
			Name:      "cache_hits_total",
			Help:      "Total cache hits for /api/search.",
		}),

		CacheMisses: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "kvasir",
			Name:      "cache_misses_total",
			Help:      "Total cache misses for /api/search.",
		}),

		RequestDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "kvasir",
			Name:      "request_duration_seconds",
			Help:      "Total /api/search request duration including aggregation.",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 4, 8, 16},
		}),

		ConsecutiveFailures: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "kvasir",
			Name:      "adapter_consecutive_failures",
			Help:      "Current streak of consecutive failures per adapter (zeroed on success).",
		}, []string{"adapter"}),

		RetryAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "kvasir",
			Name:      "retry_attempts_total",
			Help:      "Number of retry attempts performed per adapter.",
		}, []string{"adapter"}),

		// Bumped when the ?quality= filter contains a token we don't recognize
		// (typo, dropped Quality bucket, malformed URL with empty parts).
		// Reason label distinguishes "unknown" from "empty" so a mis-encoded
		// querystring surfaces separately from a typo.
		QualityFilterDropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "kvasir",
			Name:      "quality_filter_dropped_total",
			Help:      "Tokens dropped from ?quality= because they were not recognized or were empty.",
		}, []string{"reason"}),
	}

	reg.MustRegister(
		m.ScrapeDuration,
		m.ScrapeErrors,
		m.ResultsReturned,
		m.CacheHits,
		m.CacheMisses,
		m.RequestDuration,
		m.ConsecutiveFailures,
		m.RetryAttempts,
		m.QualityFilterDropped,
	)

	return m
}
