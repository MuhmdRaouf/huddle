package insights

import (
	"reflect"
	"testing"
	"time"
)

// A session past 70% context with no commit and no handoff digest in 30 minutes earns one
// tip: finish the step and commit. Past 70% means strictly past, a commit or digest in the
// last 30 minutes says the session is already wrapping up, and a session that has not
// reported in 2 hours is not live enough to nudge.

// checkpointAt is one session's checkpoint seen from fixedNow.
func checkpointAt(session string, percent float64, eventAge, commitAge, handoffAge time.Duration) Checkpoint {
	cp := Checkpoint{
		Session:        session,
		ContextPercent: percent,
		LastEventAt:    fixedNow().Add(-eventAge),
	}
	if commitAge >= 0 {
		cp.LastCommitAt = fixedNow().Add(-commitAge)
	}
	if handoffAge >= 0 {
		cp.LastHandoffAt = fixedNow().Add(-handoffAge)
	}
	return cp
}

func TestDetectContextCheckpoint(t *testing.T) {
	tests := []struct {
		name        string
		checkpoints []Checkpoint
		want        []Tip
	}{
		{
			name:        "69 percent is no tip",
			checkpoints: []Checkpoint{checkpointAt("s1", 69, 5*time.Minute, -1, -1)},
			want:        nil,
		},
		{
			name:        "70 percent exactly is no tip",
			checkpoints: []Checkpoint{checkpointAt("s1", 70, 5*time.Minute, -1, -1)},
			want:        nil,
		},
		{
			name:        "a commit 10 minutes ago says the session is already wrapping up",
			checkpoints: []Checkpoint{checkpointAt("s1", 72, 5*time.Minute, 10*time.Minute, -1)},
			want:        nil,
		},
		{
			name:        "a commit 45 minutes ago leaves the window stale",
			checkpoints: []Checkpoint{checkpointAt("s1", 72, 5*time.Minute, 45*time.Minute, -1)},
			want: []Tip{{
				ID:       "context_checkpoint:s1",
				Rule:     RuleContextCheckpoint,
				Session:  "s1",
				Sentence: "Context is at 72% with nothing committed in 45 min. A good point to finish this step and commit.",
				Count:    1,
			}},
		},
		{
			name:        "a handoff digest 10 minutes ago says the step is already handed off",
			checkpoints: []Checkpoint{checkpointAt("s1", 72, 5*time.Minute, -1, 10*time.Minute)},
			want:        nil,
		},
		{
			name:        "no commit and no digest at all is the plain tip",
			checkpoints: []Checkpoint{checkpointAt("s1", 72, 5*time.Minute, -1, -1)},
			want: []Tip{{
				ID:       "context_checkpoint:s1",
				Rule:     RuleContextCheckpoint,
				Session:  "s1",
				Sentence: "Context is at 72% with nothing committed yet. A good point to finish this step and commit.",
				Count:    1,
			}},
		},
		{
			name:        "a session silent for 3 hours is not live",
			checkpoints: []Checkpoint{checkpointAt("s1", 72, 3*time.Hour, -1, -1)},
			want:        nil,
		},
		{
			name: "two sessions over give two tips in id order",
			checkpoints: []Checkpoint{
				checkpointAt("s-b", 90, 5*time.Minute, -1, -1),
				checkpointAt("s-a", 85, 5*time.Minute, 40*time.Minute, -1),
			},
			want: []Tip{
				{
					ID:       "context_checkpoint:s-a",
					Rule:     RuleContextCheckpoint,
					Session:  "s-a",
					Sentence: "Context is at 85% with nothing committed in 40 min. A good point to finish this step and commit.",
					Count:    1,
				},
				{
					ID:       "context_checkpoint:s-b",
					Rule:     RuleContextCheckpoint,
					Session:  "s-b",
					Sentence: "Context is at 90% with nothing committed yet. A good point to finish this step and commit.",
					Count:    1,
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(Input{Now: fixedNow(), Checkpoints: tc.checkpoints})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Detect() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
