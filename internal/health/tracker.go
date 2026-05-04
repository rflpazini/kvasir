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

// Default thresholds used by Source.Degraded. Co-located with the type
// so the rule lives in one place; the handler references the same
// constants. Tunable later by promoting to a Config field if a second
// caller appears.
const (
	DefaultSuccessWindow   = 30 * time.Minute
	DefaultStreakThreshold = 3
)

// Source is the snapshot view of one adapter's recent history.
type Source struct {
	Name                string    `json:"name"`
	LastStatus          string    `json:"last_status"`
	LastSuccessAt       time.Time `json:"last_success_at,omitempty"`
	LastAttemptAt       time.Time `json:"last_attempt_at,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// Degraded reports whether this source should be presented as unhealthy
// in the UI. The rule is intentionally conservative for a homelab:
//
//   - streakThreshold: degraded after this many failures in a row,
//     regardless of how recent the last success is.
//   - successWindow: degraded if the most recent attempt is much newer
//     than the most recent success (we've been trying and missing).
//
// Idle does NOT degrade. A source that has never been called, or whose
// last call is also its last success, is healthy. The previous "stale
// last success" rule conflated idle with failing — for single-user use
// where a workday or weekend without searching is normal, that fired
// constant false positives and eroded trust in the chips.
func (s Source) Degraded(successWindow time.Duration, streakThreshold int) bool {
	if s.ConsecutiveFailures >= streakThreshold {
		return true
	}
	if s.LastAttemptAt.IsZero() {
		return false
	}
	return s.LastAttemptAt.Sub(s.LastSuccessAt) > successWindow
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
	lastAttemptAt       time.Time
	consecutiveFailures int
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{state: make(map[string]*entry), now: time.Now}
}

// RecordSuccess marks the source as healthy: status=ok, streak=0,
// LastSuccessAt and LastAttemptAt set to now.
func (t *Tracker) RecordSuccess(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(name)
	now := t.now()
	e.lastStatus = "ok"
	e.lastSuccessAt = now
	e.lastAttemptAt = now
	e.consecutiveFailures = 0
}

// RecordFailure stamps the source with the failure status, bumps the
// streak, and updates LastAttemptAt — but not LastSuccessAt — so the
// gap between attempt and last success drives the staleness check.
func (t *Tracker) RecordFailure(name, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(name)
	e.lastStatus = status
	e.lastAttemptAt = t.now()
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
		LastAttemptAt:       e.lastAttemptAt,
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
			LastAttemptAt:       e.lastAttemptAt,
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
