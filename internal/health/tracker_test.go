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

	// Healthy if last success was inside the window AND streak < threshold.
	src := health.Source{Name: "a", LastSuccessAt: now, LastStatus: "ok", ConsecutiveFailures: 0}
	if src.Degraded(30*time.Minute, 3) {
		t.Error("fresh success must not be degraded")
	}

	// Degrades on streak ≥ threshold.
	src.ConsecutiveFailures = 3
	if !src.Degraded(30*time.Minute, 3) {
		t.Error("streak == threshold must degrade")
	}

	// Degrades when last success is outside the window even with streak 0.
	src = health.Source{Name: "b", LastSuccessAt: now.Add(-31 * time.Minute), LastStatus: "ok"}
	if !src.Degraded(30*time.Minute, 3) {
		t.Error("stale last success must degrade")
	}

	// Never seen a success → degraded.
	src = health.Source{Name: "c"}
	if !src.Degraded(30*time.Minute, 3) {
		t.Error("zero LastSuccessAt must degrade")
	}
}

func TestTracker_ConcurrentRecord(t *testing.T) {
	tr := health.NewTracker()

	const workers = 50
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				tr.RecordSuccess("a")
			} else {
				tr.RecordFailure("a", "error")
			}
		}()
	}
	wg.Wait()

	// Last call wins; just verify the entry exists and the state is internally
	// consistent (no panic, no negative streak).
	src, ok := tr.Get("a")
	if !ok {
		t.Fatal("a missing")
	}
	if src.ConsecutiveFailures < 0 {
		t.Errorf("streak negative: %d", src.ConsecutiveFailures)
	}
}
