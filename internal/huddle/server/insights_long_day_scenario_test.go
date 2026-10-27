package server

// The long day tip is the whole huddle's fact, no one session's: it reaches the owner's
// insights answer alone, and a session's own answer carries none even with the tips-to-sessions
// switch on.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/insights"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// dayFixture is one home with its registry and a clock fixed at half past noon on a Sunday, so
// today's long day is settled fact rather than a race with the real hour.
type dayFixture struct {
	t     *testing.T
	home  string
	reg   *hub.DB
	clock time.Time
	srv   *server
}

func newDayFixture(t *testing.T) *dayFixture {
	t.Helper()
	homeRoot := t.TempDir()
	reg, err := hub.Open(context.Background(), home.Home{Root: homeRoot}.HubDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	f := &dayFixture{t: t, home: homeRoot, reg: reg,
		clock: time.Date(2026, 10, 25, 12, 30, 0, 0, time.UTC)}
	f.srv = &server{registry: reg, now: func() time.Time { return f.clock }, o: Options{Home: homeRoot}}
	return f
}

// runningSince reports one session whose first event landed at at and that never ended.
func (f *dayFixture) runningSince(session string, at time.Time) {
	f.t.Helper()
	err := f.reg.Report(context.Background(), hub.Report{
		SessionID: session, Cwd: f.t.TempDir(), LastEvent: "UserPromptSubmit",
		LastEventAt: at.UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// insights reads GET /api/insights the way its clients do: no session for the owner's card,
// the session's id for its own.
func (f *dayFixture) insights(session string) insightsAnswer {
	f.t.Helper()
	path := "/api/insights"
	if session != "" {
		path += "?session=" + session
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if err := f.srv.insightsRoute(rec, req); err != nil {
		f.t.Fatalf("GET %s: %v", path, err)
	}
	var answer insightsAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		f.t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return answer
}

func TestTheLongDayTipReachesTheOwnerAlone(t *testing.T) {
	bdd.Feature(t, "a day's sessions past 10 hours earn the owner one long_day tip", func(f *bdd.F) {
		f.Scenario("a session running since one this morning makes today a long day", func(s *bdd.S) {
			var (
				fix   *dayFixture
				owner insightsAnswer
			)
			s.Given("the registry holds a session running since one this morning", func(t *testing.T) {
				fix = newDayFixture(s.T)
				fix.runningSince("s-day", time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC))
			})
			s.When("the owner reads the insights card", func(t *testing.T) {
				owner = fix.insights("")
			})
			s.Then("the answer carries today's long_day tip, naming the day and the length", func(t *testing.T) {
				want := []tipView{{
					ID:       "long_day:2026-10-25",
					Rule:     "long_day",
					Sentence: "Sessions ran for 11 h 30 min today, past 10 h.",
					Count:    690,
				}}
				if !reflect.DeepEqual(owner.Tips, want) {
					t.Errorf("the owner's tips = %+v, want %+v", owner.Tips, want)
				}
			})
			s.And("the session's own answer carries no tip about the whole huddle", func(t *testing.T) {
				if err := insights.WriteSettings(insights.SettingsFile(fix.home),
					insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
					t.Fatal(err)
				}
				mine := fix.insights("s-day")
				if len(mine.Tips) != 0 {
					t.Errorf("the session's tips = %+v, want none", mine.Tips)
				}
			})
		})
	})
}
