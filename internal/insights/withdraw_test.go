package insights

import (
	"reflect"
	"testing"
	"time"
)

// A tip is the quiet early form of an alert. When the alert that covers a tip is active for
// the tip's session, the tip steps aside: the user should not hear the same thing twice, once
// quietly and once loudly. The covering kinds are radar's own (internal/radar/alerts): a loop
// alert covers the failing-tool and repeated-command tips, a context alert covers the
// checkpoint tip. The stuck alert fires long before the subagent_silent tip would, so it
// covers nothing.

func TestDetectWithdrawsCoveredTips(t *testing.T) {
	goTestFail := goTestFails(3, "FAIL: TestOneCard (0.3s)")
	contextTip := Tip{
		ID:       "context_checkpoint:s1",
		Rule:     RuleContextCheckpoint,
		Session:  "s1",
		Sentence: "Context is at 72% with nothing committed yet. A good point to finish this step and commit.",
		Count:    1,
	}
	subagentTip := Tip{
		ID:       "subagent_silent:s1:agent-1",
		Rule:     RuleSubagentSilent,
		Session:  "s1",
		Sentence: "Subagent `Explore` started 22 min 0 s ago and has not stopped.",
		Count:    1,
		Wait:     22 * time.Minute,
	}
	cases := []struct {
		name string
		in   Input
		want []Tip
	}{
		{
			name: "a loop alert on the session withdraws its failing-tool tip",
			in: Input{
				Now:    fixedNow(),
				Tools:  goTestFail,
				Alerts: []ActiveAlert{{Session: "s1", Kind: "loop"}},
			},
			want: nil,
		},
		{
			name: "a loop alert on another session leaves the failing-tool tip",
			in: Input{
				Now:    fixedNow(),
				Tools:  goTestFail,
				Alerts: []ActiveAlert{{Session: "s2", Kind: "loop"}},
			},
			want: []Tip{goTestFailTip(3, failBase())},
		},
		{
			name: "a loop alert on the session withdraws its repeated-command tip",
			in: Input{
				Now:    fixedNow(),
				Tools:  miseAndGoTest(),
				Alerts: []ActiveAlert{{Session: "s1", Kind: "loop"}},
			},
			want: nil,
		},
		{
			name: "a context alert on the session withdraws its checkpoint tip",
			in: Input{
				Now:         fixedNow(),
				Checkpoints: []Checkpoint{checkpointAt("s1", 72, 5*time.Minute, -1, -1)},
				Alerts:      []ActiveAlert{{Session: "s1", Kind: "context"}},
			},
			want: nil,
		},
		{
			name: "a stuck alert covers nothing: the subagent tip stays",
			in: Input{
				Now: fixedNow(),
				Subagents: []SubagentRun{
					{Session: "s1", Agent: "agent-1", Name: "Explore", Started: fixedNow().Add(-22 * time.Minute)},
				},
				Alerts: []ActiveAlert{{Session: "s1", Kind: "stuck"}},
			},
			want: []Tip{subagentTip},
		},
		{
			name: "no alerts leaves every tip",
			in: Input{
				Now:         fixedNow(),
				Tools:       goTestFail,
				Checkpoints: []Checkpoint{checkpointAt("s1", 72, 5*time.Minute, -1, -1)},
			},
			want: []Tip{contextTip, goTestFailTip(3, failBase())},
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

func TestDetectIsStableAcrossCallsWithAlerts(t *testing.T) {
	in := Input{
		Now:    fixedNow(),
		Tools:  goTestFails(3, "FAIL: TestOneCard (0.3s)"),
		Alerts: []ActiveAlert{{Session: "s2", Kind: "loop"}},
	}
	first := Detect(in)
	second := Detect(in)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() twice = %+v then %+v, want equal", first, second)
	}
}
