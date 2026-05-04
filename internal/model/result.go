package model

import "time"

// Result is the normalized output of a single torrent listing across all adapters.
type Result struct {
	Title       string     `json:"title"`
	InfoHash    string     `json:"info_hash,omitempty"`
	MagnetLink  string     `json:"magnet,omitempty"`
	SizeBytes   int64      `json:"size_bytes"`
	Seeders     int        `json:"seeders"`
	Leechers    int        `json:"leechers"`
	Category    string     `json:"category,omitempty"`
	Source      string     `json:"source"`
	Quality     Quality    `json:"quality,omitempty"`
	PosterURL   string     `json:"poster_url,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	DetailURL   string     `json:"detail_url"`
}

// SourceStat describes the outcome of a single adapter for a given query.
type SourceStat struct {
	Count    int    `json:"count"`
	Status   string `json:"status"`
	ErrorMsg string `json:"error,omitempty"`
}

// SourceStatus values for SourceStat.Status.
const (
	StatusOK      = "ok"
	StatusError   = "error"
	StatusTimeout = "timeout"
)

// SearchResponse is the aggregated payload returned by the API.
type SearchResponse struct {
	Query       string                `json:"query"`
	Results     []Result              `json:"results"`
	SourceStats map[string]SourceStat `json:"source_stats"`
	DurationMs  int64                 `json:"duration_ms"`
	Cached      bool                  `json:"cached"`
}
