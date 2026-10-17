package ingest_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The watcher's harness wiring: collectors run once per rescanning pass, one failing never
// stops the others, a collector's records land on their session with its cwd, and a record
// collected again lands once.

// fakeHarness is a collector the test programs: records, an error, and how many times it ran.
type fakeHarness struct {
	records []shared.RequestRecord
	err     error
	calls   atomic.Int64
}

func (f *fakeHarness) Collect(ctx context.Context, sinceCutoff int64) ([]shared.RequestRecord, error) {
	f.calls.Add(1)
	return f.records, f.err
}

// harnessWatcher starts a watcher over the collectors given, driven by hand like
// manualWatcher but taking the collectors.
func harnessWatcher(t *testing.T, env domain.EnvLookup, st *store.Store, collectors ...ingest.HarnessCollector) func(advanceMs int64) {
	t.Helper()
	var clock atomic.Int64
	clock.Store(watchT0)
	ticker := newFakeTicker()
	w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
		Env:               env,
		Store:             st,
		SinceMs:           watchDay,
		IntervalMs:        3_600_000,
		RescanMs:          5_000,
		Now:               clock.Load,
		Alive:             ownPidAlive,
		NewTicker:         ticker.new,
		HarnessCollectors: collectors,
	})
	t.Cleanup(w.Stop)
	waitFor(t, "initial ingest", 5*time.Second, func() bool { return !w.CatchingUp() })
	return func(advanceMs int64) {
		clock.Add(advanceMs)
		ticker.fire()
		w.PollOnce()
		waitFor(t, "ingest completion", 5*time.Second, func() bool { return !w.CatchingUp() })
	}
}

func TestWatcherHarnessCollectors(t *testing.T) {
	t.Run("lands a collector's records on their session with its cwd", func(t *testing.T) {
		st := newStore()
		collector := &fakeHarness{records: []shared.RequestRecord{{
			ID: "codex:cx1:0", SessionID: "cx1", AgentID: "main", Model: "gpt-5-codex",
			Ts: watchT0 + 1, Tokens: shared.Tokens{Input: 800, Output: 300, CacheRead: 200},
			Harness: shared.HarnessCodex, Cwd: ptr("/w/app"),
		}}}
		tick := harnessWatcher(t, makeEnv(t).env, st, collector)
		requests := st.Requests(store.RequestFilter{})
		if len(requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(requests))
		}
		if requests[0].Harness != shared.HarnessCodex {
			t.Errorf("harness = %q, want codex", requests[0].Harness)
		}
		view := st.SessionDetail("cx1")
		if view == nil || view.Cwd == nil || *view.Cwd != "/w/app" {
			t.Errorf("session = %+v, want one whose cwd is /w/app", view)
		}
		// a rescanning pass collects again; the same record lands once
		tick(6_000)
		if got := len(st.Requests(store.RequestFilter{})); got != 1 {
			t.Errorf("requests after the second pass = %d, want 1", got)
		}
		if collector.calls.Load() != 2 {
			t.Errorf("collect calls = %d, want one per pass", collector.calls.Load())
		}
	})

	t.Run("a failing collector never stops the others", func(t *testing.T) {
		st := newStore()
		broken := &fakeHarness{err: errors.New("boom")}
		working := &fakeHarness{records: []shared.RequestRecord{{
			ID: "opencode:oc1:0", SessionID: "oc1", AgentID: "main", Model: "glm-5.3",
			Ts: watchT0 + 2, Tokens: shared.Tokens{Input: 10, Output: 5},
			Harness: shared.HarnessOpencode,
		}}}
		harnessWatcher(t, makeEnv(t).env, st, broken, working)
		requests := st.Requests(store.RequestFilter{})
		if len(requests) != 1 || requests[0].Harness != shared.HarnessOpencode {
			t.Errorf("requests = %+v, want the working collector's one opencode record", requests)
		}
	})

	t.Run("a watcher without collectors reads nothing extra", func(t *testing.T) {
		st := newStore()
		harnessWatcher(t, makeEnv(t).env, st)
		if got := len(st.Requests(store.RequestFilter{})); got != 0 {
			t.Errorf("requests = %d, want none", got)
		}
	})
}
