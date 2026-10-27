package radartab

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// TestSubagentsSincePairsStartsAndStops: a start and its stop pair into one run, a start with
// no stop in the page keeps a zero Stopped, the start's label is the name, events of other
// kinds and other sessions stay out, a stop whose start fell outside the page stays out, and a
// dead owner answers none.
func TestSubagentsSincePairsStartsAndStops(t *testing.T) {
	env := tempEnv(t)
	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	start := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	ts := func(msAfterStart int64) string {
		return time.UnixMilli(start.UnixMilli() + msAfterStart).UTC().Format(time.RFC3339Nano)
	}
	sid, aid, other := "s1", "w1", "s2"
	agent := func(id string) *string { return &id }
	// the paired run, the lone start and another session's start, plus one event of a kind the
	// reader leaves out
	for _, line := range []shared.SpoolLine{
		{Ts: ts(0), Event: "SubagentStart", SessionID: &sid, AgentID: &aid, AgentType: strPtr("Explore")},
		{Ts: ts(1 * 60_000), Event: "SubagentStart", SessionID: &sid, AgentID: agent("w2"), AgentType: strPtr("Task")},
		{Ts: ts(2 * 60_000), Event: "PreToolUse", SessionID: &sid, ToolName: strPtr("Bash")},
		{Ts: ts(3 * 60_000), Event: "SubagentStart", SessionID: &other, AgentID: agent("w3"), AgentType: strPtr("Plan")},
		{Ts: ts(5 * 60_000), Event: "SubagentStop", SessionID: &sid, AgentID: &aid},
	} {
		tab.Store.AddSpoolLine(line)
	}

	runs := tab.SubagentsSince(context.Background(), "", start.Add(-time.Minute))
	if len(runs) != 3 {
		t.Fatalf("the subagents = %+v, want the three starts", runs)
	}
	if runs[0].Session != "s1" || runs[0].Agent != "w1" || runs[0].Name != "Explore" {
		t.Errorf("run 0 = %+v, want s1/w1 named Explore", runs[0])
	}
	if !runs[0].Started.Equal(start) || !runs[0].Stopped.Equal(start.Add(5*time.Minute)) {
		t.Errorf("run 0 ran %s..%s, want %s..%s: the start and its stop pair",
			runs[0].Started, runs[0].Stopped, start, start.Add(5*time.Minute))
	}
	if runs[1].Agent != "w2" || runs[1].Name != "Task" || !runs[1].Stopped.IsZero() {
		t.Errorf("run 1 = %+v, want w2 named Task with no stop", runs[1])
	}
	if runs[2].Agent != "w3" || runs[2].Session != "s2" || !runs[2].Stopped.IsZero() {
		t.Errorf("run 2 = %+v, want s2/w3 with no stop", runs[2])
	}
	if !runs[0].Started.Before(runs[1].Started) || !runs[1].Started.Before(runs[2].Started) {
		t.Errorf("the runs answer %s then %s then %s, want oldest start first",
			runs[0].Started, runs[1].Started, runs[2].Started)
	}

	mine := tab.SubagentsSince(context.Background(), "s1", start.Add(-time.Minute))
	if len(mine) != 2 || mine[0].Agent != "w1" || mine[1].Agent != "w2" {
		t.Errorf("s1's subagents = %+v, want w1 and w2 alone", mine)
	}

	// the page cut at two minutes leaves w1's stop without its start: the stop stays out
	later := tab.SubagentsSince(context.Background(), "", start.Add(2*time.Minute))
	if len(later) != 1 || later[0].Agent != "w3" || !later[0].Stopped.IsZero() {
		t.Errorf("the later page = %+v, want w3's start alone", later)
	}

	gone := &Tab{target: "127.0.0.1:1", client: &http.Client{Timeout: probeTimeout}}
	if dead := gone.SubagentsSince(context.Background(), "", start.Add(-time.Minute)); len(dead) != 0 {
		t.Errorf("a dead owner's subagents = %+v, want none", dead)
	}
}
