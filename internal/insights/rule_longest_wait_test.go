package insights

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// answeredItem is one needs-you item in a session, decided wait after the hub raised it. The
// clock is a Sunday, so a creation 5 days back falls on a Tuesday.
func answeredItem(session, kind string, createdDaysAgo int, wait time.Duration) Item {
	created := fixedNow().Add(-time.Duration(createdDaysAgo) * 24 * time.Hour)
	return Item{Session: session, Kind: kind, CreatedAt: created, DecidedAt: created.Add(wait)}
}

// openItem is one needs-you item in a session that nobody has answered.
func openItem(session, kind string, createdDaysAgo int) Item {
	return Item{Session: session, Kind: kind, CreatedAt: fixedNow().Add(-time.Duration(createdDaysAgo) * 24 * time.Hour)}
}

// waitTipID is the id of the tip for one item: the rule, the session and the item's creation.
func waitTipID(session string, createdDaysAgo int) string {
	created := fixedNow().Add(-time.Duration(createdDaysAgo) * 24 * time.Hour)
	return fmt.Sprintf("longest_wait:%s:%d", session, created.Unix())
}

func TestDetectLongestWait(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want []Tip
	}{
		{
			name: "three answered items this week, the longest wait wins",
			in: Input{Now: fixedNow(), Items: []Item{
				answeredItem("s1", "turn", 2, 5*time.Minute),
				answeredItem("s7", "permission", 5, 2*time.Hour+10*time.Minute),
				answeredItem("s2", "question", 1, 40*time.Minute),
			}},
			want: []Tip{{
				ID:       waitTipID("s7", 5),
				Rule:     RuleLongestWait,
				Session:  "s7",
				Sentence: "A permission ask in s7 waited 2 h 10 min on Tuesday. Pair your phone to answer from it.",
				Count:    1,
				Wait:     2*time.Hour + 10*time.Minute,
			}},
		},
		{
			name: "a longest wait of 25 min is no tip",
			in: Input{Now: fixedNow(), Items: []Item{
				answeredItem("s1", "turn", 1, 20*time.Minute),
				answeredItem("s2", "question", 2, 25*time.Minute),
			}},
			want: nil,
		},
		{
			name: "the longest item is 8 days old, the longest this week waited 12 min",
			in: Input{Now: fixedNow(), Items: []Item{
				answeredItem("s1", "turn", 8, 3*time.Hour),
				answeredItem("s2", "question", 1, 12*time.Minute),
			}},
			want: nil,
		},
		{
			name: "an unanswered item does not count",
			in: Input{Now: fixedNow(), Items: []Item{
				openItem("s1", "permission", 3),
				answeredItem("s2", "question", 1, 45*time.Minute),
			}},
			want: []Tip{{
				ID:       waitTipID("s2", 1),
				Rule:     RuleLongestWait,
				Session:  "s2",
				Sentence: "A question ask in s2 waited 45 min 0 s on Saturday. Pair your phone to answer from it.",
				Count:    1,
				Wait:     45 * time.Minute,
			}},
		},
		{
			name: "two items with the same wait, the older one names the tip",
			in: Input{Now: fixedNow(), Items: []Item{
				answeredItem("s3", "turn", 4, time.Hour),
				answeredItem("s4", "turn", 1, time.Hour),
			}},
			want: []Tip{{
				ID:       waitTipID("s3", 4),
				Rule:     RuleLongestWait,
				Session:  "s3",
				Sentence: "A turn ask in s3 waited 1 h 0 min on Wednesday. Pair your phone to answer from it.",
				Count:    1,
				Wait:     time.Hour,
			}},
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

func TestDetectIsStableAcrossCallsLongestWait(t *testing.T) {
	in := Input{Now: fixedNow(), Items: []Item{
		openItem("s1", "permission", 3),
		answeredItem("s7", "permission", 5, 2*time.Hour+10*time.Minute),
		answeredItem("s3", "turn", 4, time.Hour),
		answeredItem("s4", "turn", 1, time.Hour),
	}}
	first := Detect(in)
	second := Detect(in)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() twice = %+v then %+v, want equal", first, second)
	}
}
