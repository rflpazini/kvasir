package health_test

import (
	"sync"
	"testing"
	"time"

	"github.com/rflpazini/kvasir/internal/health"
)

func TestTracker_RecordSuccessSeedsHealthyState(t *testing.T) {
	tr := health.NewTracker()
	before := time.Now()
	tr.RecordSuccess("boitorrent")

	src, ok := tr.Get("boitorrent")
	if !ok {
		t.Fatal("expected boitorrent in tracker")
	}
	if src.LastStatus != "ok" {
		t.Errorf("status = %q, want ok", src.LastStatus)
	}
	if src.ConsecutiveFailures != 0 {
		t.Errorf("streak = %d, want 0", src.ConsecutiveFailures)
	}
	if src.LastSuccessAt.IsZero() || src.LastSuccessAt.Before(before) {
		t.Errorf("LastSuccessAt = %v, want >= %v", src.LastSuccessAt, before)
	}
}

func TestTracker_RecordFailureBumpsStreakWithoutTouchingLastSuccess(t *testing.T) {
	tr := health.NewTracker()
	tr.RecordSuccess("a")
	first, _ := tr.Get("a")

	tr.RecordFailure("a", "timeout")
	tr.RecordFailure("a", "timeout")

	src, _ := tr.Get("a")
	if src.LastStatus != "timeout" {
		t.Errorf("status = %q, want timeout", src.LastStatus)
	}
	if src.ConsecutiveFailures != 2 {
		t.Errorf("streak = %d, want 2", src.ConsecutiveFailures)
	}
	if !src.LastSuccessAt.Equal(first.LastSuccessAt) {
		t.Errorf("LastSuccessAt mutated by failures: %v != %v", src.LastSuccessAt, first.LastSuccessAt)
	}
}

func TestTracker_SuccessResetsStreak(t *testing.T) {
	tr := health.NewTracker()
	tr.RecordFailure("a", "error")
	tr.RecordFailure("a", "error")
	tr.RecordSuccess("a")

	src, _ := tr.Get("a")
	if src.ConsecutiveFailures != 0 {
		t.Errorf("streak = %d, want 0 after success", src.ConsecutiveFailures)
	}
	if src.LastStatus != "ok" {
		t.Errorf("status = %q, want ok", src.LastStatus)
	}
}

func TestTracker_SnapshotIsSortedByName(t *testing.T) {
	tr := health.NewTracker()
	tr.RecordSuccess("comando")
	tr.RecordSuccess("aaa")
	tr.RecordSuccess("torrentdosfilmes")

	snap := tr.Snapshot()
	if got := len(snap); got != 3 {
		t.Fatalf("len = %d, want 3", got)
	}
	if snap[0].Name != "aaa" || snap[1].Name != "comando" || snap[2].Name != "torrentdosfilmes" {
		t.Errorf("snapshot order = %+v, want aaa < comando < torrentdosfilmes", snap)
	}
}

func TestTracker_Degraded(t *testing.T) {
	now := time.Now()
	const window = 30 * time.Minute
	const streak = 3

	// Healthy when last attempt and last success are equal (every attempt
	// has been a success).
	src := health.Source{Name: "a", LastSuccessAt: now, LastAttemptAt: now, LastStatus: "ok"}
	if src.Degraded(window, streak) {
		t.Error("fresh success must not be degraded")
	}

	// Degrades on streak ≥ threshold even with a recent success.
	src.ConsecutiveFailures = 3
	if !src.Degraded(window, streak) {
		t.Error("streak == threshold must degrade")
	}

	// Degrades when last attempt is recent but last success is older
	// than the window — we have been trying and missing.
	src = health.Source{
		Name:          "b",
		LastSuccessAt: now.Add(-31 * time.Minute),
		LastAttemptAt: now,
		LastStatus:    "error",
	}
	if !src.Degraded(window, streak) {
		t.Error("attempt-success gap > window must degrade")
	}

	// Never observed (zero LastAttemptAt) is NOT degraded — idle is not
	// failing, and the previous false-positive eroded chip trust.
	src = health.Source{Name: "c"}
	if src.Degraded(window, streak) {
		t.Error("untouched source must NOT be degraded (idle != failing)")
	}

	// Idle but successful: last attempt == last success a long time ago
	// must NOT be degraded — no failure since.
	old := now.Add(-2 * time.Hour)
	src = health.Source{Name: "d", LastSuccessAt: old, LastAttemptAt: old, LastStatus: "ok"}
	if src.Degraded(window, streak) {
		t.Error("idle-but-ok must not be degraded")
	}
}

// TestTracker_LifecycleRecoversAfterDegrade locks the rule's "recover"
// direction: a source that has degraded via streak must drop back to
// healthy after the next RecordSuccess.
func TestTracker_LifecycleRecoversAfterDegrade(t *testing.T) {
	tr := health.NewTracker()

	tr.RecordFailure("a", "error")
	tr.RecordFailure("a", "error")
	tr.RecordFailure("a", "error")
	got, _ := tr.Get("a")
	if !got.Degraded(health.DefaultSuccessWindow, health.DefaultStreakThreshold) {
		t.Fatal("3 failures must put source in degraded state")
	}

	tr.RecordSuccess("a")
	got, _ = tr.Get("a")
	if got.Degraded(health.DefaultSuccessWindow, health.DefaultStreakThreshold) {
		t.Errorf("RecordSuccess must clear degraded; got %+v", got)
	}
	if got.ConsecutiveFailures != 0 {
		t.Errorf("streak = %d after recovery, want 0", got.ConsecutiveFailures)
	}
}

// TestTracker_FailureStreakIsConserved proves no increment is lost
// under contention. Run with `go test -race` to back this up with the
// race detector — together they're the actual evidence of safety.
func TestTracker_FailureStreakIsConserved(t *testing.T) {
	tr := health.NewTracker()
	const n = 1000
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			tr.RecordFailure("a", "error")
		}()
	}
	wg.Wait()

	src, _ := tr.Get("a")
	if src.ConsecutiveFailures != n {
		t.Fatalf("streak = %d, want %d (lost increment)", src.ConsecutiveFailures, n)
	}
}

