// The Radar day card's facts as the served server answers them: the peak number of sessions
// the registry held alive at once during the UTC day, and the cards the boards it knows moved
// to done that day, read at /api/radar/api/day behind huddle's credential.
package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

func TestTheRadarDayCountsPeakSessionsAndCardsDone(t *testing.T) {
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	// a moment this far back, kept inside today so a run just after midnight still counts it
	back := func(d time.Duration) time.Time {
		if at := now.Add(-d); at.After(midnight) {
			return at
		}
		return midnight
	}
	stamp := func(at time.Time) string { return at.Format("2006-01-02T15:04:05.000Z") }

	home := t.TempDir()
	repo := filepath.Join(home, "alpha")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo, ".agents", "kanban")
	if _, err := board.Init(context.Background(), root, "", now); err != nil {
		t.Fatal(err)
	}
	// two cards done today; one done two days back and a review today count for nothing
	log := strings.Join([]string{
		`{"ts":"` + now.Add(-48*time.Hour).Format("2006-01-02T15:04:05-0700") + `","event":"done","ticket":"OLD-1","actor":"a","note":""}`,
		`{"ts":"` + back(4*time.Minute).Format("2006-01-02T15:04:05-0700") + `","event":"review","ticket":"A-1","actor":"a","note":""}`,
		`{"ts":"` + back(3*time.Minute).Format("2006-01-02T15:04:05-0700") + `","event":"done","ticket":"A-1","actor":"a","note":""}`,
		`{"ts":"` + back(2*time.Minute).Format("2006-01-02T15:04:05-0700") + `","event":"done","ticket":"A-2","actor":"a","note":""}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "LOG"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := startServer(t, nil)
	report := func(session, event string, at time.Time) {
		t.Helper()
		body := `{"session_id":"` + session + `","cwd":"` + repo + `","last_event":"` + event +
			`","last_event_at":"` + stamp(at) + `"}`
		if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
			t.Fatalf("report %s %s: %d %s", session, event, code, out)
		}
	}
	// s-early ends before the other three start: the peak is three, not four
	report("s-early", "SessionStart", back(40*time.Minute))
	report("s-early", "SessionEnd", back(30*time.Minute))
	report("s-one", "SessionStart", back(20*time.Minute))
	report("s-two", "SessionStart", back(15*time.Minute))
	report("s-three", "SessionStart", back(10*time.Minute))
	report("s-three", "UserPromptSubmit", back(5*time.Minute))

	code, _, body := srv.get(t, "/api/radar/api/day", nil)
	if code != 200 {
		t.Fatalf("the day = %d %s, want 200", code, body)
	}
	var day struct {
		PeakSessions *int `json:"peakSessions"`
		CardsDone    *int `json:"cardsDone"`
	}
	if err := json.Unmarshal([]byte(body), &day); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if day.PeakSessions == nil || *day.PeakSessions != 3 || day.CardsDone == nil || *day.CardsDone != 2 {
		t.Fatalf("the day = %s, want peakSessions 3 and cardsDone 2", body)
	}
}

// TestTheRadarDayCountsCardsDoneOnTheHubZonesDay: with the hub's zone at UTC+14, a card done at
// the zone's midnight counts and one a second before it does not, wherever UTC's midnight falls.
func TestTheRadarDayCountsCardsDoneOnTheHubZonesDay(t *testing.T) {
	zone, err := time.LoadLocation("Pacific/Kiritimati")
	if err != nil {
		t.Fatalf("load Pacific/Kiritimati: %v", err)
	}
	local := time.Now().In(zone)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)

	home := t.TempDir()
	repo := filepath.Join(home, "alpha")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo, ".agents", "kanban")
	if _, err := board.Init(context.Background(), root, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"ts":"` + midnight.Add(-time.Second).Format("2006-01-02T15:04:05-0700") + `","event":"done","ticket":"Y-1","actor":"a","note":""}`,
		`{"ts":"` + midnight.Format("2006-01-02T15:04:05-0700") + `","event":"done","ticket":"T-1","actor":"a","note":""}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "LOG"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := startServer(t, nil)
	if code, out := putZone(t, srv, `{"zone": "Pacific/Kiritimati"}`); code != 200 {
		t.Fatalf("PUT the zone = %d %s", code, out)
	}
	body := `{"session_id":"s-zone","cwd":"` + repo + `","last_event":"SessionStart","last_event_at":"` +
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z") + `"}`
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("report the session: %d %s", code, out)
	}
	code, _, out := srv.get(t, "/api/radar/api/day", nil)
	if code != 200 {
		t.Fatalf("the day = %d %s, want 200", code, out)
	}
	var day struct {
		CardsDone *int `json:"cardsDone"`
	}
	if err := json.Unmarshal([]byte(out), &day); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if day.CardsDone == nil || *day.CardsDone != 1 {
		t.Fatalf("the day = %s, want cardsDone 1: the card done at the Kiritimati midnight", out)
	}
}
