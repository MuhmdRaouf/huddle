package extras

// The settings route's capture switch: every answer carries the current capture, the owner's
// POST turns body capture off, a session's POST is the owner's 403, and a POST naming both
// switches or neither is a 400 that names both shapes.

import (
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
)

func TestSettingsCapture(t *testing.T) {
	notifier, _ := notifyLog(t)
	zones := outside.NewZones(t.TempDir())
	capture := outside.NewCapture(t.TempDir())

	t.Run("a GET carries the current capture", func(t *testing.T) {
		state, err := Settings("GET", notifier, zones, capture, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Capture {
			t.Errorf("a fresh home's GET = %+v, want capture on", state)
		}
	})

	t.Run("the owner's POST capture answers and switches off", func(t *testing.T) {
		state, err := Settings("POST", notifier, zones, capture, channel.Owner, vv(t, `{"capture": false}`))
		if err != nil {
			t.Fatal(err)
		}
		if state.Capture {
			t.Errorf("the POST's answer = %+v, want capture off", state)
		}
		again, err := Settings("GET", notifier, zones, capture, "", nil)
		if err != nil || again.Capture {
			t.Errorf("the GET after the switch = %+v (%v), want capture off", again, err)
		}
	})

	t.Run("a session's POST is the owner's 403", func(t *testing.T) {
		if _, err := Settings("POST", notifier, zones, capture, "api", vv(t, `{"capture": false}`)); errStatus(err) != 403 {
			t.Errorf("a session switching capture = status %d, want 403", errStatus(err))
		}
	})

	t.Run("a POST naming both switches or neither is a 400 naming both shapes", func(t *testing.T) {
		_, err := Settings("POST", notifier, zones, capture, channel.Owner, vv(t, `{"notify": true, "capture": true}`))
		if errStatus(err) != 400 {
			t.Fatalf("both switches = status %d, want 400", errStatus(err))
		}
		if !strings.Contains(err.Error(), "settings: {notify: true|false} or {capture: true|false}") {
			t.Errorf("the 400 says %q, want both shapes", err)
		}
		for _, body := range []string{`{}`, `{"capture": "no"}`} {
			if _, err := Settings("POST", notifier, zones, capture, channel.Owner, vv(t, body)); errStatus(err) != 400 {
				t.Errorf("POST %s = status %d, want 400", body, errStatus(err))
			}
		}
	})

	t.Run("a POST naming notify works as before", func(t *testing.T) {
		// a fresh switch reads on, and a notify POST leaves it there
		fresh := outside.NewCapture(t.TempDir())
		state, err := Settings("POST", notifier, zones, fresh, channel.Owner, vv(t, `{"notify": false}`))
		if err != nil {
			t.Fatal(err)
		}
		if state.Notify || !state.Capture {
			t.Errorf("the POST's answer = %+v, want notify off and capture still on", state)
		}
	})
}
