package server

import (
	"context"
	"time"

	radar "github.com/MuhmdRaouf/huddle/internal/radar/server"
)

// radarDay answers Radar's day card from what the hub holds: the registry's session spans and
// the done lines of every board the kanban tab knows.
type radarDay struct{ s *server }

// DayFacts answers every session's span and the cards moved to done in the window. A
// server without a registry answers neither; without a kanban tab it counts no cards.
func (d radarDay) DayFacts(ctx context.Context, from, to time.Time) (radar.DayFacts, error) {
	facts := radar.DayFacts{}
	if d.s.registry == nil {
		return facts, nil
	}
	spans, err := d.s.registry.SessionSpans(ctx)
	if err != nil {
		return facts, err
	}
	// radar clips each span to the window, so every span goes as it is
	for _, span := range spans {
		facts.Sessions = append(facts.Sessions, radar.SessionSpan{Start: span.Start, End: span.End})
	}
	if d.s.kanban != nil {
		facts.CardsDone = d.s.kanban.DoneBetween(ctx, from, to)
	}
	return facts, nil
}
