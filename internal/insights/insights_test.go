package insights

import (
	"reflect"
	"testing"
	"time"
)

// fixedNow is the clock every case runs at, so the rules stay pure.
func fixedNow() time.Time {
	return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
}

// toolRun is one Bash call in session s1.
func toolRun(key, command string, at time.Time) ToolRun {
	return ToolRun{Session: "s1", Tool: "Bash", InputKey: key, Command: command, OK: true, At: at}
}

// bashAsk is one permission prompt, decided wait after it was created.
func bashAsk(command string, createdDaysAgo int, decision string, wait time.Duration) PermissionAsk {
	created := fixedNow().Add(-time.Duration(createdDaysAgo) * 24 * time.Hour)
	return PermissionAsk{
		Session:   "s1",
		Tool:      "Bash",
		Command:   command,
		Decision:  decision,
		CreatedAt: created,
		DecidedAt: created.Add(wait),
	}
}

// goTestAsks is the five `go test …` prompts, one created each of the given days before now,
// each decided wait after its ask.
func goTestAsks(days []int, decision string, wait time.Duration) []PermissionAsk {
	commands := []string{
		"go test ./internal/kanban/...",
		"go test -run TestX ./...",
		"go test ./...",
		"go test -count=1 ./internal/...",
		"go test ./cmd/...",
	}
	asks := make([]PermissionAsk, 0, len(days))
	for i, day := range days {
		asks = append(asks, bashAsk(commands[i], day, decision, wait))
	}
	return asks
}

// miseAndGoTest is six runs of one input in session s1 with one run of another input between
// the third and the fourth.
func miseAndGoTest() []ToolRun {
	base := fixedNow().Add(-time.Hour)
	return []ToolRun{
		toolRun("k-mise", "mise run check", base),
		toolRun("k-mise", "mise run check", base.Add(time.Minute)),
		toolRun("k-mise", "mise run check", base.Add(2*time.Minute)),
		toolRun("k-gotest", "go test ./internal/kanban/...", base.Add(3*time.Minute)),
		toolRun("k-mise", "mise run check", base.Add(4*time.Minute)),
		toolRun("k-mise", "mise run check", base.Add(5*time.Minute)),
		toolRun("k-mise", "mise run check", base.Add(6*time.Minute)),
	}
}

// miseInARow is four runs of one input with nothing between them.
func miseInARow() []ToolRun {
	base := fixedNow().Add(-time.Hour)
	return []ToolRun{
		toolRun("k-mise", "mise run check", base),
		toolRun("k-mise", "mise run check", base.Add(time.Minute)),
		toolRun("k-mise", "mise run check", base.Add(2*time.Minute)),
		toolRun("k-mise", "mise run check", base.Add(3*time.Minute)),
	}
}

// threeMise is three runs of one input with a run of another input between them.
func threeMise() []ToolRun {
	base := fixedNow().Add(-time.Hour)
	return []ToolRun{
		toolRun("k-mise", "mise run check", base),
		toolRun("k-gotest", "go test ./internal/kanban/...", base.Add(time.Minute)),
		toolRun("k-mise", "mise run check", base.Add(2*time.Minute)),
		toolRun("k-gotest", "go test ./internal/kanban/...", base.Add(3*time.Minute)),
		toolRun("k-mise", "mise run check", base.Add(4*time.Minute)),
	}
}

// bothInput is an input where both rules fire.
func bothInput() Input {
	return Input{
		Now:   fixedNow(),
		Tools: miseAndGoTest(),
		Asks:  goTestAsks([]int{1, 2, 3, 4, 5}, "allow", 100*time.Second),
	}
}

var miseTip = Tip{
	ID:       "same_command:s1:k-mise",
	Rule:     RuleSameCommand,
	Session:  "s1",
	Sentence: "`mise run check` ran 6 times this session. One narrower run is faster while fixing.",
	Count:    6,
}

var goTestTip = Tip{
	ID:       "always_allowed:Bash(go test:*)",
	Rule:     RuleAlwaysAllowed,
	Sentence: "You allowed `Bash(go test:*)` 5 times in 7 days, waiting 1 min 40 s each time. An allow rule would skip the prompt.",
	Count:    5,
	Wait:     100 * time.Second,
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want []Tip
	}{
		{
			name: "a command a session keeps coming back to",
			in:   Input{Now: fixedNow(), Tools: miseAndGoTest()},
			want: []Tip{miseTip},
		},
		{
			name: "four runs in a row are no tip",
			in:   Input{Now: fixedNow(), Tools: miseInARow()},
			want: nil,
		},
		{
			name: "three runs with another run between are no tip",
			in:   Input{Now: fixedNow(), Tools: threeMise()},
			want: nil,
		},
		{
			name: "five allowed go test asks with a long wait",
			in:   Input{Now: fixedNow(), Asks: goTestAsks([]int{1, 2, 3, 4, 5}, "allow", 100*time.Second)},
			want: []Tip{goTestTip},
		},
		{
			name: "one deny among the five gives no tip",
			in: func() Input {
				asks := goTestAsks([]int{1, 2, 3, 4, 5}, "allow", 100*time.Second)
				asks[2].Decision = "deny"
				return Input{Now: fixedNow(), Asks: asks}
			}(),
			want: nil,
		},
		{
			name: "four allowed asks give no tip",
			in:   Input{Now: fixedNow(), Asks: goTestAsks([]int{1, 2, 3, 4}, "allow", 100*time.Second)},
			want: nil,
		},
		{
			name: "a short median wait gives no tip",
			in:   Input{Now: fixedNow(), Asks: goTestAsks([]int{1, 2, 3, 4, 5}, "allow", 5*time.Second)},
			want: nil,
		},
		{
			name: "an ask older than 7 days does not count",
			in:   Input{Now: fixedNow(), Asks: goTestAsks([]int{1, 2, 3, 4, 8}, "allow", 100*time.Second)},
			want: nil,
		},
		{
			name: "both rules at once, always_allowed first",
			in:   bothInput(),
			want: []Tip{goTestTip, miseTip},
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

func TestDetectIsStableAcrossCalls(t *testing.T) {
	first := Detect(bothInput())
	second := Detect(bothInput())
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() twice = %+v then %+v, want equal", first, second)
	}
}
