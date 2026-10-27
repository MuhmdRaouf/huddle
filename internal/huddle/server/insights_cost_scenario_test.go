package server

// The project_cost tip is the owner's fact alone: the mounted radar tab's ledger holds a
// project whose day passed twice its 7-day average, the owner's insights answer carries the
// tip with the numbers, and the session's own answer carries nothing about it.

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// costFixture is one home with its registry, its own mounted radar tab and one clock for
// tab and server, so "today" is the same calendar day on both sides of the route.
type costFixture struct {
	t     *testing.T
	home  string
	reg   *hub.DB
	clock time.Time
	tab   *radartab.Tab
	srv   *server
}

func newCostFixture(t *testing.T) *costFixture {
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
	clock := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	tab := radartab.Mount(context.Background(), mux, env["RADAR_HOME"], radartab.MountOptions{
		Env:            env,
		Port:           port,
		Version:        "test",
		Now:            func() time.Time { return clock },
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
	f := &costFixture{t: t, home: homeRoot, reg: reg, tab: tab, clock: clock}
	f.srv = &server{registry: reg, now: func() time.Time { return f.clock }, radar: tab,
		o: Options{Home: homeRoot}}
	return f
}

// nameProject puts the session's working directory on record, the cwd its project name
// comes from.
func (f *costFixture) nameProject(session, cwd string) {
	f.t.Helper()
	sessionID, dir := session, cwd
	f.tab.Store.AddSpoolLine(shared.SpoolLine{
		Ts:        f.clock.UTC().Format("2006-01-02T15:04:05.000Z"),
		Event:     "SessionStart",
		SessionID: &sessionID,
		Cwd:       &dir,
	})
}

// costRun records one priced request of the session at at.
func (f *costFixture) costRun(id string, at time.Time, tokens shared.Tokens) {
	f.t.Helper()
	f.tab.Store.AddRequest(shared.RequestRecord{
		ID: id, SessionID: "s-cost", AgentID: "main", Model: "claude-opus-5-5",
		Ts: at.UnixMilli(), Tokens: tokens,
	}, nil)
}

// insights reads GET /api/insights the way its clients do: no session for the owner's card,
// the session's id for its own.
func (f *costFixture) insights(session string) insightsAnswer {
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

// pricedFigures is one request's own price, read off the price table the ledger reads; a
// fixture model that never prices is a broken fixture.
func pricedFigures(t *testing.T, at time.Time, tokens shared.Tokens) float64 {
	t.Helper()
	c := cost.RequestCost(cost.PricedRequest{Model: "claude-opus-5-5", Ts: at.UnixMilli(), Tokens: tokens})
	if c == nil {
		t.Fatal("the fixture's model never prices")
	}
	return float64(c.USD)
}

func TestTheProjectCostTipIsTheOwnersAlone(t *testing.T) {
	bdd.Feature(t, "a project whose spend today passes twice its 7-day average earns the owner one project_cost tip", func(f *bdd.F) {
		f.Scenario("a project dear today over an even week before", func(s *bdd.S) {
			var (
				fix   *costFixture
				owner insightsAnswer
				want  []tipView
			)
			s.Given("the mounted radar tab holds a project that spent double today after an even week", func(t *testing.T) {
				fix = newCostFixture(s.T)
				if err := insights.WriteSettings(insights.SettingsFile(fix.home),
					insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
					t.Fatal(err)
				}
				fix.nameProject("s-cost", "/Users/me/playground/huddle")
				// today's two calls and the week's calls sit well over the dollar-a-day floor,
				// so the tip turns on the doubling alone
				fix.costRun("r-t1", fix.clock.Add(-30*time.Minute), shared.Tokens{Input: 1_000_000, Output: 50_000})
				fix.costRun("r-t2", fix.clock.Add(-30*time.Minute), shared.Tokens{Input: 1_000_000, Output: 50_000})
				for ago := 1; ago <= 7; ago++ {
					fix.costRun(fmt.Sprintf("r-d%d", ago),
						fix.clock.Add(-time.Duration(ago)*24*time.Hour), shared.Tokens{Input: 100_000, Output: 10_000})
				}
			})
			s.When("the owner reads the insights card", func(t *testing.T) {
				owner = fix.insights("")
			})
			s.Then("the answer carries the project's tip with today, the average and the ratio", func(t *testing.T) {
				big := pricedFigures(t, fix.clock.Add(-30*time.Minute), shared.Tokens{Input: 1_000_000, Output: 50_000})
				today := big + big
				baseline := 0.0
				for ago := 1; ago <= 7; ago++ {
					baseline += pricedFigures(t, fix.clock.Add(-time.Duration(ago)*24*time.Hour),
						shared.Tokens{Input: 100_000, Output: 10_000})
				}
				baseline /= 7
				want = []tipView{{
					ID:   "project_cost:huddle:" + fix.clock.UTC().Format("2006-01-02"),
					Rule: "project_cost",
					Sentence: fmt.Sprintf("`huddle` cost $%.2f today, %.1f times its 7-day average of $%.2f.",
						today, today/baseline, baseline),
					Count: 1,
				}}
				if !reflect.DeepEqual(owner.Tips, want) {
					t.Errorf("the owner's tips = %+v, want %+v", owner.Tips, want)
				}
			})
			s.And("the session's own answer carries no tip about costs", func(t *testing.T) {
				mine := fix.insights("s-cost")
				if len(mine.Tips) != 0 {
					t.Errorf("the session's tips = %+v, want none about costs", mine.Tips)
				}
			})
		})
	})
}
