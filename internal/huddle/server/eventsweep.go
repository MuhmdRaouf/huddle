package server

import (
	"context"
	"time"
)

// eventSweepEvery is the event sweep's beat: one hour, after the first sweep at start.
const eventSweepEvery = time.Hour

// watchEventSweep prunes the registry's reported events and every channel's events to their
// 90-day retention once at start, then each beat. A failed half is one stderr line; the next
// beat retries it. Serving never depends on the sweep.
func (s *server) watchEventSweep(ctx context.Context) {
	now := s.now
	if now == nil {
		now = time.Now
	}
	sweep := func() {
		if s.registry != nil {
			if _, err := s.registry.PruneEvents(ctx, now()); err != nil {
				s.warn("event sweep: " + err.Error())
			}
		}
		if s.hub != nil {
			if _, err := s.hub.PruneEvents(ctx, now()); err != nil {
				s.warn("event sweep: " + err.Error())
			}
		}
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
