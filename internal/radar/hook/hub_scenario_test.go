package hook

// radar at session start, as scenarios: with a healthy huddle hub the hook
// points at the hub's radar page and starts nothing, whether autostart is on
// or off; with no hub, today's standalone behaviour stands.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/hookplan"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hubaddr"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// hubBase is the base url the hub fake answers with: loopback, on a port no
// test of this suite ever binds, because the fake does no networking at all.
const hubBase = "http://127.0.0.1:28860"

func TestRadarAtSessionStartScenarios(t *testing.T) {
	bdd.Feature(t, "radar at session start", func(f *bdd.F) {
		f.Scenario("with the hub running and autostart on, the hook points at the hub and starts nothing", func(s *bdd.S) {
			var (
				environ   []string
				stdout    bytes.Buffer
				starts    int
				hubAsked  bool
				startCode int
			)
			s.Given("a session world with autostart on and a healthy hub answering", func(t *testing.T) {
				environ = entryEnviron(s.T, "RADAR_AUTOSTART=1")
			})
			s.When("a SessionStart hook runs", func(t *testing.T) {
				startCode = runEntry(context.Background(),
					strings.NewReader(`{"hook_event_name":"SessionStart"}`), &stdout, environ,
					func() time.Time { return entryNow }, 1, 2,
					func() bool { starts++; return true },
					func(context.Context) (string, bool) { hubAsked = true; return hubBase, true })
			})
			s.Then("the hook exits 0 and its line names the hub's radar page", func(t *testing.T) {
				if startCode != 0 {
					t.Errorf("runEntry = %d, want 0", startCode)
				}
				want := "{\"systemMessage\":\"radar: in the huddle hub at " + hubBase + "/#/radar\"}\n"
				if stdout.String() != want {
					t.Errorf("stdout = %q, want %q", stdout.String(), want)
				}
			})
			s.And("no dashboard was started", func(t *testing.T) {
				if starts != 0 {
					t.Errorf("the dashboard was started %d times, want none", starts)
				}
				if !hubAsked {
					t.Error("the hook never asked for the hub")
				}
			})
		})

		f.Scenario("with the hub running and autostart off, the hook says nothing and starts nothing", func(s *bdd.S) {
			var (
				environ   []string
				stdout    bytes.Buffer
				starts    int
				startCode int
			)
			s.Given("a session world with autostart off and a healthy hub answering", func(t *testing.T) {
				environ = entryEnviron(s.T)
			})
			s.When("a SessionStart hook runs", func(t *testing.T) {
				startCode = runEntry(context.Background(),
					strings.NewReader(`{"hook_event_name":"SessionStart"}`), &stdout, environ,
					func() time.Time { return entryNow }, 1, 2,
					func() bool { starts++; return true },
					func(context.Context) (string, bool) { return hubBase, true })
			})
			s.Then("the hook exits 0 in silence", func(t *testing.T) {
				if startCode != 0 {
					t.Errorf("runEntry = %d, want 0", startCode)
				}
				if stdout.String() != "" {
					t.Errorf("stdout = %q, want empty", stdout.String())
				}
			})
			s.And("no dashboard was started", func(t *testing.T) {
				if starts != 0 {
					t.Errorf("the dashboard was started %d times, want none", starts)
				}
			})
		})

		f.Scenario("with no hub and autostart on, the hook starts the dashboard as before", func(s *bdd.S) {
			var (
				environ   []string
				stdout    bytes.Buffer
				starts    int
				startCode int
			)
			s.Given("a session world with autostart on, a saved port and no hub answering", func(t *testing.T) {
				environ = entryEnviron(s.T, "RADAR_AUTOSTART=1")
				state := radarHome(environ)
				if err := os.MkdirAll(state, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, "port"), []byte("29871\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.When("a SessionStart hook runs", func(t *testing.T) {
				startCode = runEntry(context.Background(),
					strings.NewReader(`{"hook_event_name":"SessionStart"}`), &stdout, environ,
					func() time.Time { return entryNow }, 1, 2,
					func() bool { starts++; return true },
					func(context.Context) (string, bool) { return "", false })
			})
			s.Then("the hook exits 0 and its line names the dashboard it started", func(t *testing.T) {
				if startCode != 0 {
					t.Errorf("runEntry = %d, want 0", startCode)
				}
				want := "{\"systemMessage\":\"radar: dashboard at http://127.0.0.1:29871\"}\n"
				if stdout.String() != want {
					t.Errorf("stdout = %q, want %q", stdout.String(), want)
				}
			})
			s.And("the dashboard was started exactly once", func(t *testing.T) {
				if starts != 1 {
					t.Errorf("the dashboard was started %d times, want once", starts)
				}
			})
		})

		f.Scenario("with no hub and a radar server already running, nothing starts", func(s *bdd.S) {
			var (
				environ   []string
				stdout    bytes.Buffer
				startCode int
			)
			s.Given("a session world with autostart on and a live radar server recorded", func(t *testing.T) {
				environ = entryEnviron(s.T, "RADAR_AUTOSTART=1")
				state := radarHome(environ)
				if err := os.MkdirAll(state, 0o700); err != nil {
					t.Fatal(err)
				}
				info := "{\"pid\":" + strconv.Itoa(os.Getpid()) + "}\n"
				if err := os.WriteFile(filepath.Join(state, "server.json"), []byte(info), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.When("a SessionStart hook runs", func(t *testing.T) {
				startCode = runEntry(context.Background(),
					strings.NewReader(`{"hook_event_name":"SessionStart"}`), &stdout, environ,
					func() time.Time { return entryNow }, 1, 2,
					refusingStarter(s.T),
					func(context.Context) (string, bool) { return "", false })
			})
			s.Then("the hook exits 0 in silence", func(t *testing.T) {
				if startCode != 0 {
					t.Errorf("runEntry = %d, want 0", startCode)
				}
				if stdout.String() != "" {
					t.Errorf("stdout = %q, want empty", stdout.String())
				}
			})
		})
	})
}

// TestHubProbeFitsTheRadarBudget pins the health probe inside the radar part's
// session-start budget: the probe takes at most half of it, so a hub that
// never answers cannot hold a session's start.
func TestHubProbeFitsTheRadarBudget(t *testing.T) {
	budget, ok := hookplan.Budget("session-start", "radar")
	if !ok {
		t.Fatal("the hookplan grants the radar part no session-start budget")
	}
	if 2*hubaddr.ProbeTimeout >= budget {
		t.Errorf("the hub probe's %v does not leave the radar part half of its %v session-start budget",
			hubaddr.ProbeTimeout, budget)
	}
}
