package server

import (
	"context"
	"errors"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// eventSweepEvery is the event sweep's beat: one hour, after the first sweep at start.
const eventSweepEvery = time.Hour

// watchEventSweep prunes the registry's reported events and every channel's events to their
// 90-day retention once at start, then each beat. A failed half is one stderr line, and the
// join of the halves' errors marks the sweep failing; the next beat retries it. Serving never
// depends on the sweep.
func (s *server) watchEventSweep(ctx context.Context) {
	now := s.now
	if now == nil {
		now = time.Now
	}
	s.health.Register("hub.event-sweep", domain.HealthLoop, eventSweepEvery)
	sweep := func() {
		var registryErr, channelErr error
		if s.registry != nil {
			if _, err := s.registry.PruneEvents(ctx, now()); err != nil {
				s.warn("event sweep: " + err.Error())
				registryErr = err
			}
		}
		if s.hub != nil {
			if _, err := s.hub.PruneEvents(ctx, now()); err != nil {
				s.warn("event sweep: " + err.Error())
				channelErr = err
			}
		}
		if joined := errors.Join(registryErr, channelErr); joined != nil {
			s.health.Fail("hub.event-sweep", joined)
			return
		}
		s.health.OK("hub.event-sweep")
	}
	sweep() // the first sweep lands before the caller carries on, so a start cuts the files back
	ticker := time.NewTicker(eventSweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
