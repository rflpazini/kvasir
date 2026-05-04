// Package adapter defines the contract every site scraper implements.
//
// Each adapter is a stateless component that translates a free-text query into
// a list of normalized model.Result values for one specific site. Adapters are
// orchestrated in parallel by the aggregator; failures in one adapter must not
// affect others.
package adapter

import (
	"context"

	"github.com/rflpazini/kvasir/internal/model"
)

// Adapter is the contract every site implementation satisfies.
type Adapter interface {
	// Name returns a stable, lowercase identifier (used in metrics, logs and
	// SourceStat keys). MUST be unique per site.
	Name() string

	// Search executes a single query against the site and returns normalized
	// results. Implementations must respect the context deadline and return
	// ctx.Err() on cancellation.
	Search(ctx context.Context, query string) ([]model.Result, error)

	// HealthCheck performs a cheap probe (typically a HEAD on the homepage)
	// and reports whether the site is reachable. Used by /healthz.
	HealthCheck(ctx context.Context) error
}

// Registry is a simple in-memory map of adapters keyed by Name().
type Registry struct {
	adapters map[string]Adapter
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

// Register adds an adapter. Panics on duplicate name (programmer error, not runtime).
func (r *Registry) Register(a Adapter) {
	if _, exists := r.adapters[a.Name()]; exists {
		panic("adapter: duplicate registration for " + a.Name())
	}
	r.adapters[a.Name()] = a
}

// All returns a snapshot slice of registered adapters in non-deterministic order.
func (r *Registry) All() []Adapter {
	out := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		out = append(out, a)
	}
	return out
}

// Get returns the adapter registered under the given name and a found flag.
func (r *Registry) Get(name string) (Adapter, bool) {
	a, ok := r.adapters[name]
	return a, ok
}
