// Package health tracks per-source operational state — last successful
// scrape, current streak of failures, last status — so /healthz can
// expose more than "the adapter is registered". The frontend uses this
// to fade chips when a source has been silent or failing for too long,
// shifting trust from "the tool is broken" to "the upstream is dead."
package health

import (
	"sort"
	"sync"
	"time"
)

// Source is the snapshot view of one adapter's recent history.
type Source struct {
	Name                string    `json:"name"`
	LastStatus          string    `json:"last_status"`
	LastSuccessAt       time.Time `json:"last_success_at,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// Degraded reports whether this source should be presented as unhealthy
// in the UI. The two thresholds tune sensitivity:
//
//   - successWindow: how stale the last success can be before we fade
//     the chip even if no recent error fired (eg an adapter that has
//     not been called in a while).
//   - streakThreshold: how many failures in a row qualify as degraded
//     even if the last success was technically recent.
//
// A zero LastSuccessAt (never observed a success) is always degraded.
func (s Source) Degraded(successWindow time.Duration, streakThreshold int) bool {
	if s.ConsecutiveFailures >= streakThreshold {
		return true
	}
	if s.LastSuccessAt.IsZero() {
		return true
	}
	return time.Since(s.LastSuccessAt) > successWindow
}

// Tracker is a thread-safe in-memory store of per-source state.
type Tracker struct {
	mu    sync.RWMutex
	state map[string]*entry
	now   func() time.Time // injectable for tests
}

type entry struct {
	lastStatus          string
	lastSuccessAt       time.Time
	consecutiveFailures int
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{state: make(map[string]*entry), now: time.Now}
}

// RecordSuccess marks the source as healthy: status=ok, streak=0,
// LastSuccessAt=now.
func (t *Tracker) RecordSuccess(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(name)
	e.lastStatus = "ok"
	e.lastSuccessAt = t.now()
	e.consecutiveFailures = 0
}

// RecordFailure stamps the source with the failure status, bumps the
// streak, and leaves LastSuccessAt untouched.
func (t *Tracker) RecordFailure(name, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(name)
	e.lastStatus = status
	e.consecutiveFailures++
}

// Get returns a snapshot of the source by name, or false if untouched.
func (t *Tracker) Get(name string) (Source, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	e, ok := t.state[name]
	if !ok {
		return Source{}, false
	}
	return Source{
		Name:                name,
		LastStatus:          e.lastStatus,
		LastSuccessAt:       e.lastSuccessAt,
		ConsecutiveFailures: e.consecutiveFailures,
	}, true
}

// Snapshot returns the full state sorted by name. Each Source is a copy,
// safe for the caller to mutate.
func (t *Tracker) Snapshot() []Source {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Source, 0, len(t.state))
	for name, e := range t.state {
		out = append(out, Source{
			Name:                name,
			LastStatus:          e.lastStatus,
			LastSuccessAt:       e.lastSuccessAt,
			ConsecutiveFailures: e.consecutiveFailures,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (t *Tracker) entryLocked(name string) *entry {
	e, ok := t.state[name]
	if !ok {
		e = &entry{}
		t.state[name] = e
	}
	return e
}
