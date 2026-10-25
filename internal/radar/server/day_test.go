package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The day route: the Overview's day card reads the peak number of sessions alive at once and
// the cards moved to done, over the UTC day up to now.

// fakeDay answers fixed facts and records the window it was asked for.
type fakeDay struct {
	facts    DayFacts
	err      error
	from, to time.Time
}

func (f *fakeDay) DayFacts(_ context.Context, from, to time.Time) (DayFacts, error) {
	f.from, f.to = from, to
	return f.facts, f.err
}

func TestDayRoute(t *testing.T) {
	now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
	midnight := time.Date(2026, time.October, 25, 0, 0, 0, 0, time.UTC)
	at := func(hour, minute int) time.Time {
		return midnight.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	}

	t.Run("503s without a day source", func(t *testing.T) {
		answer, err := dayRoute(context.Background(), nil, now)
		if err != nil || answer.Status != 503 {
			t.Fatalf("answer = %+v (%v), want 503", answer, err)
		}
	})

	t.Run("answers the peak of three overlapping sessions and the cards done", func(t *testing.T) {
		source := &fakeDay{facts: DayFacts{
			Sessions: []SessionSpan{
				{Start: at(1, 0), End: at(2, 0)},  // alone in the early hours
				{Start: at(9, 0), End: at(11, 0)}, // the three overlap from 10:00 to 10:30
				{Start: at(9, 30), End: at(10, 30)},
				{Start: at(10, 0)}, // still open
			},
			CardsDone: 2,
		}}
		answer, err := dayRoute(context.Background(), source, now)
		if err != nil || answer.Status != 200 {
			t.Fatalf("answer = %+v (%v), want 200", answer, err)
		}
		var day dayAnswer
		decodeBody(t, answer, &day)
		if day.PeakSessions != 3 || day.CardsDone != 2 {
			t.Errorf("day = %+v, want peak 3 and 2 cards done", day)
		}
		if !source.from.Equal(midnight) || !source.to.Equal(now) {
			t.Errorf("asked %s to %s, want the UTC midnight to now", source.from, source.to)
		}
	})

	t.Run("passes a source failure on", func(t *testing.T) {
		boom := errors.New("registry closed")
		if _, err := dayRoute(context.Background(), &fakeDay{err: boom}, now); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the source's", err)
		}
	})
}

func TestPeakSessions(t *testing.T) {
	from := time.Date(2026, time.October, 25, 0, 0, 0, 0, time.UTC)
	to := from.Add(12 * time.Hour)
	hour := func(h int) time.Time { return from.Add(time.Duration(h) * time.Hour) }
	for _, c := range []struct {
		name  string
		spans []SessionSpan
		want  int
	}{
		{"no sessions", nil, 0},
		{"a session ending as the next starts is not alive with it",
			[]SessionSpan{{Start: hour(1), End: hour(2)}, {Start: hour(2), End: hour(3)}}, 1},
		{"a session from yesterday still alive today counts",
			[]SessionSpan{{Start: from.Add(-time.Hour), End: hour(2)}, {Start: hour(1), End: hour(3)}}, 2},
		{"sessions wholly yesterday or after now count for nothing",
			[]SessionSpan{{Start: from.Add(-3 * time.Hour), End: from.Add(-time.Hour)}, {Start: to.Add(time.Hour)}, {Start: hour(4), End: hour(5)}}, 1},
		{"an open session runs to now",
			[]SessionSpan{{Start: hour(1)}, {Start: hour(11), End: hour(13)}}, 2},
		{"an empty span is never alive",
			[]SessionSpan{{Start: hour(1), End: hour(1)}}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := peakSessions(c.spans, from, to); got != c.want {
				t.Errorf("peak = %d, want %d", got, c.want)
			}
		})
	}
}
