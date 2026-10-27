package server

// A tip is the quiet early form of an alert. While radar holds an alert live for a session,
// the tips it covers step aside from both answers — the owner's card and the session's own —
// so the user hears each thing once, not once quietly and once loudly. The alerts come from
// the mounted radar tab, faked here the way radartab's own tests fake a recorded owner: a lock
// record naming a fake owner whose /api/alerts answers.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/radartab"
	"github.com/MuhmdRaouf/huddle/internal/insights"
	radarserver "github.com/MuhmdRaouf/huddle/internal/radar/server"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// alertFixture is one home with its registry and a radar tab mounted behind a fake owner: the
// lock record names the fake, so the tab proxies and every radar answer is the fake's own.
type alertFixture struct {
	t     *testing.T
	home  string
	reg   *hub.DB
	clock time.Time
	srv   *server
}

func newAlertFixture(t *testing.T, alertsJSON string) *alertFixture {
	t.Helper()
	homeRoot := t.TempDir()
	reg, err := hub.Open(context.Background(), home.Home{Root: homeRoot}.HubDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	var mu sync.Mutex
	asked := map[string]int{}
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/api/health", "/api/radar/api/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/alerts", "/api/radar/api/alerts":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(alertsJSON))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(owner.Close)
	host := strings.TrimPrefix(owner.URL, "http://")
	port, err := strconv.Atoi(host[strings.LastIndex(host, ":")+1:])
	if err != nil {
		t.Fatalf("the fake owner's port: %v", err)
	}
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
	if ok := radarserver.WriteServerInfo(context.Background(), env, radarserver.ServerInfo{
		Pid:   int64(os.Getpid()),
		Port:  port,
		URL:   owner.URL,
		Token: "tab-token",
	}); !ok {
		t.Fatal("write the fake owner's lock record")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	bindPort := listener.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	tab := radartab.Mount(context.Background(), mux, env["RADAR_HOME"], radartab.MountOptions{
		Env:            env,
		Port:           bindPort,
		Version:        "test",
		IntervalMs:     3_600_000,
		InsightsTickMs: 3_600_000,
		NotifyRunner:   func(string, []string) {},
	})
	if tab.Owns {
		t.Fatal("the mount took the lock while the fake owner holds it")
	}
	t.Cleanup(tab.Stop)
	httpSrv := &http.Server{Handler: mux}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = httpSrv.Serve(listener) }()
	t.Cleanup(func() { _ = httpSrv.Close(); <-serveDone })
	f := &alertFixture{t: t, home: homeRoot, reg: reg,
		clock: time.Date(2026, 10, 25, 12, 30, 0, 0, time.UTC)}
	f.srv = &server{registry: reg, now: func() time.Time { return f.clock }, radar: tab,
		o: Options{Home: homeRoot}}
	return f
}

// insights reads GET /api/insights the way its clients do: no session for the owner's card,
// the session's id for its own.
func (f *alertFixture) insights(session string) insightsAnswer {
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

func TestATipStepsAsideWhileItsAlertIsLive(t *testing.T) {
	bdd.Feature(t, "a tip the alert that covers it fired steps aside until the alert lifts", func(f *bdd.F) {
		f.Scenario("radar holds a context alert for a session whose checkpoint tip would fire", func(s *bdd.S) {
			var fix *alertFixture
			s.Given("the registry holds a session at 72% with nothing committed, and radar answers a context alert for it", func(t *testing.T) {
				fix = newAlertFixture(s.T,
					`{"alerts":[{"kind":"context","sessionId":"s-check","id":"context:s-check","detail":"context at 91%","severity":"warn"}]}`)
				if err := insights.WriteSettings(insights.SettingsFile(fix.home),
					insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
					t.Fatal(err)
				}
				stamp := func(ago time.Duration) string {
					return fix.clock.Add(-ago).UTC().Format("2006-01-02T15:04:05.000Z")
				}
				for _, rep := range []hub.Report{
					{SessionID: "s-check", LastEvent: "PostToolUse", LastEventAt: stamp(5 * time.Minute), Tool: "Bash", ToolTarget: "go build ./..."},
					{SessionID: "s-check", LastEvent: "UserPromptSubmit", LastEventAt: stamp(time.Minute), ContextPercent: 72},
				} {
					if err := fix.reg.Report(context.Background(), rep); err != nil {
						t.Fatalf("Report(%s %s): %v", rep.SessionID, rep.LastEvent, err)
					}
				}
			})
			s.Then("the owner's answer carries no tip: the context alert covers the checkpoint tip", func(t *testing.T) {
				if owner := fix.insights(""); len(owner.Tips) != 0 {
					t.Errorf("the owner's tips = %+v, want none", owner.Tips)
				}
			})
			s.And("the session's own answer carries none either", func(t *testing.T) {
				if mine := fix.insights("s-check"); len(mine.Tips) != 0 {
					t.Errorf("s-check's tips = %+v, want none", mine.Tips)
				}
			})
		})
	})
}

// TestATipStaysWhileNoAlertNamesIt keeps the quiet half honest: the same session whose tip
// fired when radar answered is back to tipping once the fake owner answers no alerts, the way
// it does when radar is down or holds nothing live.
func TestATipStaysWhileNoAlertNamesIt(t *testing.T) {
	fix := newAlertFixture(t, `{"alerts":[]}`)
	if err := insights.WriteSettings(insights.SettingsFile(fix.home),
		insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
		t.Fatal(err)
	}
	stamp := func(ago time.Duration) string {
		return fix.clock.Add(-ago).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	for _, rep := range []hub.Report{
		{SessionID: "s-check", LastEvent: "PostToolUse", LastEventAt: stamp(5 * time.Minute), Tool: "Bash", ToolTarget: "go build ./..."},
		{SessionID: "s-check", LastEvent: "UserPromptSubmit", LastEventAt: stamp(time.Minute), ContextPercent: 72},
	} {
		if err := fix.reg.Report(context.Background(), rep); err != nil {
			t.Fatalf("Report(%s %s): %v", rep.SessionID, rep.LastEvent, err)
		}
	}
	want := []tipView{{
		ID:       "context_checkpoint:s-check",
		Rule:     "context_checkpoint",
		Session:  "s-check",
		Sentence: "Context is at 72% with nothing committed yet. A good point to finish this step and commit.",
		Count:    1,
	}}
	if owner := fix.insights(""); !reflect.DeepEqual(owner.Tips, want) {
		t.Errorf("the owner's tips = %+v, want %+v", owner.Tips, want)
	}
	if mine := fix.insights("s-check"); !reflect.DeepEqual(mine.Tips, want) {
		t.Errorf("s-check's tips = %+v, want %+v", mine.Tips, want)
	}
}
