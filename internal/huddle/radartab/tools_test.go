package radartab

import (
	"context"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// TestToolRunsOfAnswersASessionsToolCallsOldestFirst: the tab's own API answers one session's
// recorded tool calls oldest first, a call with no input key left out and another session's
// call kept out; an unknown session answers none.
func TestToolRunsOfAnswersASessionsToolCallsOldestFirst(t *testing.T) {
	env := tempEnv(t)
	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	key := "9a11c0de"
	start := time.Now().Add(-time.Hour)
	at := func(minute int) int64 { return start.Add(time.Duration(minute) * time.Minute).UnixMilli() }
	// four calls of one input key for s1, added out of order, one call of theirs with no input
	// key, and one call of s2's
	for _, call := range []shared.ToolCallRecord{
		{ID: "t-1", SessionID: "s1", Name: "Bash", StartedAt: at(3), Ok: true, InputKey: &key},
		{ID: "t-2", SessionID: "s1", Name: "Bash", StartedAt: at(1), Ok: false, InputKey: &key},
		{ID: "t-3", SessionID: "s1", Name: "Bash", StartedAt: at(0), Ok: true, InputKey: &key},
		{ID: "t-4", SessionID: "s1", Name: "Bash", StartedAt: at(2), Ok: true, InputKey: &key},
		{ID: "t-5", SessionID: "s1", Name: "Read", StartedAt: at(4), Ok: true},
		{ID: "t-6", SessionID: "s2", Name: "Bash", StartedAt: at(0), Ok: true, InputKey: &key},
	} {
		tab.Store.AddToolCall(call)
	}
	runs := tab.ToolRunsOf(context.Background(), "s1")
	if len(runs) != 4 {
		t.Fatalf("the tool runs of s1 = %+v, want the four keyed calls", runs)
	}
	for i, run := range runs {
		if run.InputKey != key {
			t.Errorf("run %d = %+v, want the input key %s", i, run, key)
		}
		if run.Name != "Bash" {
			t.Errorf("run %d = %+v, want a Bash call", i, run)
		}
		if run.OK != (i != 1) {
			t.Errorf("run %d ok = %v, want false only for the one failed call", i, run.OK)
		}
		if !run.At.Equal(time.UnixMilli(at(i))) {
			t.Errorf("run %d started at %s, want %s: the runs answer oldest first",
				i, run.At, time.UnixMilli(at(i)))
		}
	}
	if runs := tab.ToolRunsOf(context.Background(), "sess-nobody"); len(runs) != 0 {
		t.Errorf("the tool runs of an unknown session = %+v, want none", runs)
	}
}
