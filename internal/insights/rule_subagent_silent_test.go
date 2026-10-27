package insights

import (
	"reflect"
	"testing"
	"time"
)

func TestDetectSubagentSilent(t *testing.T) {
	running := func(session, agent, name string, startedAgo time.Duration) SubagentRun {
		return SubagentRun{Session: session, Agent: agent, Name: name, Started: fixedNow().Add(-startedAgo)}
	}
	cases := []struct {
		name string
		in   Input
		want []Tip
	}{
		{
			name: "a run started 22 minutes ago with no stop event is a tip",
			in: Input{Now: fixedNow(), Subagents: []SubagentRun{
				running("s1", "agent-1", "Explore", 22*time.Minute),
			}},
			want: []Tip{{
				ID:       "subagent_silent:s1:agent-1",
				Rule:     RuleSubagentSilent,
				Session:  "s1",
				Sentence: "Subagent `Explore` started 22 min 0 s ago and has not stopped.",
				Count:    1,
				Wait:     22 * time.Minute,
			}},
		},
		{
			name: "a run that stopped five minutes in is no tip",
			in: Input{Now: fixedNow(), Subagents: []SubagentRun{
				{Session: "s1", Agent: "agent-1", Name: "Explore",
					Started: fixedNow().Add(-22 * time.Minute), Stopped: fixedNow().Add(-17 * time.Minute)},
			}},
			want: nil,
		},
		{
			name: "a run started ten minutes ago is still young",
			in: Input{Now: fixedNow(), Subagents: []SubagentRun{
				running("s1", "agent-1", "Explore", 10*time.Minute),
			}},
			want: nil,
		},
		{
			name: "a run started exactly twenty minutes ago is no tip yet",
			in: Input{Now: fixedNow(), Subagents: []SubagentRun{
				running("s1", "agent-1", "Explore", 20*time.Minute),
			}},
			want: nil,
		},
		{
			name: "an empty name reads the agent id",
			in: Input{Now: fixedNow(), Subagents: []SubagentRun{
				running("s1", "agent-9", "", 45*time.Minute),
			}},
			want: []Tip{{
				ID:       "subagent_silent:s1:agent-9",
				Rule:     RuleSubagentSilent,
				Session:  "s1",
				Sentence: "Subagent `agent-9` started 45 min 0 s ago and has not stopped.",
				Count:    1,
				Wait:     45 * time.Minute,
			}},
		},
		{
			name: "two silent runs give two tips in id order",
			in: Input{Now: fixedNow(), Subagents: []SubagentRun{
				running("s2", "b-agent", "Task", 25*time.Minute),
				running("s1", "a-agent", "Explore", 30*time.Minute),
			}},
			want: []Tip{
				{
					ID:       "subagent_silent:s1:a-agent",
					Rule:     RuleSubagentSilent,
					Session:  "s1",
					Sentence: "Subagent `Explore` started 30 min 0 s ago and has not stopped.",
					Count:    1,
					Wait:     30 * time.Minute,
				},
				{
					ID:       "subagent_silent:s2:b-agent",
					Rule:     RuleSubagentSilent,
					Session:  "s2",
					Sentence: "Subagent `Task` started 25 min 0 s ago and has not stopped.",
					Count:    1,
					Wait:     25 * time.Minute,
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Detect() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDetectIsStableAcrossCallsSubagentSilent(t *testing.T) {
	in := Input{Now: fixedNow(), Subagents: []SubagentRun{
		{Session: "s1", Agent: "agent-1", Name: "Explore", Started: fixedNow().Add(-22 * time.Minute)},
	}}
	first := Detect(in)
	second := Detect(in)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() twice = %+v then %+v, want equal", first, second)
	}
}
