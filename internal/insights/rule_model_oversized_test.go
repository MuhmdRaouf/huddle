package insights

import (
	"reflect"
	"testing"
)

func TestDetectModelOversized(t *testing.T) {
	cases := []struct {
		name   string
		models []ModelAdvice
		want   []Tip
	}{
		{
			name: "a priced row of five runs is one tip with the saving",
			models: []ModelAdvice{{
				Model: "glm-5.3", Flash: "glm-5.3-flash", Runs: 5, SavingUsd: 1.234, Priced: true,
			}},
			want: []Tip{{
				ID:      "model_oversized:glm-5.3:2026-10-25",
				Rule:    RuleModelOversized,
				Session: "",
				Sentence: "5 subagent runs on `glm-5.3` looked small enough for `glm-5.3-flash`;" +
					" they would have cost $1.23 less.",
				Count: 5,
			}},
		},
		{
			name: "an unpriced row reads without a figure",
			models: []ModelAdvice{{
				Model: "glm-5.3", Flash: "glm-5.3-flash", Runs: 5, Priced: false,
			}},
			want: []Tip{{
				ID:       "model_oversized:glm-5.3:2026-10-25",
				Rule:     RuleModelOversized,
				Session:  "",
				Sentence: "5 subagent runs on `glm-5.3` looked small enough for `glm-5.3-flash`.",
				Count:    5,
			}},
		},
		{
			name: "a priced row whose saving is zero reads without a figure",
			models: []ModelAdvice{{
				Model: "glm-5.3", Flash: "glm-5.3-flash", Runs: 5, SavingUsd: 0, Priced: true,
			}},
			want: []Tip{{
				ID:       "model_oversized:glm-5.3:2026-10-25",
				Rule:     RuleModelOversized,
				Session:  "",
				Sentence: "5 subagent runs on `glm-5.3` looked small enough for `glm-5.3-flash`.",
				Count:    5,
			}},
		},
		{
			name: "two runs are too few",
			models: []ModelAdvice{{
				Model: "glm-5.3", Flash: "glm-5.3-flash", Runs: 2, SavingUsd: 1.5, Priced: true,
			}},
			want: nil,
		},
		{
			name: "three runs are enough",
			models: []ModelAdvice{{
				Model: "glm-5.3", Flash: "glm-5.3-flash", Runs: 3, SavingUsd: 0.5, Priced: true,
			}},
			want: []Tip{{
				ID:      "model_oversized:glm-5.3:2026-10-25",
				Rule:    RuleModelOversized,
				Session: "",
				Sentence: "3 subagent runs on `glm-5.3` looked small enough for `glm-5.3-flash`;" +
					" they would have cost $0.50 less.",
				Count: 3,
			}},
		},
		{
			name: "a row with no flash sibling is no tip",
			models: []ModelAdvice{{
				Model: "claude-opus-5", Flash: "", Runs: 9, SavingUsd: 2, Priced: true,
			}},
			want: nil,
		},
		{
			name: "two rows give two tips in id order",
			models: []ModelAdvice{
				{Model: "kimi-k2.9", Flash: "kimi-k2.9-flash", Runs: 4, SavingUsd: 0.75, Priced: true},
				{Model: "glm-5.3", Flash: "glm-5.3-flash", Runs: 6, SavingUsd: 1.25, Priced: true},
			},
			want: []Tip{
				{
					ID:      "model_oversized:glm-5.3:2026-10-25",
					Rule:    RuleModelOversized,
					Session: "",
					Sentence: "6 subagent runs on `glm-5.3` looked small enough for `glm-5.3-flash`;" +
						" they would have cost $1.25 less.",
					Count: 6,
				},
				{
					ID:      "model_oversized:kimi-k2.9:2026-10-25",
					Rule:    RuleModelOversized,
					Session: "",
					Sentence: "4 subagent runs on `kimi-k2.9` looked small enough for `kimi-k2.9-flash`;" +
						" they would have cost $0.75 less.",
					Count: 4,
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(Input{Now: fixedNow(), Models: tc.models})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Detect() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDetectIsStableAcrossCallsModelOversized(t *testing.T) {
	in := Input{Now: fixedNow(), Models: []ModelAdvice{
		{Model: "glm-5.3", Flash: "glm-5.3-flash", Runs: 5, SavingUsd: 1.234, Priced: true},
	}}
	first := Detect(in)
	second := Detect(in)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() twice = %+v then %+v, want equal", first, second)
	}
}
