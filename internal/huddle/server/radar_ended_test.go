// The registry bridge radar's ingest asks: a server without a registry answers none, and one
// with a registry names the sessions a SessionEnd report ended.
package server

import (
	"maps"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

func TestRadarEndedAnswersTheRegistry(t *testing.T) {
	t.Run("a server without a registry answers none", func(t *testing.T) {
		got, err := radarEnded{s: &server{}}.EndedAmong(bg, []string{"s-ended"})
		if err != nil || got != nil {
			t.Fatalf("ended among = %v, %v; want nil, nil", got, err)
		}
	})

	t.Run("a server with a registry names the sessions it has ended", func(t *testing.T) {
		registry, _ := openRegistry(t)
		report := func(rep hub.Report) {
			t.Helper()
			if err := registry.Report(bg, rep); err != nil {
				t.Fatalf("report %s: %v", rep.LastEvent, err)
			}
		}
		report(hub.Report{SessionID: "s-live", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:00.000Z"})
		report(hub.Report{SessionID: "s-ended", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:01.000Z"})
		report(hub.Report{SessionID: "s-ended", LastEvent: "SessionEnd", LastEventAt: "2026-10-16T09:00:02.000Z"})

		got, err := radarEnded{s: &server{registry: registry}}.EndedAmong(bg, []string{"s-live", "s-ended"})
		if err != nil {
			t.Fatalf("ended among: %v", err)
		}
		want := map[string]bool{"s-ended": true}
		if !maps.Equal(got, want) {
			t.Errorf("ended among = %v, want %v", got, want)
		}
	})
}
