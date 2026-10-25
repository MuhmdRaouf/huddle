package insights

import (
	"reflect"
	"testing"
	"time"
)

// laneBase is when a case's requests start, an hour before the clock.
func laneBase() time.Time {
	return fixedNow().Add(-time.Hour)
}

// laneRequests is count requests one provider served, one a minute from start, all answered
// with status.
func laneRequests(provider string, start time.Time, count, status int) []Request {
	requests := make([]Request, 0, count)
	for i := 0; i < count; i++ {
		requests = append(requests, Request{Provider: provider, Status: status, At: start.Add(time.Duration(i) * time.Minute)})
	}
	return requests
}

// kimiFailingRequests is fifty kimi requests in the last hour: 44 answered 200, 5 got 429 and
// one got 503.
func kimiFailingRequests() []Request {
	requests := laneRequests("kimi", laneBase(), 50, 200)
	for i := 44; i < 49; i++ {
		requests[i].Status = 429
	}
	requests[49].Status = 503
	return requests
}

// kimiFailingTip is the tip those fifty requests earn: six failures, mostly the rate limit.
func kimiFailingTip() Tip {
	return Tip{
		ID:       "lane_failing:kimi:2026-10-25",
		Rule:     RuleLaneFailing,
		Sentence: "The kimi lane failed 12% of 50 requests in 24 h, mostly 429.",
		Count:    6,
	}
}

// bothLanesFailing is an input where the lane rule fires for kimi and zai at once.
func bothLanesFailing() Input {
	return Input{Now: fixedNow(), Requests: append(kimiFailingRequests(), laneRequests("zai", laneBase(), 25, 429)...)}
}

func TestDetectLaneFailing(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want []Tip
	}{
		{
			name: "fifty requests in the last hour, six failed",
			in:   Input{Now: fixedNow(), Requests: kimiFailingRequests()},
			want: []Tip{kimiFailingTip()},
		},
		{
			name: "two failures of fifty are under five percent",
			in: func() Input {
				requests := laneRequests("kimi", laneBase(), 50, 200)
				requests[0].Status = 429
				requests[1].Status = 503
				return Input{Now: fixedNow(), Requests: requests}
			}(),
			want: nil,
		},
		{
			name: "nineteen failing requests are too few",
			in:   Input{Now: fixedNow(), Requests: laneRequests("kimi", laneBase(), 19, 429)},
			want: nil,
		},
		{
			name: "failures 25 hours ago are outside the window",
			in: func() Input {
				old := laneRequests("kimi", fixedNow().Add(-25*time.Hour), 10, 429)
				return Input{Now: fixedNow(), Requests: append(old, laneRequests("kimi", laneBase(), 40, 200)...)}
			}(),
			want: nil,
		},
		{
			name: "no answers outnumber the 500, the sentence names no answer",
			in: func() Input {
				requests := laneRequests("zai", laneBase(), 30, 200)
				requests[0].Status = 0
				requests[1].Status = 0
				requests[2].Status = 0
				requests[3].Status = 500
				return Input{Now: fixedNow(), Requests: requests}
			}(),
			want: []Tip{{
				ID:       "lane_failing:zai:2026-10-25",
				Rule:     RuleLaneFailing,
				Sentence: "The zai lane failed 13% of 30 requests in 24 h, mostly no answer.",
				Count:    4,
			}},
		},
		{
			name: "two lanes failing give two tips ordered by id",
			in:   bothLanesFailing(),
			want: []Tip{
				kimiFailingTip(),
				{
					ID:       "lane_failing:zai:2026-10-25",
					Rule:     RuleLaneFailing,
					Sentence: "The zai lane failed 100% of 25 requests in 24 h, mostly 429.",
					Count:    25,
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

func TestDetectIsStableAcrossCallsLaneFailing(t *testing.T) {
	in := bothLanesFailing()
	first := Detect(in)
	second := Detect(in)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() twice = %+v then %+v, want equal", first, second)
	}
}
