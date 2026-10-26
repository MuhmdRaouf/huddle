package radartab

import (
	"context"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// TestAgentFactsOfJoinsRadarRowsByBareAgentId: the tab answers one session's subagent facts
// keyed by the tray's bare ids, the agent- spelled row winning when both spellings hold one
// agent, the main agent and other sessions kept out; an unknown session answers an empty map.
func TestAgentFactsOfJoinsRadarRowsByBareAgentId(t *testing.T) {
	env := tempEnv(t)
	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	what := "↳ prompt: scan the tree → 3 files"
	start := time.Now().Add(-time.Hour)
	at := func(minute int) int64 { return start.Add(time.Duration(minute) * time.Minute).UnixMilli() }
	// s1: two main calls, the transcript-spelled agent-a1 twice (the newest carrying a what),
	// the bare a1 once with fewer calls, and another session's subagent
	for _, call := range []shared.RequestRecord{
		{ID: "r-1", SessionID: "s1", AgentID: "main", Model: "test-model", Ts: at(0), Tokens: shared.Tokens{Output: 30}},
		{ID: "r-2", SessionID: "s1", AgentID: "main", Model: "test-model", Ts: at(1), Tokens: shared.Tokens{Output: 40}},
		{ID: "r-3", SessionID: "s1", AgentID: "agent-a1", Model: "test-model", Ts: at(2), Tokens: shared.Tokens{Output: 5}},
		{ID: "r-4", SessionID: "s1", AgentID: "agent-a1", Model: "test-model", Ts: at(3), Tokens: shared.Tokens{Output: 7}, What: &what},
		{ID: "r-5", SessionID: "s1", AgentID: "a1", Model: "test-model", Ts: at(4), Tokens: shared.Tokens{Output: 100}},
		{ID: "r-6", SessionID: "s2", AgentID: "agent-z9", Model: "test-model", Ts: at(5), Tokens: shared.Tokens{Output: 9}},
	} {
		tab.Store.AddRequest(call, nil)
	}
	facts := tab.AgentFactsOf(context.Background(), "s1")
	if len(facts) != 1 {
		t.Fatalf("the facts of s1 = %+v, want a1 alone", facts)
	}
	a1, ok := facts["a1"]
	if !ok {
		t.Fatalf("the facts of s1 = %+v, want a1 keyed by the tray's bare id", facts)
	}
	// the agent- row holds more calls, so its tokens and its newest call win over the bare row's
	if a1.OutputTokens != 12 {
		t.Errorf("a1 output tokens = %d, want the agent-a1 row's 12", a1.OutputTokens)
	}
	if !a1.LastAt.Equal(time.UnixMilli(at(3))) {
		t.Errorf("a1 last activity = %s, want %s", a1.LastAt, time.UnixMilli(at(3)))
	}
	if a1.What != what {
		t.Errorf("a1 what = %q, want %q", a1.What, what)
	}
	if got := tab.AgentFactsOf(context.Background(), "sess-nobody"); got == nil || len(got) != 0 {
		t.Errorf("an unknown session's facts = %+v, want an empty map", got)
	}
}
