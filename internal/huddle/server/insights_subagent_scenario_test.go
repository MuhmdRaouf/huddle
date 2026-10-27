package server

// The subagent_silent tip is one session's fact and the huddle's: it reaches the owner's
// insights answer and that session's own answer, both from the mounted radar tab's events.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/radartab"
	"github.com/MuhmdRaouf/huddle/internal/insights"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// subagentFixture is one home with its registry, its own mounted radar tab and a clock fixed at
// half past noon on a Sunday, so a subagent's thirty minutes of silence is settled fact rather
// than a race with the real hour.
type subagentFixture struct {
	t     *testing.T
	home  string
	reg   *hub.DB
	clock time.Time
	tab   *radartab.Tab
	srv   *server
}

func newSubagentFixture(t *testing.T) *subagentFixture {
	t.Helper()
	homeRoot := t.TempDir()
	reg, err := hub.Open(context.Background(), home.Home{Root: homeRoot}.HubDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	root := t.TempDir()
	env := domain.EnvLookup{
		"RADAR_HOME":         root + "/radar",
		"HOME":               root + "/home",
		"CLAUDE_CONFIG_DIR":  root + "/claude",
		"ZAI_STATE_DIR":      root + "/zai",
		"KIMI_STATE_DIR":     root + "/kimi",
		"DEEPSEEK_STATE_DIR": root + "/deepseek",
		"MINIMAX_STATE_DIR":  root + "/minimax",
		"QWEN_STATE_DIR":     root + "/qwen",
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	tab := radartab.Mount(context.Background(), mux, env["RADAR_HOME"], radartab.MountOptions{
		Env:            env,
		Port:           port,
		Version:        "test",
		IntervalMs:     3_600_000,
		InsightsTickMs: 3_600_000,
		NotifyRunner:   func(string, []string) {},
	})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	t.Cleanup(tab.Stop)
	httpSrv := &http.Server{Handler: mux}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = httpSrv.Serve(listener) }()
	t.Cleanup(func() { _ = httpSrv.Close(); <-serveDone })
	f := &subagentFixture{t: t, home: homeRoot, reg: reg, tab: tab,
		clock: time.Date(2026, 10, 25, 12, 30, 0, 0, time.UTC)}
	f.srv = &server{registry: reg, now: func() time.Time { return f.clock }, radar: tab,
		o: Options{Home: homeRoot}}
	return f
}

// silentSince records one subagent of one session that started at at and has no stop event.
func (f *subagentFixture) silentSince(session, agent, name string, at time.Time) {
	f.t.Helper()
	sessionID, agentID, agentType := session, agent, name
	f.tab.Store.AddSpoolLine(shared.SpoolLine{
		Ts:        at.UTC().Format("2006-01-02T15:04:05.000Z"),
		Event:     "SubagentStart",
		SessionID: &sessionID,
		AgentID:   &agentID,
		AgentType: &agentType,
	})
}

// insights reads GET /api/insights the way its clients do: no session for the owner's card,
// the session's id for its own.
func (f *subagentFixture) insights(session string) insightsAnswer {
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

func TestTheSubagentSilentTipReachesBothAnswers(t *testing.T) {
	bdd.Feature(t, "a subagent long silent earns one subagent_silent tip", func(f *bdd.F) {
		f.Scenario("a subagent started half an hour ago and has not stopped", func(s *bdd.S) {
			var (
				fix   *subagentFixture
				owner insightsAnswer
				want  []tipView
			)
			s.Given("the mounted radar tab holds a subagent started thirty minutes ago with no stop", func(t *testing.T) {
				fix = newSubagentFixture(s.T)
				if err := insights.WriteSettings(insights.SettingsFile(fix.home),
					insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
					t.Fatal(err)
				}
				fix.silentSince("s-sub", "w-sub", "researcher", fix.clock.Add(-30*time.Minute))
			})
			s.When("the owner reads the insights card", func(t *testing.T) {
				owner = fix.insights("")
			})
			s.Then("the answer carries the run's tip, naming the subagent and its age", func(t *testing.T) {
				want = []tipView{{
					ID:       "subagent_silent:s-sub:w-sub",
					Rule:     "subagent_silent",
					Session:  "s-sub",
					Sentence: "Subagent `researcher` started 30 min 0 s ago and has not stopped.",
					Count:    1,
					WaitMs:   1_800_000,
				}}
				if !reflect.DeepEqual(owner.Tips, want) {
					t.Errorf("the owner's tips = %+v, want %+v", owner.Tips, want)
				}
			})
			s.And("the session's own answer carries it too", func(t *testing.T) {
				mine := fix.insights("s-sub")
				if !reflect.DeepEqual(mine.Tips, want) {
					t.Errorf("the session's tips = %+v, want %+v", mine.Tips, want)
				}
			})
		})
	})
}
