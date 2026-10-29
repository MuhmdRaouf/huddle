// The retention cases that prune the history file, plus the context cases that prove the
// beat ends.
package history

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// retentionEnv is one throwaway world whose settings file says the retention days, plus
// the source that reads it back — the wiring the server gives the schedule.
func retentionEnv(t *testing.T, days int) RetentionSource {
	t.Helper()
	home := t.TempDir()
	env := domain.EnvLookup{"HOME": home, "RADAR_HOME": filepath.Join(home, "state")}
	if err := settings.WriteSettings(t.Context(), env, settings.Settings{
		Notifications: true, HistoryRetentionDays: days,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return func(ctx context.Context) int {
		return settings.ReadSettings(ctx, env).HistoryRetentionDays
	}
}

// allTreeIDs lists every top-level tree, either scope: the prune cases move the clock, so
// live moves to history.
func allTreeIDs(t *testing.T, h *History, now int64) []string {
	t.Helper()
	ids := []string{}
	for _, scope := range []Scope{ScopeLive, ScopeHistory} {
		roots, err := h.Roots(RootsQuery{Scope: scope, Now: now, LiveMs: 15 * 60_000, Limit: 50})
		if err != nil {
			t.Fatalf("read the %s roots: %v", scope, err)
		}
		ids = append(ids, rootIDs(roots)...)
	}
	slices.Sort(ids)
	return ids
}

// pruneNode is the base node the prune cases build on.
func pruneNode(id, sessionID string, lastAt int64) NodeUpsert {
	return NodeUpsert{ID: id, Kind: shared.NodeMain, SessionID: sessionID, AgentID: "main", LastAt: i64p(lastAt)}
}

func TestRetentionPrunes(t *testing.T) {
	t.Run("prunes past the retention at startup and on the hour beat, forever when 0", func(t *testing.T) {
		if cutoff := RetentionCutoff(7, 1_000_000_000); cutoff == nil || *cutoff != 1_000_000_000-7*DayMs {
			t.Errorf("the 7-day cutoff: got %v", cutoff)
		}
		if cutoff := RetentionCutoff(0, 1_000_000_000); cutoff != nil {
			t.Errorf("the 0-day cutoff: got %v, want none", *cutoff)
		}
		if cutoff := RetentionCutoff(-3, 1_000_000_000); cutoff != nil {
			t.Errorf("the negative cutoff: got %v, want none", *cutoff)
		}
		base := int64(1_000_000_000_000)
		h, err := OpenHistory(filepath.Join(t.TempDir(), "prune.db"), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		retention := retentionEnv(t, 7)
		if err := h.UpsertNode(pruneNode("old/main", "old", base-40*DayMs)); err != nil {
			t.Fatalf("upsert the old node: %v", err)
		}
		if err := h.UpsertNode(pruneNode("fresh/main", "fresh", base)); err != nil {
			t.Fatalf("upsert the fresh node: %v", err)
		}
		schedule := SchedulePrunes(t.Context(), h, retention, PruneOptions{IntervalMs: 20, Now: func() int64 { return base }})
		t.Cleanup(schedule.Stop)
		if got := allTreeIDs(t, h, base); !reflect.DeepEqual(got, []string{"fresh/main"}) {
			t.Errorf("pruned once, at startup: got %v", got)
		}
		if err := h.UpsertNode(pruneNode("late/main", "late", base-40*DayMs)); err != nil {
			t.Fatalf("upsert the late node: %v", err)
		}
		time.Sleep(60 * time.Millisecond) // the beat
		if got := allTreeIDs(t, h, base); !reflect.DeepEqual(got, []string{"fresh/main"}) {
			t.Errorf("the hourly beat caught it: got %v", got)
		}
		schedule.Stop()
		if err := h.UpsertNode(pruneNode("late2/main", "late2", base-40*DayMs)); err != nil {
			t.Fatalf("upsert the second late node: %v", err)
		}
		time.Sleep(60 * time.Millisecond)
		if got := allTreeIDs(t, h, base); !reflect.DeepEqual(got, []string{"fresh/main", "late2/main"}) {
			t.Errorf("stopped: nothing prunes any more: got %v", got)
		}
	})

	t.Run("keeps everything when retention is 0", func(t *testing.T) {
		h, err := OpenHistory(filepath.Join(t.TempDir(), "forever.db"), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		retention := retentionEnv(t, 0)
		now := time.Now().UnixMilli()
		if err := h.UpsertNode(pruneNode("old/main", "old", now-400*DayMs)); err != nil {
			t.Fatalf("upsert the old node: %v", err)
		}
		pruned, err := PruneOnce(t.Context(), h, retention, now)
		if err != nil {
			t.Fatalf("prune: %v", err)
		}
		if pruned != 0 {
			t.Errorf("the prune: got %d trees, want 0", pruned)
		}
		roots, err := h.Roots(RootsQuery{Scope: ScopeHistory, Now: now, LiveMs: 15 * 60_000, Limit: 50})
		if err != nil {
			t.Fatalf("read the roots: %v", err)
		}
		if got := rootIDs(roots); !reflect.DeepEqual(got, []string{"old/main"}) {
			t.Errorf("the kept trees: got %v", got)
		}
	})

	t.Run("ends the prune beat when its context ends", func(t *testing.T) {
		base := int64(1_000_000_000_000)
		h, err := OpenHistory(filepath.Join(t.TempDir(), "cancel.db"), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		retention := retentionEnv(t, 7)
		ctx, cancel := context.WithCancel(t.Context())
		schedule := SchedulePrunes(ctx, h, retention, PruneOptions{IntervalMs: 20, Now: func() int64 { return base }})
		t.Cleanup(schedule.Stop)
		cancel()
		select {
		case <-schedule.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the prune beat never ended")
		}
		if err := h.UpsertNode(pruneNode("old2/main", "old2", base-400*DayMs)); err != nil {
			t.Fatalf("upsert the node: %v", err)
		}
		time.Sleep(60 * time.Millisecond)
		if got := allTreeIDs(t, h, base); !reflect.DeepEqual(got, []string{"old2/main"}) {
			t.Errorf("nothing prunes after the context ends: got %v", got)
		}
	})
}

// recordingHealth records what a loop reported, the cases' stand-in for the registry.
type recordingHealth struct {
	mu      sync.Mutex
	records []healthRecord
}

// healthRecord is one report a loop left: the component, and whether its pass was good.
type healthRecord struct {
	name string
	ok   bool
	err  error
}

func (r *recordingHealth) OK(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, healthRecord{name: name, ok: true})
}

func (r *recordingHealth) Fail(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, healthRecord{name: name, err: err})
}

func (r *recordingHealth) snapshot() []healthRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.records)
}

// TestThePruneBeatReportsEachPass: every pass of the prune beat lands its result on health —
// ok after a good pass, failing after a bad one — so a history that stopped pruning shows in
// /health instead of only in silence.
func TestThePruneBeatReportsEachPass(t *testing.T) {
	t.Run("records its first pass ok over a good history", func(t *testing.T) {
		base := int64(1_000_000_000_000)
		h, err := OpenHistory(filepath.Join(t.TempDir(), "beat.db"), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		health := &recordingHealth{}
		schedule := SchedulePrunes(t.Context(), h, retentionEnv(t, 7), PruneOptions{
			IntervalMs: 20,
			Now:        func() int64 { return base },
			Health:     health,
		})
		t.Cleanup(schedule.Stop)
		if got := health.snapshot(); len(got) != 1 || got[0].name != PruneHealthName || !got[0].ok {
			t.Errorf("the first pass reported %v, want one %s ok", got, PruneHealthName)
		}
	})

	t.Run("records its first pass failing over a history closed before the beat", func(t *testing.T) {
		h, err := OpenHistory(filepath.Join(t.TempDir(), "closed.db"), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		retention := retentionEnv(t, 7)
		if err := h.Close(); err != nil {
			t.Fatalf("close the history: %v", err)
		}
		health := &recordingHealth{}
		schedule := SchedulePrunes(t.Context(), h, retention, PruneOptions{IntervalMs: 20, Health: health})
		t.Cleanup(schedule.Stop)
		got := health.snapshot()
		if len(got) != 1 || got[0].name != PruneHealthName || got[0].ok {
			t.Fatalf("the first pass reported %v, want one %s failing", got, PruneHealthName)
		}
		if got[0].err == nil {
			t.Errorf("the failing pass carried no error")
		}
	})
}
