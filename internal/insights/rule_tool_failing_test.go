package insights

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// failBase is when the first failure of a case starts, an hour before the clock.
func failBase() time.Time {
	return fixedNow().Add(-time.Hour)
}

// failedRun is one failed Bash call in session s1.
func failedRun(key, command, firstErr string, at time.Time) ToolRun {
	run := toolRun(key, command, at)
	run.OK = false
	run.Error = firstErr
	return run
}

// goTestFails is count failed `go test …` runs in session s1, one minute apart, all failing
// with the same error's first line.
func goTestFails(count int, firstErr string) []ToolRun {
	runs := make([]ToolRun, 0, count)
	for i := 0; i < count; i++ {
		runs = append(runs, failedRun("k-gotest", "go test ./internal/kanban/...", firstErr,
			failBase().Add(time.Duration(i)*time.Minute)))
	}
	return runs
}

// goTestFailTip is the expected tip for count failed `go test …` runs whose first run started
// at start.
func goTestFailTip(count int, start time.Time) Tip {
	return Tip{
		ID:       fmt.Sprintf("tool_failing:s1:Bash:%d", start.Unix()),
		Rule:     RuleToolFailing,
		Session:  "s1",
		Sentence: fmt.Sprintf("`go test ./internal/kanban/...` failed %d times in a row with \"FAIL: TestOneCard (0.3s)\". Read the first error before retrying.", count),
		Count:    count,
	}
}

func TestDetectToolFailing(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want []Tip
	}{
		{
			name: "three failed go test runs a minute apart",
			in:   Input{Now: fixedNow(), Tools: goTestFails(3, "FAIL: TestOneCard (0.3s)")},
			want: []Tip{goTestFailTip(3, failBase())},
		},
		{
			name: "two failures then a success then a failure",
			in: func() Input {
				runs := goTestFails(3, "FAIL: TestOneCard (0.3s)")
				runs[2].OK = true
				runs[2].Error = ""
				return Input{Now: fixedNow(), Tools: runs}
			}(),
			want: nil,
		},
		{
			name: "three failures with two error shapes, two then one",
			in: func() Input {
				runs := goTestFails(3, "FAIL: TestOneCard (0.3s)")
				runs[2].Error = "FAIL: TestOtherCard (0.4s)"
				return Input{Now: fixedNow(), Tools: runs}
			}(),
			want: nil,
		},
		{
			name: "three failures spread over 20 minutes",
			in: func() Input {
				return Input{Now: fixedNow(), Tools: []ToolRun{
					failedRun("k-gotest", "go test ./internal/kanban/...", "FAIL: TestOneCard (0.3s)", failBase()),
					failedRun("k-gotest", "go test ./internal/kanban/...", "FAIL: TestOneCard (0.3s)", failBase().Add(10*time.Minute)),
					failedRun("k-gotest", "go test ./internal/kanban/...", "FAIL: TestOneCard (0.3s)", failBase().Add(20*time.Minute)),
				}}
			}(),
			want: nil,
		},
		{
			name: "four failures in a row",
			in:   Input{Now: fixedNow(), Tools: goTestFails(4, "FAIL: TestOneCard (0.3s)")},
			want: []Tip{goTestFailTip(4, failBase())},
		},
		{
			name: "two sequences separated by a success",
			in: func() Input {
				runs := goTestFails(7, "FAIL: TestOneCard (0.3s)")
				runs[3].OK = true
				runs[3].Error = ""
				return Input{Now: fixedNow(), Tools: runs}
			}(),
			want: []Tip{
				goTestFailTip(3, failBase()),
				goTestFailTip(3, failBase().Add(4*time.Minute)),
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

func TestDetectIsStableAcrossCallsToolFailing(t *testing.T) {
	in := Input{Now: fixedNow(), Tools: goTestFails(3, "FAIL: TestOneCard (0.3s)")}
	first := Detect(in)
	second := Detect(in)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() twice = %+v then %+v, want equal", first, second)
	}
}
