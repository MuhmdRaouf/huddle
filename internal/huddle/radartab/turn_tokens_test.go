package radartab

import (
	"context"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// TestOutputTokensSinceSumsTheMainAgentsOutput: the tab's own API answers one session's main
// agent's output tokens after a moment — the calls before it, a totals record, a subagent's
// call and another session's call all left out; a session with no requests answers (0, true).
func TestOutputTokensSinceSumsTheMainAgentsOutput(t *testing.T) {
	env := tempEnv(t)
	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	start := time.Now().Add(-time.Hour)
	at := func(second int) int64 { return start.Add(time.Duration(second) * time.Second).UnixMilli() }
	request := func(id, session, agent string, second int, output int64, totals bool) {
		tab.Store.AddRequest(shared.RequestRecord{
			ID:        id,
			SessionID: session,
			AgentID:   agent,
			Model:     "test-model",
			Ts:        at(second),
			Tokens:    shared.Tokens{Output: output},
			Totals:    totals,
		}, nil)
	}
	// s1's main agent: one call before since, two after it, and one totals record after it —
	// a totals record repeats a run's total, so counting it would count the run twice
	request("r-1", "s1", "main", -60, 100, false)
	request("r-2", "s1", "main", 10, 200, false)
	request("r-3", "s1", "main", 20, 300, false)
	request("r-4", "s1", "main", 30, 600, true)
	// one subagent call of s1's and one main call of s2's, both after since
	request("r-5", "s1", "agent-a1", 40, 400, false)
	request("r-6", "s2", "main", 50, 500, false)

	since := time.UnixMilli(at(0))
	got, ok := tab.OutputTokensSince(context.Background(), "s1", since)
	if !ok {
		t.Fatal("OutputTokensSince answered not ok")
	}
	if got != 500 {
		t.Errorf("output since = %d, want 500: the two main calls after since alone — the call before it, the totals record, the subagent and the other session left out", got)
	}
	if zero, ok := tab.OutputTokensSince(context.Background(), "sess-nobody", since); !ok || zero != 0 {
		t.Errorf("a session with no requests = (%d, %v), want (0, true)", zero, ok)
	}
}
