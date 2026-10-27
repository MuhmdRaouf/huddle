package insights

import (
	"reflect"
	"testing"
	"time"
)

// dayAt is a wall-clock time on the fixed day, 2026-10-25, in UTC.
func dayAt(hour, minute int) time.Time {
	return time.Date(2026, 10, 25, hour, minute, 0, 0, time.UTC)
}

func TestDetectLongDay(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want []Tip
	}{
		{
			name: "one session's ten and a half hours today are a long day",
			in: Input{Now: fixedNow(), Spans: []SessionSpan{
				{Session: "s1", Start: dayAt(1, 0), End: dayAt(11, 30)},
			}},
			want: []Tip{{
				ID:       "long_day:2026-10-25",
				Rule:     RuleLongDay,
				Sentence: "Sessions ran for 10 h 30 min today, past 10 h.",
				Count:    630,
			}},
		},
		{
			name: "two overlapping sessions count their union once",
			in: Input{Now: fixedNow(), Spans: []SessionSpan{
				{Session: "s1", Start: dayAt(1, 0), End: dayAt(6, 0)},
				{Session: "s2", Start: dayAt(5, 0), End: dayAt(11, 1)},
			}},
			want: []Tip{{
				ID:       "long_day:2026-10-25",
				Rule:     RuleLongDay,
				Sentence: "Sessions ran for 10 h 1 min today, past 10 h.",
				Count:    601,
			}},
		},
		{
			name: "exactly ten hours are no long day",
			in: Input{Now: fixedNow(), Spans: []SessionSpan{
				{Session: "s1", Start: dayAt(1, 0), End: dayAt(11, 0)},
			}},
			want: nil,
		},
		{
			name: "a running session counts up to now",
			in: Input{Now: fixedNow(), Spans: []SessionSpan{
				{Session: "s1", Start: dayAt(1, 30)},
			}},
			want: []Tip{{
				ID:       "long_day:2026-10-25",
				Rule:     RuleLongDay,
				Sentence: "Sessions ran for 10 h 30 min today, past 10 h.",
				Count:    630,
			}},
		},
		{
			name: "a session that ran overnight counts only its part of today",
			in: Input{Now: fixedNow(), Spans: []SessionSpan{
				{Session: "s1", Start: time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)},
			}},
			want: []Tip{{
				ID:       "long_day:2026-10-25",
				Rule:     RuleLongDay,
				Sentence: "Sessions ran for 12 h 0 min today, past 10 h.",
				Count:    720,
			}},
		},
		{
			name: "a long day two days back is outside the window",
			in: Input{Now: fixedNow(), Spans: []SessionSpan{
				{Session: "s1", Start: time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC),
					End: time.Date(2026, 10, 23, 20, 0, 0, 0, time.UTC)},
			}},
			want: nil,
		},
		{
			name: "yesterday's long day names its day",
			in: Input{Now: fixedNow(), Spans: []SessionSpan{
				{Session: "s1", Start: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC),
					End: time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC)},
			}},
			want: []Tip{{
				ID:       "long_day:2026-10-24",
				Rule:     RuleLongDay,
				Sentence: "Sessions ran for 11 h 0 min on Saturday, past 10 h.",
				Count:    660,
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

func TestDetectIsStableAcrossCallsLongDay(t *testing.T) {
	in := Input{Now: fixedNow(), Spans: []SessionSpan{
		{Session: "s1", Start: dayAt(1, 0), End: dayAt(11, 30)},
	}}
	first := Detect(in)
	second := Detect(in)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Detect() twice = %+v then %+v, want equal", first, second)
	}
}
