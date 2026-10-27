package insights

import (
	"reflect"
	"testing"
)

// A project whose spend today passes twice its 7-day average earns the owner one tip with
// the numbers. Twice exactly is no news, a project under a dollar a day is noise, and a
// week shorter than seven days is too little history to judge.

// oneDay is one project's cost on one day before today.
func oneDay(project string, daysAgo int, cost float64) ProjectDay {
	return ProjectDay{Project: project, DaysAgo: daysAgo, CostUSD: cost}
}

// evenPast is one project's costs over the last `days` full days, the same figure each day.
func evenPast(project string, days int, cost float64) []ProjectDay {
	out := make([]ProjectDay, 0, days+1)
	for ago := 1; ago <= days; ago++ {
		out = append(out, oneDay(project, ago, cost))
	}
	return out
}

func TestDetectProjectCost(t *testing.T) {
	date := fixedNow().UTC().Format("2006-01-02")
	tests := []struct {
		name string
		days []ProjectDay
		want []Tip
	}{
		{
			name: "seven days at two dollars and today at four ten is a tip",
			days: append(evenPast("web", 7, 2.00), oneDay("web", 0, 4.10)),
			want: []Tip{{
				ID:       "project_cost:web:" + date,
				Rule:     RuleProjectCost,
				Session:  "",
				Sentence: "`web` cost $4.10 today, 2.0 times its 7-day average of $2.00.",
				Count:    1,
			}},
		},
		{
			name: "today exactly twice the average is no tip",
			days: append(evenPast("web", 7, 2.00), oneDay("web", 0, 4.00)),
			want: nil,
		},
		{
			name: "three days of history is no tip",
			days: append(evenPast("web", 3, 2.00), oneDay("web", 0, 4.10)),
			want: nil,
		},
		{
			name: "a day under a dollar is noise, not a tip",
			days: append(evenPast("web", 7, 0.20), oneDay("web", 0, 0.90)),
			want: nil,
		},
		{
			name: "two projects over give two tips in id order",
			days: append(append(evenPast("web", 7, 2.00),
				append(evenPast("api", 7, 1.00), oneDay("api", 0, 2.10))...),
				oneDay("web", 0, 4.10)),
			want: []Tip{
				{
					ID:       "project_cost:api:" + date,
					Rule:     RuleProjectCost,
					Session:  "",
					Sentence: "`api` cost $2.10 today, 2.1 times its 7-day average of $1.00.",
					Count:    1,
				},
				{
					ID:       "project_cost:web:" + date,
					Rule:     RuleProjectCost,
					Session:  "",
					Sentence: "`web` cost $4.10 today, 2.0 times its 7-day average of $2.00.",
					Count:    1,
				},
			},
		},
		{
			name: "a week with gaps counts the missing days as zero",
			days: []ProjectDay{
				oneDay("gap", 7, 2.80),
				oneDay("gap", 6, 2.80),
				oneDay("gap", 4, 2.80),
				oneDay("gap", 3, 2.80),
				oneDay("gap", 1, 2.80),
				oneDay("gap", 0, 4.10),
			},
			want: []Tip{{
				ID:       "project_cost:gap:" + date,
				Rule:     RuleProjectCost,
				Session:  "",
				Sentence: "`gap` cost $4.10 today, 2.0 times its 7-day average of $2.00.",
				Count:    1,
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(Input{Now: fixedNow(), ProjectDays: tc.days})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Detect() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDetectIsStableAcrossCallsProjectCost(t *testing.T) {
	in := Input{Now: fixedNow(), ProjectDays: append(evenPast("web", 7, 2.00), oneDay("web", 0, 4.10))}
	first := Detect(in)
	second := Detect(in)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() moved between calls: %+v then %+v", first, second)
	}
}
