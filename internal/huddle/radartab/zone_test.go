// The mount hands radar the hub's zone: the day card's window opens at the zone's midnight.
package radartab

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/server"
)

// windowDay records the window the day card asks for and answers no facts.
type windowDay struct {
	mu   sync.Mutex
	from time.Time
}

func (w *windowDay) DayFacts(_ context.Context, from, _ time.Time) (server.DayFacts, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.from = from
	return server.DayFacts{}, nil
}

func TestTheDayCardOpensAtTheHubZonesMidnight(t *testing.T) {
	// UTC+14: its midnight is never the UTC one
	zone, err := time.LoadLocation("Pacific/Kiritimati")
	if err != nil {
		t.Fatalf("load Pacific/Kiritimati: %v", err)
	}
	day := &windowDay{}
	_, client, base := mountMux(t, tempEnv(t), MountOptions{
		Day:  day,
		Zone: func() *time.Location { return zone },
	})
	before := time.Now()
	if code, body := getJSON(t, client, base, Prefix+"/api/day"); code != 200 {
		t.Fatalf("the day = %d %v, want 200", code, body)
	}
	local := before.In(zone)
	want := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
	day.mu.Lock()
	from := day.from
	day.mu.Unlock()
	// a run that crosses the zone's midnight between the clock read and the request opens on the next day
	if !from.Equal(want) && !from.Equal(want.AddDate(0, 0, 1)) {
		t.Errorf("the day opens at %s, want the Kiritimati midnight %s", from.UTC(), want.UTC())
	}
}
