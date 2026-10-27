package insights

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// A turn strictly past 45 minutes that has edited at least one file and committed none of it
// earns one tip: commit what works. A commit after the turn started already banked its work,
// and a turn with no edit has nothing to commit.

// turnAt is one session's open turn started ago before fixedNow, with edits distinct files
// edited and commitAge the newest commit's age (-1: none).
func turnAt(session string, startedAgo time.Duration, edits int, commitAge time.Duration) Turn {
	turn := Turn{Session: session, StartedAt: fixedNow().Add(-startedAgo), Edits: edits}
	if commitAge >= 0 {
		turn.LastCommitAt = fixedNow().Add(-commitAge)
	}
	return turn
}

// longTurnID is the tip id of one session's turn started ago before fixedNow.
func longTurnID(session string, startedAgo time.Duration) string {
	return fmt.Sprintf("long_turn:%s:%d", session, fixedNow().Add(-startedAgo).Unix())
}

func TestDetectLongTurn(t *testing.T) {
	tests := []struct {
		name  string
		turns []Turn
		want  []Tip
	}{
		{
			name:  "a turn 50 minutes old with 9 edits and no commit",
			turns: []Turn{turnAt("s1", 50*time.Minute, 9, -1)},
			want: []Tip{{
				ID:       longTurnID("s1", 50*time.Minute),
				Rule:     RuleLongTurn,
				Session:  "s1",
				Sentence: "This turn has edited 9 files over 50 min with no commit. A good point to commit what works.",
				Count:    9,
			}},
		},
		{
			name:  "one edit reads as one file",
			turns: []Turn{turnAt("s1", 50*time.Minute, 1, -1)},
			want: []Tip{{
				ID:       longTurnID("s1", 50*time.Minute),
				Rule:     RuleLongTurn,
				Session:  "s1",
				Sentence: "This turn has edited 1 file over 50 min with no commit. A good point to commit what works.",
				Count:    1,
			}},
		},
		{
			name:  "45 minutes exactly is no tip",
			turns: []Turn{turnAt("s1", 45*time.Minute, 9, -1)},
			want:  nil,
		},
		{
			name:  "30 minutes is no tip",
			turns: []Turn{turnAt("s1", 30*time.Minute, 9, -1)},
			want:  nil,
		},
		{
			name:  "a commit 10 minutes ago says the turn already banked its work",
			turns: []Turn{turnAt("s1", 50*time.Minute, 9, 10*time.Minute)},
			want:  nil,
		},
		{
			name:  "a turn with no edits has nothing to commit",
			turns: []Turn{turnAt("s1", 50*time.Minute, 0, -1)},
			want:  nil,
		},
		{
			name: "two sessions over give two tips in id order",
			turns: []Turn{
				turnAt("s-b", 60*time.Minute, 2, -1),
				turnAt("s-a", 50*time.Minute, 1, -1),
			},
			want: []Tip{
				{
					ID:       longTurnID("s-a", 50*time.Minute),
					Rule:     RuleLongTurn,
					Session:  "s-a",
					Sentence: "This turn has edited 1 file over 50 min with no commit. A good point to commit what works.",
					Count:    1,
				},
				{
					ID:       longTurnID("s-b", 60*time.Minute),
					Rule:     RuleLongTurn,
					Session:  "s-b",
					Sentence: "This turn has edited 2 files over 60 min with no commit. A good point to commit what works.",
					Count:    2,
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(Input{Now: fixedNow(), Turns: tc.turns})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Detect() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDetectIsStableAcrossCallsLongTurn(t *testing.T) {
	in := Input{Now: fixedNow(), Turns: []Turn{turnAt("s1", 50*time.Minute, 9, -1)}}
	first := Detect(in)
	second := Detect(in)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() twice = %+v then %+v, want equal", first, second)
	}
}
