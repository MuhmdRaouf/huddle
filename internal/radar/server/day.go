// The day route: the Overview's day card, the peak number of sessions alive at once and the
// cards moved to done over the current UTC day. Radar holds neither fact; the embedding hub
// answers them from its session registry and the boards it knows.
package server

import (
	"context"
	"sort"
	"time"

	dayclock "github.com/MuhmdRaouf/huddle/internal/radar/clock"
)

// DayPath is the day card's route.
const DayPath = "/api/day"

// SessionSpan is one session's life: alive from Start until End. A zero End is a session
// still alive.
type SessionSpan struct {
	Start time.Time
	End   time.Time
}

// DayFacts is what the hub knows of one window: every session alive in it, and how many cards
// its boards moved to done in it.
type DayFacts struct {
	Sessions  []SessionSpan
	CardsDone int
}

// DaySource answers the facts of the window from to to.
type DaySource interface {
	DayFacts(ctx context.Context, from, to time.Time) (DayFacts, error)
}

// dayAnswer is the day route's body.
type dayAnswer struct {
	PeakSessions int `json:"peakSessions"`
	CardsDone    int `json:"cardsDone"`
}

// dayRoute answers the day from the UTC midnight to now; without a source (a standalone radar)
// it is a 503. A source failure is the caller's to answer for.
func dayRoute(ctx context.Context, source DaySource, now time.Time) (ApiResponse, error) {
	if source == nil {
		return errorOf(503, "day facts unavailable"), nil
	}
	from := dayclock.Midnight(now)
	facts, err := source.DayFacts(ctx, from, now)
	if err != nil {
		return ApiResponse{}, err
	}
	return jsonOf(200, dayAnswer{
		PeakSessions: peakSessions(facts.Sessions, from, now),
		CardsDone:    facts.CardsDone,
	}), nil
}

// peakSessions is the most sessions alive at one moment t, from <= t < to, where a session is
// alive while Start <= t < End.
func peakSessions(spans []SessionSpan, from, to time.Time) int {
	type edge struct {
		at    time.Time
		delta int
	}
	var edges []edge
	for _, span := range spans {
		start, end := span.Start, span.End
		if end.IsZero() || end.After(to) {
			end = to
		}
		if start.Before(from) {
			start = from
		}
		if !start.Before(end) {
			continue
		}
		edges = append(edges, edge{start, 1}, edge{end, -1})
	}
	// an end sorts before a start at the same moment: a session ending as another starts is
	// never alive with it
	sort.Slice(edges, func(i, j int) bool {
		if !edges[i].at.Equal(edges[j].at) {
			return edges[i].at.Before(edges[j].at)
		}
		return edges[i].delta < edges[j].delta
	})
	alive, peak := 0, 0
	for _, e := range edges {
		alive += e.delta
		peak = max(peak, alive)
	}
	return peak
}
